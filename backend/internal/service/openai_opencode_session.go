package service

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	openCodeSessionHeader         = "X-OpenCode-Session"
	openCodeInboundBodyContextKey = "opencode_inbound_body"

	// openCodeUpstreamUserAgent 是出站到 opencode.ai 的客户端身份。Zen 免费额度按
	// User-Agent 判定「请求是否来自 OpenCode 客户端」：不是 opencode/<semver> 形式
	// 一律 403 FreeTierError，低于 1.17.0 返回 426 UpgradeRequired（2026-09-17 实测）。
	// 官方客户端身份必须钉死在此，绝不透传 Codex / Claude Code / curl 的 UA。
	openCodeUpstreamUserAgent = "opencode/1.18.31"

	// OpenCode 客户端的会话 ID 形如 ses_<12 位小写十六进制时间戳><14 位 base62>，共 30 字符。
	// 免费额度校验要求 x-opencode-session 严格符合该形状：长度或字符集不符同样返回
	// 403 FreeTierError（实测 ses_x / 12 位随机串 / 大写十六进制前缀均被拒），因此
	// 非该形状的客户端会话标识必须重新编码后再出站。
	openCodeSessionIDPrefix       = "ses_"
	openCodeSessionIDHexLength    = 12
	openCodeSessionIDBodyLength   = 26
	openCodeSessionIDRandomLength = openCodeSessionIDBodyLength - openCodeSessionIDHexLength
	openCodeSessionIDAlphabet     = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	openCodeSessionIDHexDigits    = "0123456789abcdef"
)

// rememberOpenCodeInboundBody keeps the client request body so protocol
// conversion (Responses↔Anthropic↔Chat Completions) can still recover
// prompt_cache_key / metadata.user_id after those fields are dropped.
func rememberOpenCodeInboundBody(c *gin.Context, body []byte) {
	if c == nil || len(body) == 0 {
		return
	}
	c.Set(openCodeInboundBodyContextKey, body)
}

func openCodeInboundBodies(c *gin.Context) [][]byte {
	if c == nil {
		return nil
	}
	raw, ok := c.Get(openCodeInboundBodyContextKey)
	if !ok {
		return nil
	}
	body, ok := raw.([]byte)
	if !ok || len(body) == 0 {
		return nil
	}
	return [][]byte{body}
}

// applyOpenCodeUpstreamIdentity stamps a ses_<26 base62> conversation id in
// x-opencode-session on outbound inference requests to opencode.ai. The Zen free
// tier rejects requests whose session header is missing or malformed (403
// FreeTierError); the Go gateway requires the session value to be present
// (MissingSessionID, 2026-09-05). The pinned User-Agent is stamped separately by
// applyOpenCodeUpstreamUserAgent, which runs BEFORE Account.ApplyHeaderOverrides
// so an explicit account-level user-agent override keeps the final say.
//
// The session id is resolved with caller headers first, then the documented body
// session fields (OpenAI prompt_cache_key / Anthropic metadata.user_id), then any
// already applied account override. A generated UUID is last-resort only for
// probes and clients that omit every stable identifier — a new UUID each turn
// would miss upstream prompt cache.
func applyOpenCodeUpstreamIdentity(c *gin.Context, account *Account, targetURL string, headers http.Header, bodies ...[]byte) {
	if account == nil || account.Type != AccountTypeAPIKey || headers == nil {
		return
	}
	if !shouldSendOpenCodeSessionHeader(account, targetURL) {
		return
	}

	payloads := append(openCodeInboundBodies(c), bodies...)
	sessionID := resolveOpenCodeSessionID(c, headers, payloads...)
	for key := range headers {
		if strings.EqualFold(key, openCodeSessionHeader) {
			delete(headers, key)
		}
	}
	headers.Set(openCodeSessionHeader, normalizeOpenCodeSessionID(sessionID))
}

func shouldSendOpenCodeSessionHeader(account *Account, targetURL string) bool {
	if account != nil && account.IsOpenCodeGoPlan() {
		return true
	}
	return isOfficialOpenCodeHost(targetURL)
}

// normalizeOpenCodeSessionID re-encodes any resolved conversation id into the
// ses_<26 base62> shape the OpenCode client sends. A value already in that shape
// passes through untouched, so a real OpenCode client keeps its own session
// identity (and its upstream prompt cache); everything else is hashed
// deterministically, which keeps the derived id stable across the turns of one
// conversation and never leaks a client-supplied opaque string into the
// upstream's session space.
func normalizeOpenCodeSessionID(sessionID string) string {
	if isOpenCodeSessionID(sessionID) {
		return sessionID
	}
	digest := sha256.Sum256([]byte(sessionID))
	body := make([]byte, 0, openCodeSessionIDBodyLength)
	body = append(body, hex.EncodeToString(digest[:openCodeSessionIDHexLength/2])...)
	for _, b := range digest[openCodeSessionIDHexLength/2 : openCodeSessionIDHexLength/2+openCodeSessionIDRandomLength] {
		body = append(body, openCodeSessionIDAlphabet[int(b)%len(openCodeSessionIDAlphabet)])
	}
	return openCodeSessionIDPrefix + string(body)
}

func isOpenCodeSessionID(sessionID string) bool {
	if !strings.HasPrefix(sessionID, openCodeSessionIDPrefix) {
		return false
	}
	body := sessionID[len(openCodeSessionIDPrefix):]
	if len(body) != openCodeSessionIDBodyLength {
		return false
	}
	for i := range openCodeSessionIDHexLength {
		if strings.IndexByte(openCodeSessionIDHexDigits, body[i]) < 0 {
			return false
		}
	}
	for _, c := range body[openCodeSessionIDHexLength:] {
		if strings.IndexByte(openCodeSessionIDAlphabet, byte(c)) < 0 {
			return false
		}
	}
	return true
}

func isOfficialOpenCodeHost(targetURL string) bool {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Hostname(), "opencode.ai")
}

func isOfficialCommandCodeHost(targetURL string) bool {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Hostname(), "api.commandcode.ai")
}

// applyOpenCodeUpstreamUserAgent 将发往官方 OpenCode / Command Code 上游的出站
// User-Agent 收敛为规范客户端身份，覆盖客户端透传值与平台默认 UA。判定规则与
// x-opencode-session 同源：opencode 平台账号，或（任意平台账号）目标为官方主机。
// 必须先于 Account.ApplyHeaderOverrides 调用——账号 header_overrides 中显式
// 配置的 user-agent 仍拥有最终决定权。
func applyOpenCodeUpstreamUserAgent(account *Account, targetURL string, headers http.Header) {
	if headers == nil {
		return
	}

	userAgent := ""
	switch {
	case isOfficialCommandCodeHost(targetURL):
		userAgent = CodexCanonicalUserAgent()
	case account != nil && account.IsOpenCodeGo(), isOfficialOpenCodeHost(targetURL):
		userAgent = openCodeUpstreamUserAgent
	default:
		return
	}
	if userAgent == "" {
		return
	}

	// 先删任意大小写变体再写入：透传链路与覆写直写 map 可能残留非 canonical 键。
	for key := range headers {
		if strings.EqualFold(key, "User-Agent") {
			delete(headers, key)
		}
	}
	headers.Set("User-Agent", userAgent)
}

func resolveOpenCodeSessionID(c *gin.Context, headers http.Header, bodies ...[]byte) string {
	if c != nil && c.Request != nil {
		if sessionID := sanitizeSessionID(c.GetHeader(openCodeSessionHeader)); sessionID != "" {
			return sessionID
		}
		if sessionID := sanitizeSessionID(explicitOpenAIHeaderSessionID(c)); sessionID != "" {
			return sessionID
		}
		if sessionID := sanitizeSessionID(ClaudeCodeSessionIDFromHeader(c)); sessionID != "" {
			return sessionID
		}
	}
	for _, body := range bodies {
		if sessionID := sanitizeSessionID(openCodeSessionIDFromPayload(body)); sessionID != "" {
			return sessionID
		}
	}
	if sessionID := sanitizeSessionID(existingOpenCodeSessionHeader(headers)); sessionID != "" {
		return sessionID
	}
	return uuid.NewString()
}

// openCodeSessionIDFromPayload reads the stable conversation id from documented
// request-body fields. Kimi Code / OpenAI clients send prompt_cache_key on
// Chat Completions and Responses; Anthropic clients send metadata.user_id on
// Messages. Neither field changes model behavior.
func openCodeSessionIDFromPayload(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	view := openAIRequestPayloadView(body)
	if sessionID := strings.TrimSpace(view.Get("prompt_cache_key").String()); sessionID != "" {
		return sessionID
	}
	return openCodeSessionIDFromMetadataUserID(view.Get("metadata.user_id").String())
}

func openCodeSessionIDFromMetadataUserID(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	if strings.HasPrefix(userID, "{") {
		if sessionID := strings.TrimSpace(gjson.Get(userID, "session_id").String()); sessionID != "" {
			return sessionID
		}
	}
	return userID
}

func openCodeSessionHintBody(promptCacheKey string) []byte {
	key := strings.TrimSpace(promptCacheKey)
	if key == "" {
		return nil
	}
	return []byte(`{"prompt_cache_key":` + strconv.Quote(key) + `}`)
}

func existingOpenCodeSessionHeader(headers http.Header) string {
	if headers == nil {
		return ""
	}
	for key, values := range headers {
		if strings.EqualFold(key, openCodeSessionHeader) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}
