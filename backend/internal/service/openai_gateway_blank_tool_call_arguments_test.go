package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeBlankToolCallArguments(t *testing.T) {
	t.Run("blank responses function_call arguments become {}", func(t *testing.T) {
		body := []byte(`{"model":"muse-spark-1.3","input":[{"type":"function_call","name":"mcp__node_repl","arguments":"","call_id":"call_01a088ed"}]}`)
		out := normalizeBlankToolCallArguments(body)
		require.Equal(t, "{}", gjson.GetBytes(out, "input.0.arguments").String())
	})

	t.Run("whitespace-only arguments become {}", func(t *testing.T) {
		body := []byte(`{"model":"m","input":[{"type":"custom_tool_call","name":"x","arguments":"  "}]}`)
		out := normalizeBlankToolCallArguments(body)
		require.Equal(t, "{}", gjson.GetBytes(out, "input.0.arguments").String())
	})

	t.Run("valid arguments untouched and body identical", func(t *testing.T) {
		body := []byte(`{"model":"m","input":[{"type":"function_call","name":"x","arguments":"{\"cmd\":\"ls\"}"}]}`)
		out := normalizeBlankToolCallArguments(body)
		require.Equal(t, string(body), string(out))
	})

	t.Run("non-string and missing arguments untouched", func(t *testing.T) {
		body := []byte(`{"model":"m","input":[{"type":"function_call","name":"a"},{"type":"message","role":"user","content":"hi"}]}`)
		out := normalizeBlankToolCallArguments(body)
		require.Equal(t, string(body), string(out))
	})

	t.Run("non-tool items with arguments untouched", func(t *testing.T) {
		body := []byte(`{"model":"m","input":[{"type":"message","role":"user","arguments":""}]}`)
		out := normalizeBlankToolCallArguments(body)
		require.Equal(t, string(body), string(out))
	})

	t.Run("chat completions tool_calls blank arguments become {}", func(t *testing.T) {
		body := []byte(`{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"x","arguments":""}}]}]}`)
		out := normalizeBlankToolCallArguments(body)
		require.Equal(t, "{}", gjson.GetBytes(out, "messages.0.tool_calls.0.function.arguments").String())
	})

	t.Run("invalid body returned as-is", func(t *testing.T) {
		for _, body := range [][]byte{nil, {}, []byte("not-json"), []byte(`{"input":"x"}`)} {
			require.Equal(t, string(body), string(normalizeBlankToolCallArguments(body)))
		}
	})
}
