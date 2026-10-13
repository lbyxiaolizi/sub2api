//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func accountGroupEndpointTestCredentials(protocol string) map[string]any {
	return map[string]any{
		"api_protocol": protocol,
		"base_url":     "https://group.example/v1",
		"api_base_urls": map[string]any{
			APIProtocolChatCompletions: "https://group-chat.example/v1",
			APIProtocolAnthropic:       "https://group-anthropic.example",
			APIProtocolResponses:       "https://group-responses.example/v1",
		},
		"model_mapping": map[string]any{"alias": "model"},
	}
}

func accountGroupEndpointTestAccount(platform, protocol, address string) *Account {
	return &Account{
		ID: 997, Name: "independent-endpoint", Platform: platform, Type: AccountTypeAPIKey,
		Status: StatusActive, Concurrency: 1,
		Credentials: MergeAccountConfigGroupSettings(map[string]any{
			"api_key":                            "member-only-key",
			AccountConfigGroupBaseURLOverrideKey: address,
		}, accountGroupEndpointTestCredentials(protocol), true),
	}
}

func TestAccountConfigGroupEndpointActualRequestBuilders(t *testing.T) {
	platforms := []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo}
	for _, platform := range platforms {
		for _, address := range []string{"https://member.example/prefix", "https://member.example/prefix/v1"} {
			t.Run(platform+"/"+address, func(t *testing.T) {
				account := accountGroupEndpointTestAccount(platform, APIProtocolAdaptive, address)
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				wantVersionedBase := "https://member.example/prefix/v1"
				chatURL, err := svc.openAIChatCompletionsTargetURL(account)
				require.NoError(t, err)
				require.Equal(t, wantVersionedBase+"/chat/completions", chatURL)
				messagesURL, err := svc.nativeAnthropicTargetURL(account)
				require.NoError(t, err)
				require.Equal(t, wantVersionedBase+"/messages", messagesURL)
				models, err := buildOpenAIAPIKeyModelsRequest(context.Background(), account, svc.validateUpstreamBaseURL)
				require.NoError(t, err)
				require.Equal(t, wantVersionedBase+"/models", models.URL.String())
				require.Equal(t, "Bearer member-only-key", models.Header.Get("Authorization"))

				if account.SupportsNativeCNResponses() {
					wantResponsesURL := wantVersionedBase + "/responses"
					if platform == PlatformDeepseek && !strings.HasSuffix(address, "/v1") {
						wantResponsesURL = address + "/responses"
					}
					body := []byte(`{"model":"gpt-5","input":"hi"}`)
					c := adaptiveProtocolTestContext("/v1/responses", body)
					req, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "member-only-key", false, "", false)
					require.NoError(t, err)
					require.Equal(t, wantResponsesURL, req.URL.String())
					passthrough, err := svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "member-only-key")
					require.NoError(t, err)
					require.Equal(t, wantResponsesURL, passthrough.URL.String())
					wsURL, err := svc.buildOpenAIResponsesWSURL(account)
					require.NoError(t, err)
					require.Equal(t, strings.Replace(wantResponsesURL, "https:", "wss:", 1), wsURL)
				}
			})
		}
	}
}

func TestAccountConfigGroupEndpointPinnedAnthropicModelSyncDoesNotUseProviderDefault(t *testing.T) {
	for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo} {
		t.Run(platform, func(t *testing.T) {
			account := accountGroupEndpointTestAccount(platform, APIProtocolAnthropic, "https://member.example/relay/v1")
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			messagesURL, err := svc.nativeAnthropicTargetURL(account)
			require.NoError(t, err)
			require.Equal(t, "https://member.example/relay/v1/messages", messagesURL)
			req, err := buildOpenAIAPIKeyModelsRequest(context.Background(), account, svc.validateUpstreamBaseURL)
			require.NoError(t, err)
			require.Equal(t, "https://member.example/relay/v1/models", req.URL.String())
			require.Equal(t, "https://member.example/relay/v1/embeddings", buildOpenAIEmbeddingsURL(account.GetOpenAIFormatBaseURL()))
		})
	}
}

func TestAccountConfigGroupEndpointRemainsIndependentWhileRoutingRulesSync(t *testing.T) {
	account := accountGroupEndpointTestAccount(PlatformOpenCodeGo, APIProtocolAdaptive, "https://member.example/v1")
	groupCredentials := accountGroupEndpointTestCredentials(APIProtocolAdaptive)
	groupCredentials["base_url"] = "https://replacement-group.example/v1"
	groupCredentials["protocol_rules"] = []any{
		map[string]any{"pattern": "route-responses", "protocol": APIProtocolResponses},
		map[string]any{"pattern": "route-messages", "protocol": APIProtocolAnthropic},
		map[string]any{"pattern": "route-chat", "protocol": APIProtocolChatCompletions},
	}
	groupCredentials["model_mapping"] = map[string]any{"new-alias": "route-responses"}
	account.Credentials = MergeAccountConfigGroupSettings(account.Credentials, groupCredentials, true)
	require.Equal(t, "https://member.example/v1", account.GetOpenAIBaseURL())
	require.Equal(t, "member-only-key", account.GetCredential("api_key"))
	require.Equal(t, groupCredentials["protocol_rules"], account.Credentials["protocol_rules"])
	require.Equal(t, groupCredentials["model_mapping"], account.Credentials["model_mapping"])

	for _, tc := range []struct {
		model, protocol, path string
		response              func() *http.Response
	}{
		{"route-chat", APIProtocolChatCompletions, "/v1/chat/completions", adaptiveCNChatTestResponse},
		{"route-messages", APIProtocolAnthropic, "/v1/messages", adaptiveCNAnthropicTestResponse},
		{"new-alias", APIProtocolResponses, "/v1/responses", adaptiveCNResponsesTestResponse},
	} {
		t.Run(tc.model, func(t *testing.T) {
			model := tc.model
			if model == "new-alias" {
				model = "route-responses"
			}
			require.Equal(t, tc.protocol, account.resolveModelRoutedProtocol(model))
			svc, upstream := adaptiveCNAccountTestService(account, tc.response())
			c, _ := newTestContext()
			require.NoError(t, svc.TestAccountConnection(c, account.ID, tc.model, "hi", AccountTestModeDefault))
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "https://member.example"+tc.path, upstream.requests[0].URL.String())
			require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
		})
	}
}

func TestAccountConfigGroupEndpointImplicitMembersKeepFollowingProtocolDefaults(t *testing.T) {
	account := &Account{Platform: PlatformOpenCodeGo, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "own-key"}}
	account.Credentials = MergeAccountConfigGroupSettings(account.Credentials, accountGroupEndpointTestCredentials(APIProtocolAdaptive), true)
	require.Equal(t, "https://group-chat.example/v1", account.GetOpenAIBaseURL())
	require.Equal(t, "https://group-anthropic.example", account.GetAnthropicProtocolBaseURL())
	newConfig := accountGroupEndpointTestCredentials(APIProtocolAdaptive)
	newConfig["api_base_urls"] = map[string]any{APIProtocolChatCompletions: "https://new-group.example/v1"}
	account.Credentials = MergeAccountConfigGroupSettings(account.Credentials, newConfig, true)
	require.Equal(t, "https://new-group.example/v1", account.GetOpenAIBaseURL())
	require.Equal(t, "own-key", account.GetCredential("api_key"))
	require.NotContains(t, account.Credentials, AccountConfigGroupBaseURLOverrideKey)
}

func TestAccountConfigGroupEndpointDetachedPinnedAnthropicRetainsMaterializedDestinations(t *testing.T) {
	for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo} {
		t.Run(platform, func(t *testing.T) {
			account := accountGroupEndpointTestAccount(platform, APIProtocolAnthropic, "https://member.example/relay/v1")
			// Membership removal/dissolution deletes only the ownership marker;
			// effective per-protocol addresses remain as independent configuration.
			delete(account.Credentials, AccountConfigGroupBaseURLOverrideKey)
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
			messagesURL, err := svc.nativeAnthropicTargetURL(account)
			require.NoError(t, err)
			require.Equal(t, "https://member.example/relay/v1/messages", messagesURL)
			req, err := buildOpenAIAPIKeyModelsRequest(context.Background(), account, svc.validateUpstreamBaseURL)
			require.NoError(t, err)
			require.Equal(t, "https://member.example/relay/v1/models", req.URL.String())
			require.Equal(t, "https://member.example/relay/v1/embeddings", buildOpenAIEmbeddingsURL(account.GetOpenAIFormatBaseURL()))

			// The normal pinned-protocol editor clears api_base_urls when saving
			// its new base_url. Detached accounts must not retain a hidden old host.
			delete(account.Credentials, "api_base_urls")
			account.Credentials["base_url"] = "https://edited.example/anthropic"
			messagesURL, err = svc.nativeAnthropicTargetURL(account)
			require.NoError(t, err)
			require.Equal(t, "https://edited.example/anthropic/v1/messages", messagesURL)
			require.NotContains(t, account.GetOpenAIFormatBaseURL(), "member.example")
		})
	}
}
