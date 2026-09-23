package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenCodeZenFreeTierEndpoint(t *testing.T) {
	tests := []struct {
		url  string
		want openCodeZenEndpoint
	}{
		{"https://opencode.ai/zen/v1/chat/completions", openCodeZenEndpointChatCompletions},
		{"https://opencode.ai/zen/v1/responses", openCodeZenEndpointResponses},
		{"https://opencode.ai/zen/v1/responses/compact", openCodeZenEndpointNone},
		{"https://opencode.ai/zen/v1/systemone", openCodeZenEndpointNone},
		{"https://opencode.ai/zen/go/v1/chat/completions", openCodeZenEndpointNone},
		{"https://opencode.ai.evil.example/zen/v1/chat/completions", openCodeZenEndpointNone},
		{"http://opencode.ai/zen/v1/chat/completions", openCodeZenEndpointNone},
		{"https://api.openai.com/v1/chat/completions", openCodeZenEndpointNone},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, openCodeZenFreeTierEndpoint(tt.url), tt.url)
	}
}

func openCodeZenToolNames(body []byte, endpoint openCodeZenEndpoint) []string {
	path := "tools.#.name"
	if endpoint == openCodeZenEndpointChatCompletions {
		path = "tools.#.function.name"
	}
	var names []string
	for _, name := range gjson.GetBytes(body, path).Array() {
		names = append(names, name.String())
	}
	return names
}

func TestShapeOpenCodeZenFreeTierBodyPlainChat(t *testing.T) {
	body := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`)
	shaped, changed := shapeOpenCodeZenFreeTierBody(openCodeZenEndpointChatCompletions, body)
	require.True(t, changed)
	require.True(t, gjson.GetBytes(shaped, "stream").Bool())
	require.True(t, gjson.GetBytes(shaped, "stream_options.include_usage").Bool())
	require.Equal(t, "none", gjson.GetBytes(shaped, "tool_choice").String(), "纯对话补桩后必须禁用工具调用")
	require.Equal(t, []string{"bash", "read"}, openCodeZenToolNames(shaped, openCodeZenEndpointChatCompletions))
	require.Equal(t, "hi", gjson.GetBytes(shaped, "messages.0.content").String())
}

func TestShapeOpenCodeZenFreeTierBodyKeepsClientToolsAndChoice(t *testing.T) {
	body := []byte(`{"model":"m","stream":true,"tool_choice":"auto","tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}},{"type":"function","function":{"name":"lookup"}}]}`)
	shaped, changed := shapeOpenCodeZenFreeTierBody(openCodeZenEndpointChatCompletions, body)
	require.True(t, changed)
	require.Equal(t, []string{"bash", "lookup", "read"}, openCodeZenToolNames(shaped, openCodeZenEndpointChatCompletions))
	require.Equal(t, "auto", gjson.GetBytes(shaped, "tool_choice").String())
	require.False(t, gjson.GetBytes(shaped, "stream_options").Exists(), "客户端本就流式时不改 stream_options")
}

func TestShapeOpenCodeZenFreeTierBodyLeavesCompliantRequestUntouched(t *testing.T) {
	body := []byte(`{"model":"m","stream":true,"tools":[{"type":"function","function":{"name":"read"}},{"type":"function","function":{"name":"bash"}}]}`)
	shaped, changed := shapeOpenCodeZenFreeTierBody(openCodeZenEndpointChatCompletions, body)
	require.False(t, changed)
	require.Equal(t, string(body), string(shaped))
}

func TestShapeOpenCodeZenFreeTierBodyToolNamesAreCaseSensitive(t *testing.T) {
	body := []byte(`{"model":"m","stream":true,"tools":[{"type":"function","function":{"name":"Bash"}},{"type":"function","function":{"name":"Read"}}]}`)
	shaped, changed := shapeOpenCodeZenFreeTierBody(openCodeZenEndpointChatCompletions, body)
	require.True(t, changed, "上游按小写名校验，Bash/Read 不算数")
	require.Equal(t, []string{"Bash", "Read", "bash", "read"}, openCodeZenToolNames(shaped, openCodeZenEndpointChatCompletions))
}

func TestShapeOpenCodeZenFreeTierBodyResponses(t *testing.T) {
	body := []byte(`{"model":"muse-spark-1.3-contributor-free","input":"hi"}`)
	shaped, changed := shapeOpenCodeZenFreeTierBody(openCodeZenEndpointResponses, body)
	require.True(t, changed)
	require.True(t, gjson.GetBytes(shaped, "stream").Bool())
	require.Equal(t, []string{"bash", "read"}, openCodeZenToolNames(shaped, openCodeZenEndpointResponses))
	require.False(t, gjson.GetBytes(shaped, "tool_choice").Exists(), "muse 系 Responses 不接受 tool_choice=none")
	require.False(t, gjson.GetBytes(shaped, "stream_options").Exists())
}

func TestShapeOpenCodeZenFreeTierBodyIgnoresOtherEndpointsAndInvalidJSON(t *testing.T) {
	body := []byte(`{"model":"m"}`)
	shaped, changed := shapeOpenCodeZenFreeTierBody(openCodeZenEndpointNone, body)
	require.False(t, changed)
	require.Equal(t, string(body), string(shaped))

	invalid := []byte(`{"model":`)
	shaped, changed = shapeOpenCodeZenFreeTierBody(openCodeZenEndpointChatCompletions, invalid)
	require.False(t, changed)
	require.Equal(t, string(invalid), string(shaped))
}

func TestAggregateOpenCodeChatCompletionsSSE(t *testing.T) {
	stream := strings.Join([]string{
		`: keep-alive`,
		`data: {"id":"router-1","object":"chat.completion.chunk","created":10,"model":"big-pickle","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think "},"finish_reason":null}]}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","created":10,"model":"big-pickle","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","created":10,"model":"big-pickle","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
		`data: {"id":"router-1","object":"chat.completion.chunk","created":10,"model":"big-pickle","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		`data: [DONE]`,
	}, "\n\n")
	out, err := aggregateOpenCodeChatCompletionsSSE(strings.NewReader(stream), 0)
	require.NoError(t, err)
	require.Equal(t, "chat.completion", gjson.GetBytes(out, "object").String())
	require.Equal(t, "router-1", gjson.GetBytes(out, "id").String())
	require.Equal(t, "big-pickle", gjson.GetBytes(out, "model").String())
	require.Equal(t, "assistant", gjson.GetBytes(out, "choices.0.message.role").String())
	require.Equal(t, "Hello", gjson.GetBytes(out, "choices.0.message.content").String())
	require.Equal(t, "think ", gjson.GetBytes(out, "choices.0.message.reasoning_content").String())
	require.Equal(t, "stop", gjson.GetBytes(out, "choices.0.finish_reason").String())
	require.Equal(t, int64(7), gjson.GetBytes(out, "usage.total_tokens").Int())
}

func TestAggregateOpenCodeChatCompletionsSSEToolCalls(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"x","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]},"finish_reason":null}]}`,
		`data: {"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"go\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n")
	out, err := aggregateOpenCodeChatCompletionsSSE(strings.NewReader(stream), 0)
	require.NoError(t, err)
	require.Equal(t, "null", gjson.GetBytes(out, "choices.0.message.content").Raw)
	require.Equal(t, "call_1", gjson.GetBytes(out, "choices.0.message.tool_calls.0.id").String())
	require.Equal(t, "lookup", gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.name").String())
	require.Equal(t, `{"q":"go"}`, gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.arguments").String())
	require.False(t, gjson.GetBytes(out, "choices.0.message.tool_calls.0.index").Exists())
	require.Equal(t, "tool_calls", gjson.GetBytes(out, "choices.0.finish_reason").String())
}

func TestAggregateOpenCodeChatCompletionsSSEFailures(t *testing.T) {
	for name, stream := range map[string]string{
		"error event": `data: {"error":{"message":"rate limited"}}`,
		"truncated":   `data: {"id":"x","choices":[{"index":0,"delta":{"content":"par"},"finish_reason":null}]}`,
		"empty":       `: keep-alive`,
		"malformed":   `data: {"id":`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := aggregateOpenCodeChatCompletionsSSE(strings.NewReader(stream), 0)
			require.Error(t, err)
		})
	}
}

type openCodeZenSSEHTTPUpstream struct {
	request *http.Request
	body    []byte
}

func (u *openCodeZenSSEHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.request = req
	u.body, _ = io.ReadAll(req.Body)
	header := make(http.Header)
	header.Set("Content-Type", "text/event-stream")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body: io.NopCloser(strings.NewReader(
			"data: {\"id\":\"r\",\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"id\":\"r\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n" +
				"data: [DONE]\n\n")),
	}, nil
}

func (u *openCodeZenSSEHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestSendCCUpstreamRequestShapesZenPlainChatAndBuffersStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &openCodeZenSSEHTTPUpstream{}
	svc := openCodeSessionTestService()
	svc.httpUpstream = upstream
	account := openCodeSessionTestAccount("https://opencode.ai/zen/v1")
	c := newOpenCodeSessionTestContext(t, "")

	resp, err := svc.sendCCUpstreamRequest(
		context.Background(), c, account,
		"https://opencode.ai/zen/v1/chat/completions",
		[]byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`),
		false, "token", "", "",
	)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, "text/event-stream", upstream.request.Header.Get("Accept"))
	require.True(t, gjson.GetBytes(upstream.body, "stream").Bool())
	require.Equal(t, []string{"bash", "read"}, openCodeZenToolNames(upstream.body, openCodeZenEndpointChatCompletions))
	requireOpenCodeClientSessionID(t, upstream.request.Header)
	require.Equal(t, openCodeUpstreamUserAgent, upstream.request.Header.Get("User-Agent"))

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "chat.completion", gjson.GetBytes(out, "object").String())
	require.Equal(t, "ok", gjson.GetBytes(out, "choices.0.message.content").String())
	require.Equal(t, int64(4), gjson.GetBytes(out, "usage.total_tokens").Int())
}

func TestSendCCUpstreamRequestDoesNotShapeZenGoPlan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &openCodeZenSSEHTTPUpstream{}
	svc := openCodeSessionTestService()
	svc.httpUpstream = upstream
	account := openCodeSessionTestAccount("https://opencode.ai/zen/go/v1")
	c := newOpenCodeSessionTestContext(t, "")
	body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`

	resp, err := svc.sendCCUpstreamRequest(
		context.Background(), c, account,
		"https://opencode.ai/zen/go/v1/chat/completions", []byte(body),
		false, "token", "", "",
	)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.JSONEq(t, body, string(upstream.body))
	require.Equal(t, "application/json", upstream.request.Header.Get("Accept"))
}

func TestBuildUpstreamRequestShapesZenResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	account := openCodeSessionTestAccount("https://opencode.ai/zen/v1")
	c := newOpenCodeSessionTestContext(t, "")

	req, err := svc.buildUpstreamRequest(
		context.Background(), c, account,
		[]byte(`{"model":"muse-spark-1.3-contributor-free","input":"hi"}`), "token", false, "", false,
	)
	require.NoError(t, err)
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(body, "stream").Bool())
	require.Equal(t, []string{"bash", "read"}, openCodeZenToolNames(body, openCodeZenEndpointResponses))
}

func TestUpgradeStaleOpenCodeUserAgent(t *testing.T) {
	tests := []struct{ in, want string }{
		{"opencode/1.4.3", openCodeUpstreamUserAgent},
		{"opencode/1.17.9 ai-sdk/provider-utils/4.0.23", openCodeUpstreamUserAgent},
		{"opencode/1.18.32 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14", "opencode/1.18.32 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14"},
		{openCodeUpstreamUserAgent, openCodeUpstreamUserAgent},
		{"custom-relay-agent/2.0", "custom-relay-agent/2.0"},
		{"", ""},
	}
	for _, tt := range tests {
		headers := make(http.Header)
		if tt.in != "" {
			headers["user-agent"] = []string{tt.in}
		}
		upgradeStaleOpenCodeUserAgent(headers)
		got := ""
		for key, values := range headers {
			if strings.EqualFold(key, "User-Agent") {
				got = values[0]
			}
		}
		require.Equal(t, tt.want, got, tt.in)
		if tt.want != "" {
			require.Len(t, headers, 1, "不得残留重复的 UA 键")
		}
	}
}

func TestSendCCUpstreamRequestUpgradesStaleOpenCodeUserAgentOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &openCodeZenSSEHTTPUpstream{}
	svc := openCodeSessionTestService()
	svc.httpUpstream = upstream
	account := openCodeSessionTestAccount("https://opencode.ai/zen/v1")
	account.Credentials[credKeyHeaderOverrides] = map[string]any{
		"user-agent":         "opencode/1.4.3",
		"x-opencode-session": "cliproxy-opencode-go-session",
	}
	c := newOpenCodeSessionTestContext(t, "")

	resp, err := svc.sendCCUpstreamRequest(
		context.Background(), c, account,
		"https://opencode.ai/zen/v1/chat/completions",
		[]byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`),
		true, "token", "", "",
	)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, openCodeUpstreamUserAgent, upstream.request.Header.Get("User-Agent"))
	requireOpenCodeClientSessionID(t, upstream.request.Header)
}
