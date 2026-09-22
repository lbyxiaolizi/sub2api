package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newOpenCodeSessionTestContext(t *testing.T, value string) *gin.Context {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if value != "" {
		c.Request.Header.Set(openCodeSessionHeader, value)
	}
	return c
}

func openCodeSessionTestService() *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		},
	}}
}

func openCodeSessionTestAccount(baseURL string) *Account {
	return &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url":                   baseURL,
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       map[string]any{"x-opencode-session": "fixed-account-value"},
		},
	}
}

func requireSingleOpenCodeSessionHeader(t *testing.T, headers http.Header, want string) {
	t.Helper()
	count := 0
	for key, values := range headers {
		if strings.EqualFold(key, openCodeSessionHeader) {
			count += len(values)
			require.Equal(t, []string{want}, values)
		}
	}
	require.Equal(t, 1, count)
}

// requireOpenCodeClientSessionID asserts the header carries a value in the shape the
// OpenCode client emits (ses_<12 lowercase hex><14 base62>) and returns it.
func requireOpenCodeClientSessionID(t *testing.T, headers http.Header) string {
	t.Helper()
	return requireOpenCodeClientSessionValue(t, headers.Get(openCodeSessionHeader))
}

func requireOpenCodeClientSessionValue(t *testing.T, value string) string {
	t.Helper()
	require.True(t, isOpenCodeSessionID(value), "x-opencode-session 必须符合客户端形状, got %q", value)
	return value
}

// openCodeIdentitySessionFor runs the outbound identity helper for a single target
// and returns the resulting x-opencode-session value.
func openCodeIdentitySessionFor(t *testing.T, c *gin.Context, account *Account, targetURL string, bodies ...[]byte) string {
	t.Helper()
	headers := make(http.Header)
	applyOpenCodeUpstreamIdentity(c, account, targetURL, headers, bodies...)
	return headers.Get(openCodeSessionHeader)
}

func TestApplyOpenCodeUpstreamIdentityTrustBoundary(t *testing.T) {
	tests := []struct {
		name       string
		account    *Account
		targetURL  string
		incoming   string
		wantHeader bool
	}{
		{
			name:       "official origin",
			account:    openCodeSessionTestAccount("https://opencode.ai/zen/v1"),
			targetURL:  "https://opencode.ai/zen/v1/chat/completions",
			incoming:   " conversation-123 ",
			wantHeader: true,
		},
		{
			name:      "lookalike origin",
			account:   openCodeSessionTestAccount("https://opencode.ai.evil.example/v1"),
			targetURL: "https://opencode.ai.evil.example/v1/responses",
			incoming:  "conversation-123",
		},
		{
			name:      "subdomain is not implicitly trusted",
			account:   openCodeSessionTestAccount("https://api.opencode.ai/v1"),
			targetURL: "https://api.opencode.ai/v1/responses",
			incoming:  "conversation-123",
		},
		{
			name:      "insecure official origin",
			account:   openCodeSessionTestAccount("http://opencode.ai/zen/v1"),
			targetURL: "http://opencode.ai/zen/v1/responses",
			incoming:  "conversation-123",
		},
		{
			name:       "missing caller value generates a client-format session",
			account:    openCodeSessionTestAccount("https://opencode.ai/zen/go/v1"),
			targetURL:  "https://opencode.ai/zen/go/v1/responses",
			wantHeader: true,
		},
		{
			name:       "zen origin generates a client-format session for free tier",
			account:    openCodeSessionTestAccount("https://opencode.ai/zen/v1"),
			targetURL:  "https://opencode.ai/zen/v1/responses",
			wantHeader: true,
		},
		{
			name:      "oauth account",
			account:   &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			targetURL: "https://opencode.ai/zen/v1/responses",
			incoming:  "conversation-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			applyOpenCodeUpstreamIdentity(newOpenCodeSessionTestContext(t, tt.incoming), tt.account, tt.targetURL, headers)
			if !tt.wantHeader {
				require.Empty(t, headers.Get(openCodeSessionHeader))
				return
			}
			got := requireOpenCodeClientSessionID(t, headers)
			require.NotEqual(t, strings.TrimSpace(tt.incoming), got, "客户端原始会话串不得原样出站")
		})
	}
}

func TestApplyOpenCodeUpstreamIdentityStampsSessionButKeepsRelayUserAgent(t *testing.T) {
	account := &Account{
		ID:       4,
		Platform: PlatformOpenCodeGo,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://relay.example.com/v1",
		},
	}
	headers := make(http.Header)
	headers.Set("User-Agent", "codex_cli_rs/0.144.0")
	applyOpenCodeUpstreamIdentity(newOpenCodeSessionTestContext(t, ""), account, "https://relay.example.com/v1/chat/completions", headers)
	requireOpenCodeClientSessionID(t, headers)
	require.Equal(t, "codex_cli_rs/0.144.0", headers.Get("User-Agent"), "自建中转不适用 opencode.ai 客户端身份")
}

func TestApplyOpenCodeUpstreamIdentityMapsCallerSessionIDIntoClientShape(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}
	const targetURL = "https://opencode.ai/zen/go/v1/chat/completions"

	withCallerID := func() string {
		c := newOpenCodeSessionTestContext(t, "")
		c.Request.Header.Set("session_id", "conv-from-client")
		return openCodeIdentitySessionFor(t, c, account, targetURL)
	}

	first := withCallerID()
	requireOpenCodeClientSessionValue(t, first)
	require.NotEqual(t, "conv-from-client", first, "客户端不透明会话串不得原样出站")
	require.Equal(t, first, withCallerID(), "同一会话跨轮必须稳定，否则丢失上游 prompt cache")
}

func TestApplyOpenCodeUpstreamIdentityRejectsControlCharsInPromptCacheKey(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}
	body := []byte("{\"model\":\"glm-5.3\",\"prompt_cache_key\":\"a\\nb\",\"input\":\"hello\"}")
	headers := make(http.Header)
	applyOpenCodeUpstreamIdentity(newOpenCodeSessionTestContext(t, ""), account, "https://opencode.ai/zen/go/v1/responses", headers, body)
	got := requireOpenCodeClientSessionID(t, headers)
	require.NotContains(t, got, "\n")
	require.NotEqual(t, "a\nb", got)
}

func TestApplyOpenCodeUpstreamIdentityDerivesStableSessionFromPromptCacheKey(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}
	const targetURL = "https://opencode.ai/zen/go/v1/responses"

	turn := func(promptCacheKey string) string {
		body := []byte(`{"model":"grok-4.6","prompt_cache_key":"` + promptCacheKey + `","input":"hello"}`)
		return openCodeIdentitySessionFor(t, newOpenCodeSessionTestContext(t, ""), account, targetURL, body)
	}

	first := turn("kimi-session-42")
	requireOpenCodeClientSessionValue(t, first)
	require.Equal(t, first, turn("kimi-session-42"), "同一 prompt_cache_key 跨轮不得变化")
	require.NotEqual(t, first, turn("kimi-session-43"), "不同会话必须得到不同 id")
}

func TestApplyOpenCodeUpstreamIdentityUsesAnthropicMetadataUserID(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}
	const targetURL = "https://opencode.ai/zen/go/v1/messages"

	fromUserID := func(userID string) string {
		body := []byte(`{"model":"minimax-m3","metadata":{"user_id":"` + userID + `"},"messages":[{"role":"user","content":"hi"}]}`)
		return openCodeIdentitySessionFor(t, newOpenCodeSessionTestContext(t, ""), account, targetURL, body)
	}

	first := fromUserID("coding-agent-session")
	requireOpenCodeClientSessionValue(t, first)
	require.Equal(t, first, fromUserID("coding-agent-session"))
	require.NotEqual(t, first, fromUserID("other-session"))
}

func TestApplyOpenCodeUpstreamIdentityUnwrapsClaudeCodeMetadataSessionJSON(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}
	const targetURL = "https://opencode.ai/zen/go/v1/messages"

	unwrapped := openCodeIdentitySessionFor(t, newOpenCodeSessionTestContext(t, ""), account, targetURL,
		[]byte(`{"model":"claude-sonnet-4","metadata":{"user_id":"{\"session_id\":\"meta-session-xyz\"}"},"messages":[]}`))
	requireOpenCodeClientSessionValue(t, unwrapped)

	direct := openCodeIdentitySessionFor(t, newOpenCodeSessionTestContext(t, ""), account, targetURL,
		[]byte(`{"model":"claude-sonnet-4","metadata":{"user_id":"meta-session-xyz"},"messages":[]}`))
	require.Equal(t, direct, unwrapped, "包装 JSON 必须解出 session_id 后再映射")
}

func TestApplyOpenCodeUpstreamIdentitySessionSourcePrecedence(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}
	const targetURL = "https://opencode.ai/zen/go/v1/responses"
	fromBody := []byte(`{"prompt_cache_key":"from-body"}`)

	headerOnly := openCodeIdentitySessionFor(t, newOpenCodeSessionTestContext(t, "from-header"), account, targetURL)
	bodyOnly := openCodeIdentitySessionFor(t, newOpenCodeSessionTestContext(t, ""), account, targetURL, fromBody)
	generated := openCodeIdentitySessionFor(t, newOpenCodeSessionTestContext(t, ""), account, targetURL)
	requireOpenCodeClientSessionValue(t, headerOnly)

	// header 优先于 body；body 优先于无来源兜底。
	require.Equal(t, headerOnly,
		openCodeIdentitySessionFor(t, newOpenCodeSessionTestContext(t, "from-header"), account, targetURL, fromBody))
	require.Equal(t, bodyOnly,
		openCodeIdentitySessionFor(t, newOpenCodeSessionTestContext(t, ""), account, targetURL, fromBody))
	require.NotEqual(t, headerOnly, bodyOnly)
	require.NotEqual(t, bodyOnly, generated)
}

func TestApplyOpenCodeUpstreamIdentityKeepsClientSessionIDAfterConversion(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey}
	const targetURL = "https://opencode.ai/zen/go/v1/responses"

	// 真实 OpenCode 客户端自带的 ses_ id 必须原样出站。
	clientSession := "ses_f52c5b544ffeUt3bKqeaJsIeV0"
	c := newOpenCodeSessionTestContext(t, clientSession)
	require.Equal(t, clientSession, openCodeIdentitySessionFor(t, c, account, targetURL))

	// 协议转换丢掉 prompt_cache_key 后，回溯入站 body 仍取到同一会话。
	c2 := newOpenCodeSessionTestContext(t, "")
	rememberOpenCodeInboundBody(c2, []byte(`{"model":"gpt-5","prompt_cache_key":"inbound-responses-session","input":"hello"}`))
	converted := openCodeIdentitySessionFor(t, c2, account, targetURL, []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello"}]}`))
	fromInboundBody := openCodeIdentitySessionFor(t, newOpenCodeSessionTestContext(t, ""), account, targetURL,
		[]byte(`{"model":"gpt-5","prompt_cache_key":"inbound-responses-session","input":"hello"}`))
	require.Equal(t, fromInboundBody, converted, "转换后的请求必须沿用入站会话")

	c3 := newOpenCodeSessionTestContext(t, "")
	rememberOpenCodeInboundBody(c3, []byte(`{"model":"grok-4.6","metadata":{"user_id":"inbound-messages-session"},"messages":[{"role":"user","content":"hi"}]}`))
	convertedResponses := openCodeIdentitySessionFor(t, c3, account, targetURL, []byte(`{"model":"grok-4.6","input":"hi"}`))
	fromInboundMetadata := openCodeIdentitySessionFor(t, newOpenCodeSessionTestContext(t, ""), account, targetURL,
		[]byte(`{"model":"grok-4.6","metadata":{"user_id":"inbound-messages-session"},"messages":[{"role":"user","content":"hi"}]}`))
	require.Equal(t, fromInboundMetadata, convertedResponses)
	require.NotEqual(t, converted, convertedResponses)
}

func TestOpenCodeSessionIDFromPayloadIgnoresEmptyBody(t *testing.T) {
	require.Empty(t, openCodeSessionIDFromPayload(nil))
	require.Empty(t, openCodeSessionIDFromPayload([]byte(`{"model":"gpt-5"}`)))
}

// Zen 免费额度按 x-opencode-session 的形状判定客户端：长度或字符集不符即 403，
// 因此形状本身就是必须守住的契约（真机实测：ses_x / 12 位随机串 / 大写十六进制前缀被拒）。
func TestIsOpenCodeSessionIDAcceptsOnlyClientShape(t *testing.T) {
	valid := []string{
		"ses_f52c5b544ffeUt3bKqeaJsIeV0",
		openCodeSessionIDPrefix + strings.Repeat("a", openCodeSessionIDBodyLength),
		openCodeSessionIDPrefix + strings.Repeat("0", openCodeSessionIDHexLength) + strings.Repeat("Z", openCodeSessionIDRandomLength),
	}
	for _, id := range valid {
		require.True(t, isOpenCodeSessionID(id), id)
	}

	invalid := []string{
		"",
		"conversation-123",
		openCodeSessionIDPrefix,
		openCodeSessionIDPrefix + "x",
		openCodeSessionIDPrefix + strings.Repeat("a", openCodeSessionIDBodyLength-1),
		openCodeSessionIDPrefix + strings.Repeat("a", openCodeSessionIDBodyLength+1),
		openCodeSessionIDPrefix + strings.Repeat("A", openCodeSessionIDHexLength) + strings.Repeat("a", openCodeSessionIDRandomLength),
		openCodeSessionIDPrefix + strings.Repeat("a", openCodeSessionIDBodyLength-1) + "!",
		openCodeSessionIDPrefix + strings.Repeat("a", openCodeSessionIDBodyLength-1) + "\n",
		"abc_" + strings.Repeat("a", openCodeSessionIDBodyLength),
	}
	for _, id := range invalid {
		require.False(t, isOpenCodeSessionID(id), id)
	}
}

func TestNormalizeOpenCodeSessionIDIsDeterministicAndValid(t *testing.T) {
	for _, raw := range []string{"conv-from-client", "kimi-session-42", "a\nb", "ses_short", strings.Repeat("x", 200)} {
		got := normalizeOpenCodeSessionID(raw)
		require.True(t, isOpenCodeSessionID(got), "raw=%q got=%q", raw, got)
		require.Equal(t, got, normalizeOpenCodeSessionID(raw), "同一入参必须映射到同一 id")
		require.NotEqual(t, raw, got)
	}

	// 真实客户端 id 原样保留。
	clientID := "ses_f52c5b544ffeUt3bKqeaJsIeV0"
	require.Equal(t, clientID, normalizeOpenCodeSessionID(clientID))
}

func TestOpenCodeUpstreamIdentityForwardedByResponsesBuilders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	account := openCodeSessionTestAccount("https://opencode.ai/zen/v1")
	body := []byte(`{"model":"gpt-5","input":"hello"}`)

	tests := []struct {
		name  string
		build func(*gin.Context) (*http.Request, error)
	}{
		{
			name: "normal responses",
			build: func(c *gin.Context) (*http.Request, error) {
				return svc.buildUpstreamRequest(context.Background(), c, account, body, "token", false, "", false)
			},
		},
		{
			name: "passthrough responses",
			build: func(c *gin.Context) (*http.Request, error) {
				return svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "token")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newOpenCodeSessionTestContext(t, "conversation-456")
			c.Request.Header.Set("User-Agent", "claude-cli/2.1.260 (external, cli)")
			req, err := tt.build(c)
			require.NoError(t, err)
			requireOpenCodeClientSessionID(t, req.Header)
			require.Equal(t, openCodeUpstreamUserAgent, req.Header.Get("User-Agent"), "Zen 免费额度按 UA 判定客户端")
		})
	}
}

func TestOpenCodeSessionForwardedFromPromptCacheKeyWithoutCallerHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	account := &Account{
		ID:       1,
		Platform: PlatformOpenCodeGo,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://opencode.ai/zen/go/v1",
		},
	}
	c := newOpenCodeSessionTestContext(t, "")
	body := []byte(`{"model":"gpt-5","prompt_cache_key":"stable-cache-key","input":"hello"}`)
	req, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "token", false, "", false)
	require.NoError(t, err)
	got := requireOpenCodeClientSessionID(t, req.Header)

	stable := newOpenCodeSessionTestContext(t, "")
	req2, err := svc.buildUpstreamRequest(context.Background(), stable, account, body, "token", false, "", false)
	require.NoError(t, err)
	requireSingleOpenCodeSessionHeader(t, req2.Header, got)
}

func TestOpenCodeSessionMissingCallerValueMapsAccountOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	account := openCodeSessionTestAccount("https://opencode.ai/zen/v1")
	c := newOpenCodeSessionTestContext(t, "")
	c.Request.Header.Set("User-Agent", "curl/8.6.0")

	req, err := svc.buildUpstreamRequest(
		context.Background(), c, account,
		[]byte(`{"model":"gpt-5","input":"hello"}`), "token", false, "", false,
	)
	require.NoError(t, err)
	got := requireOpenCodeClientSessionID(t, req.Header)
	require.NotEqual(t, "fixed-account-value", got, "账号级覆写值同样要映射成客户端形状")
	require.Equal(t, openCodeUpstreamUserAgent, req.Header.Get("User-Agent"))
}

type openCodeSessionHTTPUpstream struct {
	request *http.Request
}

func (u *openCodeSessionHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.request = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}, nil
}

func (u *openCodeSessionHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestApplyOpenCodeUpstreamUserAgent(t *testing.T) {
	tests := []struct {
		name      string
		account   *Account
		targetURL string
		seed      string
		want      string
	}{
		{
			name:      "opencode go account canonicalizes passthrough UA",
			account:   &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey},
			targetURL: "https://opencode.ai/zen/go/v1/chat/completions",
			seed:      "Python-urllib/3.13",
			want:      openCodeUpstreamUserAgent,
		},
		{
			name:      "opencode go account canonicalizes on custom relay too",
			account:   &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey},
			targetURL: "https://relay.example.com/v1/chat/completions",
			seed:      "Python-urllib/3.13",
			want:      openCodeUpstreamUserAgent,
		},
		{
			name:      "non-opencode account targeting official host",
			account:   &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			targetURL: "https://opencode.ai/zen/go/v1/responses",
			seed:      "Python-urllib/3.13",
			want:      openCodeUpstreamUserAgent,
		},
		{
			name:      "official command code host uses canonical Codex UA",
			account:   &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			targetURL: "https://api.commandcode.ai/provider/v1/responses",
			seed:      "Python-urllib/3.13",
			want:      CodexCanonicalUserAgent(),
		},
		{
			name:      "command code lookalike host keeps client UA",
			account:   &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			targetURL: "https://api.commandcode.ai.evil.example/provider/v1/responses",
			seed:      "Python-urllib/3.13",
			want:      "Python-urllib/3.13",
		},
		{
			name:      "command code subdomain keeps client UA",
			account:   &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			targetURL: "https://proxy.api.commandcode.ai/provider/v1/responses",
			seed:      "Python-urllib/3.13",
			want:      "Python-urllib/3.13",
		},
		{
			name:      "insecure command code host keeps client UA",
			account:   &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			targetURL: "http://api.commandcode.ai/provider/v1/responses",
			seed:      "Python-urllib/3.13",
			want:      "Python-urllib/3.13",
		},
		{
			name:      "non-official account and host keeps client UA",
			account:   &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			targetURL: "https://api.openai.com/v1/chat/completions",
			seed:      "Python-urllib/3.13",
			want:      "Python-urllib/3.13",
		},
		{
			name:      "lookalike host does not count as official",
			account:   &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			targetURL: "https://opencode.ai.evil.example/v1/chat/completions",
			seed:      "Python-urllib/3.13",
			want:      "Python-urllib/3.13",
		},
		{
			name:      "missing UA is filled for opencode account",
			account:   &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey},
			targetURL: "https://opencode.ai/zen/go/v1/messages",
			want:      openCodeUpstreamUserAgent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			if tt.seed != "" {
				headers.Set("User-Agent", tt.seed)
			}
			applyOpenCodeUpstreamUserAgent(tt.account, tt.targetURL, headers)
			require.Equal(t, tt.want, headers.Get("User-Agent"))
		})
	}
}

func TestApplyOpenCodeUpstreamUserAgentDropsNonCanonicalKeyVariants(t *testing.T) {
	headers := http.Header{"user-agent": []string{"Python-urllib/3.13"}}
	applyOpenCodeUpstreamUserAgent(&Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey},
		"https://opencode.ai/zen/go/v1/chat/completions", headers)

	count := 0
	for key, values := range headers {
		if strings.EqualFold(key, "User-Agent") {
			count += len(values)
			require.Equal(t, []string{openCodeUpstreamUserAgent}, values)
		}
	}
	require.Equal(t, 1, count)
}

func TestApplyOpenCodeUpstreamUserAgentNilSafe(t *testing.T) {
	applyOpenCodeUpstreamUserAgent(&Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey},
		"https://opencode.ai/zen/go/v1/chat/completions", nil)
}

func TestOpenCodeUpstreamUserAgentCanonicalizedAfterPassthrough(t *testing.T) {
	// 回归：客户端透传的编程库 UA（Python-urllib）会被 opencode.ai 前置
	// Cloudflare 以 error code 1010 拦截并计入账号 403 strike，导致健康账号
	// 被自动禁用。出站 UA 必须收敛为规范 opencode 客户端身份。
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	account := &Account{
		ID:       1,
		Platform: PlatformOpenCodeGo,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://opencode.ai/zen/go/v1",
		},
	}
	c := newOpenCodeSessionTestContext(t, "")
	c.Request.Header.Set("User-Agent", "Python-urllib/3.13")

	req, err := svc.buildUpstreamRequest(
		context.Background(), c, account,
		[]byte(`{"model":"glm-5.3","input":"hello"}`), "token", false, "", false,
	)
	require.NoError(t, err)
	require.Equal(t, openCodeUpstreamUserAgent, req.Header.Get("User-Agent"))
}

func TestCommandCodeUpstreamUserAgentCanonicalizedAfterPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	account := &Account{
		ID:       2,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.commandcode.ai/provider/v1",
		},
	}
	c := newOpenCodeSessionTestContext(t, "")
	c.Request.Header.Set("User-Agent", "Python-urllib/3.13")

	req, err := svc.buildUpstreamRequest(
		context.Background(), c, account,
		[]byte(`{"model":"gpt-5","input":"hello"}`), "token", false, "", false,
	)
	require.NoError(t, err)
	require.Equal(t, CodexCanonicalUserAgent(), req.Header.Get("User-Agent"))
}

func TestOpenCodeUpstreamUserAgentYieldsToExplicitAccountOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	account := &Account{
		ID:       1,
		Platform: PlatformOpenCodeGo,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url":                   "https://opencode.ai/zen/go/v1",
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       map[string]any{"user-agent": "custom-relay-agent/2.0"},
		},
	}
	c := newOpenCodeSessionTestContext(t, "")
	c.Request.Header.Set("User-Agent", "Python-urllib/3.13")

	req, err := svc.buildUpstreamRequest(
		context.Background(), c, account,
		[]byte(`{"model":"glm-5.3","input":"hello"}`), "token", false, "", false,
	)
	require.NoError(t, err)
	require.Equal(t, "custom-relay-agent/2.0", req.Header.Get("User-Agent"))
}

func TestOpenCodeSessionForwardedByRawChatCompletionsAfterAccountOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &openCodeSessionHTTPUpstream{}
	svc := openCodeSessionTestService()
	svc.httpUpstream = upstream
	account := openCodeSessionTestAccount("https://opencode.ai/zen/v1")
	c := newOpenCodeSessionTestContext(t, "conversation-789")
	c.Request.Header.Set("User-Agent", "opencode/1.0.0")

	resp, err := svc.sendCCUpstreamRequest(
		context.Background(), c, account,
		"https://opencode.ai/zen/v1/chat/completions", []byte(`{"model":"gpt-5"}`),
		false, "token", "", "",
	)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.NotNil(t, upstream.request)
	requireOpenCodeClientSessionID(t, upstream.request.Header)
	require.Equal(t, openCodeUpstreamUserAgent, upstream.request.Header.Get("User-Agent"), "过期/伪造的客户端 UA 不得出站")
}

func TestOpenCodeUpstreamIdentityIsNotForwardedToOtherUpstreams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := openCodeSessionTestService()
	body := []byte(`{"model":"gpt-5","input":"hello"}`)

	for _, baseURL := range []string{
		"https://api.openai.com/v1",
		"https://opencode.ai.evil.example/v1",
		"https://api.opencode.ai/v1",
	} {
		t.Run(baseURL, func(t *testing.T) {
			account := &Account{
				ID:          1,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"base_url": baseURL},
			}
			c := newOpenCodeSessionTestContext(t, "private-conversation")
			req, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "token", false, "", false)
			require.NoError(t, err)
			require.Empty(t, req.Header.Get(openCodeSessionHeader))
			require.NotEqual(t, openCodeUpstreamUserAgent, req.Header.Get("User-Agent"))
		})
	}
}
