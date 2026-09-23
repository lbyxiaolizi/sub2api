package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenCode Zen 免费额度（opencode.ai/zen，非 /zen/go 付费计划）除身份头
// （User-Agent + x-opencode-session，见 openai_opencode_session.go）外，还按请求体
// 判定「是否来自 OpenCode 客户端」，不满足一律 403 FreeTierError（2026-09-24 实测）：
//
//   - stream 必须为 true：stream=false / 缺省均被拒；
//   - tools 必须同时含名为 bash 与 read 的工具：只校验名字（区分大小写，Bash/Read
//     被拒），不校验 schema / description；system prompt、tool_choice 不参与判定。
//
// 真实 OpenCode 客户端恒满足上述条件；纯对话（无工具、非流式）的中转请求则全部被拒。
// 出站前补齐缺失的工具桩并强制流式；客户端要非流式时，由网关把上游 SSE 聚合回
// 单个 JSON（Chat Completions 见 aggregateOpenCodeChatCompletionsSSE，Responses
// 由 handleNonStreamingResponse 的 SSE→JSON 既有逻辑兜底）。
var openCodeZenFreeTierToolNames = []string{"bash", "read"}

// openCodeZenFreeTierStubDescription 让模型不去调用补入的工具桩：实测配合
// tool_choice=none 时，即便用户显式要求执行命令，模型也只回复纯文本。
const openCodeZenFreeTierStubDescription = "Disabled in this session. Never call this tool; answer in plain text."

type openCodeZenEndpoint int

const (
	openCodeZenEndpointNone openCodeZenEndpoint = iota
	openCodeZenEndpointChatCompletions
	openCodeZenEndpointResponses
)

// openCodeZenFreeTierEndpoint 判定出站目标是否为受免费额度校验的 Zen 推理端点。
// /zen/go（付费计划）、SystemOne、/responses/compact 等不在此列。
func openCodeZenFreeTierEndpoint(targetURL string) openCodeZenEndpoint {
	if !isOfficialOpenCodeHost(targetURL) {
		return openCodeZenEndpointNone
	}
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return openCodeZenEndpointNone
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasPrefix(path, "/zen/") || strings.HasPrefix(path, "/zen/go/") {
		return openCodeZenEndpointNone
	}
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return openCodeZenEndpointChatCompletions
	case strings.HasSuffix(path, "/responses"):
		return openCodeZenEndpointResponses
	default:
		return openCodeZenEndpointNone
	}
}

// shapeOpenCodeZenFreeTierBody 把出站请求体补齐为 Zen 免费额度可接受的形状：
// 强制 stream=true，并补入缺失的 bash / read 工具桩。只做增量修改——已满足条件的
// 请求（真实 OpenCode 客户端、已带这些工具的 harness）原样放行。changed 为 false
// 时返回原 body。
func shapeOpenCodeZenFreeTierBody(endpoint openCodeZenEndpoint, body []byte) (shaped []byte, changed bool) {
	if endpoint == openCodeZenEndpointNone || len(body) == 0 || !gjson.ValidBytes(body) {
		return body, false
	}
	out := body
	var err error

	hadTools := len(gjson.GetBytes(out, "tools").Array()) > 0
	present := make(map[string]bool, len(openCodeZenFreeTierToolNames))
	for _, tool := range gjson.GetBytes(out, "tools").Array() {
		name := tool.Get("name").String()
		if endpoint == openCodeZenEndpointChatCompletions {
			name = tool.Get("function.name").String()
		}
		present[name] = true
	}
	for _, name := range openCodeZenFreeTierToolNames {
		if present[name] {
			continue
		}
		out, err = sjson.SetRawBytes(out, "tools.-1", openCodeZenFreeTierStubTool(endpoint, name))
		if err != nil {
			return body, false
		}
		changed = true
	}
	// 纯对话请求原本无工具：Chat Completions 再显式禁用工具调用，保证回复仍是纯文本。
	// Responses 端（muse 系）不接受 tool_choice=none（400），仅靠桩描述约束。
	if changed && !hadTools && endpoint == openCodeZenEndpointChatCompletions &&
		!gjson.GetBytes(out, "tool_choice").Exists() {
		if out, err = sjson.SetBytes(out, "tool_choice", "none"); err != nil {
			return body, false
		}
	}

	if !gjson.GetBytes(out, "stream").Bool() {
		if out, err = sjson.SetBytes(out, "stream", true); err != nil {
			return body, false
		}
		// 客户端原本非流式：带上 include_usage，聚合后的 JSON 才有用量可计费。
		if endpoint == openCodeZenEndpointChatCompletions {
			if out, err = sjson.SetBytes(out, "stream_options.include_usage", true); err != nil {
				return body, false
			}
		}
		changed = true
	}
	if !changed {
		return body, false
	}
	return out, true
}

func openCodeZenFreeTierStubTool(endpoint openCodeZenEndpoint, name string) []byte {
	params := json.RawMessage(`{"type":"object","properties":{}}`)
	var tool any
	if endpoint == openCodeZenEndpointChatCompletions {
		tool = apicompat.ChatTool{
			Type: "function",
			Function: &apicompat.ChatFunction{
				Name:        name,
				Description: openCodeZenFreeTierStubDescription,
				Parameters:  params,
			},
		}
	} else {
		tool = map[string]any{
			"type":        "function",
			"name":        name,
			"description": openCodeZenFreeTierStubDescription,
			"parameters":  params,
		}
	}
	raw, _ := json.Marshal(tool)
	return raw
}

// bufferOpenCodeZenChatCompletionsSSE 在客户端要非流式、而出站被强制为流式时，
// 把上游 CC SSE 聚合为 chat.completion JSON 并替换 resp.Body / Content-Type，
// 使下游各 CC 非流式读取器（raw 透传、Responses/Messages 回退）无需感知。
// 聚合失败（流中报错、截断、坏 chunk）改写为 502 JSON 错误，走调用方错误链。
func bufferOpenCodeZenChatCompletionsSSE(resp *http.Response, maxLineSize int) {
	if resp == nil || resp.Body == nil || resp.StatusCode >= 400 || !isEventStreamResponse(resp.Header) {
		return
	}
	body, err := aggregateOpenCodeChatCompletionsSSE(resp.Body, maxLineSize)
	_ = resp.Body.Close()
	if err != nil {
		resp.StatusCode = http.StatusBadGateway
		resp.Status = strconv.Itoa(http.StatusBadGateway) + " " + http.StatusText(http.StatusBadGateway)
		body, _ = json.Marshal(map[string]any{
			"error": map[string]any{"type": "upstream_error", "message": err.Error()},
		})
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Del("Content-Length")
}

type openCodeChatChoiceAccumulator struct {
	role             string
	content          strings.Builder
	reasoningContent strings.Builder
	reasoning        strings.Builder
	sawContent       bool
	toolCalls        map[int]*apicompat.ChatToolCall
	toolOrder        []int
	finishReason     string
}

// aggregateOpenCodeChatCompletionsSSE 把 Chat Completions SSE 流折叠为非流式响应体。
func aggregateOpenCodeChatCompletionsSSE(r io.Reader, maxLineSize int) ([]byte, error) {
	if maxLineSize <= 0 {
		maxLineSize = defaultMaxLineSize
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	out := apicompat.ChatCompletionsResponse{Object: "chat.completion"}
	choices := map[int]*openCodeChatChoiceAccumulator{}
	sawChunk, sawDone := false, false

	for scanner.Scan() {
		payload, ok := extractOpenAISSEDataLine(scanner.Text())
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			sawDone = true
			break
		}
		if errMsg := gjson.Get(payload, "error"); errMsg.Exists() {
			msg := strings.TrimSpace(errMsg.Get("message").String())
			if msg == "" {
				msg = strings.TrimSpace(errMsg.String())
			}
			return nil, fmt.Errorf("upstream stream error: %s", msg)
		}
		var chunk apicompat.ChatCompletionsChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil, fmt.Errorf("malformed chat stream chunk: %w", err)
		}
		sawChunk = true
		if chunk.ID != "" {
			out.ID = chunk.ID
		}
		if chunk.Created != 0 {
			out.Created = chunk.Created
		}
		if chunk.Model != "" {
			out.Model = chunk.Model
		}
		if chunk.SystemFingerprint != "" {
			out.SystemFingerprint = chunk.SystemFingerprint
		}
		if chunk.ServiceTier != "" {
			out.ServiceTier = chunk.ServiceTier
		}
		if chunk.Usage != nil {
			out.Usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			acc := choices[choice.Index]
			if acc == nil {
				acc = &openCodeChatChoiceAccumulator{toolCalls: map[int]*apicompat.ChatToolCall{}}
				choices[choice.Index] = acc
			}
			acc.absorb(choice)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read upstream stream: %w", err)
	}
	if !sawChunk {
		return nil, fmt.Errorf("upstream stream ended without any chunk")
	}
	if !sawDone && !anyChoiceFinished(choices) {
		return nil, fmt.Errorf("upstream stream truncated before completion")
	}

	indexes := make([]int, 0, len(choices))
	for index := range choices {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		out.Choices = append(out.Choices, choices[index].choice(index))
	}
	if out.Choices == nil {
		out.Choices = []apicompat.ChatChoice{}
	}
	return json.Marshal(out)
}

func anyChoiceFinished(choices map[int]*openCodeChatChoiceAccumulator) bool {
	for _, acc := range choices {
		if acc.finishReason != "" {
			return true
		}
	}
	return false
}

func (a *openCodeChatChoiceAccumulator) absorb(choice apicompat.ChatChunkChoice) {
	delta := choice.Delta
	if delta.Role != "" {
		a.role = delta.Role
	}
	if delta.Content != nil {
		a.sawContent = true
		a.content.WriteString(*delta.Content)
	}
	if delta.ReasoningContent != nil {
		a.reasoningContent.WriteString(*delta.ReasoningContent)
	}
	if delta.Reasoning != nil {
		a.reasoning.WriteString(*delta.Reasoning)
	}
	for position, call := range delta.ToolCalls {
		index := position
		if call.Index != nil {
			index = *call.Index
		}
		existing := a.toolCalls[index]
		if existing == nil {
			existing = &apicompat.ChatToolCall{Type: "function"}
			a.toolCalls[index] = existing
			a.toolOrder = append(a.toolOrder, index)
		}
		if call.ID != "" {
			existing.ID = call.ID
		}
		if call.Type != "" {
			existing.Type = call.Type
		}
		if call.Function.Name != "" {
			existing.Function.Name = call.Function.Name
		}
		existing.Function.Arguments += call.Function.Arguments
	}
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		a.finishReason = *choice.FinishReason
	}
}

func (a *openCodeChatChoiceAccumulator) choice(index int) apicompat.ChatChoice {
	role := a.role
	if role == "" {
		role = "assistant"
	}
	msg := apicompat.ChatMessage{
		Role:             role,
		ReasoningContent: a.reasoningContent.String(),
		Reasoning:        a.reasoning.String(),
	}
	if a.sawContent || len(a.toolOrder) == 0 {
		msg.Content, _ = json.Marshal(a.content.String())
	} else {
		msg.Content = json.RawMessage("null")
	}
	sort.Ints(a.toolOrder)
	for _, toolIndex := range a.toolOrder {
		call := *a.toolCalls[toolIndex]
		call.Index = nil
		msg.ToolCalls = append(msg.ToolCalls, call)
	}
	finish := a.finishReason
	if finish == "" {
		finish = "stop"
	}
	return apicompat.ChatChoice{Index: index, Message: msg, FinishReason: finish}
}
