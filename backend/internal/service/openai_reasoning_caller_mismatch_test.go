package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const callerMismatchResponse = "{\"error\":{\"type\":\"invalid_request_error\",\"message\":\"Error from provider (Console): Upstream request failed: [invalid_request_error] reasoning `encrypted_content` was not issued to this caller\"}}"

func TestForwardReasoningCallerMismatch(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, failsAgain := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough=%t/fails_again=%t", passthrough, failsAgain), func(t *testing.T) {
				body := []byte(`{"model":"muse-spark-1.3","stream":false,"store":false,"prompt_cache_key":"same-session","input":[{"role":"user","content":"hi"},{"type":"reasoning","summary":[],"encrypted_content":"caller-bound-blob"},{"type":"message","role":"assistant","content":"hello"},{"type":"function_call","call_id":"call_1","name":"inspect","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"},{"role":"user","content":"check local"}]}`)
				original := string(body)
				second := newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"id":"resp_ok","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
				if failsAgain {
					second = newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, callerMismatchResponse)
				}
				upstream := &httpUpstreamRecorder{responses: []*http.Response{
					newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, callerMismatchResponse), second,
				}}
				account := newOpenAIRejectedFieldTestAccount()
				account.Extra["openai_passthrough"] = passthrough
				result, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), newOpenAIRejectedFieldTestContext(body), account, body)
				if failsAgain {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					require.NotNil(t, result)
				}
				require.Len(t, upstream.bodies, 2, "caller mismatch must retry only once")
				require.Equal(t, original, string(body), "canonical history must remain immutable")
				require.Equal(t, "caller-bound-blob", gjson.GetBytes(upstream.bodies[0], "input.1.encrypted_content").String())
				before := gjson.GetBytes(upstream.bodies[0], "input").Array()
				after := gjson.GetBytes(upstream.bodies[1], "input").Array()
				require.Len(t, after, len(before)-1)
				for i, sourceIndex := range []int{0, 2, 3, 4, 5} {
					require.JSONEq(t, before[sourceIndex].Raw, after[i].Raw, "visible messages and tool history must survive")
				}
				require.Equal(t, "same-session", gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String())
			})
		}
	}
}

func TestReasoningCallerMismatchDoesNotOvermatch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		errorBody string
	}{
		{"no encrypted reasoning", 400, `{"input":[{"role":"user","content":"hi"}]}`, callerMismatchResponse},
		{"unrelated error", 400, `{"input":[{"type":"reasoning","encrypted_content":"keep"}]}`, `{"error":{"message":"encrypted_content is missing"}}`},
		{"auth error", 401, `{"input":[{"type":"reasoning","encrypted_content":"keep"}]}`, callerMismatchResponse},
		{"server error", 500, `{"input":[{"type":"reasoning","encrypted_content":"keep"}]}`, callerMismatchResponse},
		{"compaction preserved", 400, `{"input":[{"type":"compaction","encrypted_content":"keep"}]}`, callerMismatchResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(tc.status, []byte(tc.body), []byte(tc.errorBody))
			require.NoError(t, err)
			require.False(t, changed)
		})
	}
}
