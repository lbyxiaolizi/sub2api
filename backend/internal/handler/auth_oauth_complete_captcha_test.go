package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type legacyOAuthCompleteCaptchaVerifier struct {
	calls int
	proof service.TencentCaptchaProof
}

func (v *legacyOAuthCompleteCaptchaVerifier) VerifyTicket(
	_ context.Context,
	_ service.TencentCaptchaCredentials,
	proof service.TencentCaptchaProof,
	_ string,
) (*service.TencentCaptchaVerifyResponse, error) {
	v.calls++
	v.proof = proof
	return &service.TencentCaptchaVerifyResponse{CaptchaCode: 1}, nil
}

func TestLegacyOAuthCompleteRegistrationVerifiesCaptchaBeforeCreatingAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		provider    string
		providerKey string
		complete    func(*AuthHandler, *gin.Context)
	}{
		{provider: "linuxdo", providerKey: "linuxdo", complete: func(h *AuthHandler, c *gin.Context) { h.CompleteLinuxDoOAuthRegistration(c) }},
		{provider: "oidc", providerKey: "https://issuer.example.com", complete: func(h *AuthHandler, c *gin.Context) { h.CompleteOIDCOAuthRegistration(c) }},
		{provider: "wechat", providerKey: wechatOAuthProviderKey, complete: func(h *AuthHandler, c *gin.Context) { h.CompleteWeChatOAuthRegistration(c) }},
		{provider: "dingtalk", providerKey: "dingtalk", complete: func(h *AuthHandler, c *gin.Context) { h.CompleteDingTalkOAuthRegistration(c) }},
	}

	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			handler, client := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
				settingValues: map[string]string{
					service.SettingKeyTencentCaptchaEnabled:        "true",
					service.SettingKeyTencentCaptchaAppID:          "123456789",
					service.SettingKeyTencentCaptchaAppSecretKey:   "app-secret",
					service.SettingKeyTencentCaptchaCloudSecretID:  "cloud-secret-id",
					service.SettingKeyTencentCaptchaCloudSecretKey: "cloud-secret-key",
				},
			})
			verifier := &legacyOAuthCompleteCaptchaVerifier{}
			handler.authService.SetTencentCaptchaService(service.NewTencentCaptchaService(handler.settingSvc, verifier))

			session, err := client.PendingAuthSession.Create().
				SetSessionToken(test.provider + "-captcha-session").
				SetIntent(oauthIntentLogin).
				SetProviderType(test.provider).
				SetProviderKey(test.providerKey).
				SetProviderSubject(test.provider + "-captcha-subject").
				SetResolvedEmail(test.provider + "-captcha@example.com").
				SetBrowserSessionKey(test.provider + "-captcha-browser").
				SetUpstreamIdentityClaims(map[string]any{"username": test.provider + "-user"}).
				SetExpiresAt(time.Now().UTC().Add(10 * time.Minute)).
				Save(context.Background())
			require.NoError(t, err)

			recorder := completeLegacyOAuthRegistrationRequest(t, handler, session, test.complete, `{"invitation_code":"INVITE"}`)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "TENCENT_CAPTCHA_VERIFICATION_FAILED")
			require.Zero(t, verifier.calls)
			userCount, err := client.User.Query().Count(context.Background())
			require.NoError(t, err)
			require.Zero(t, userCount)

			recorder = completeLegacyOAuthRegistrationRequest(t, handler, session, test.complete, `{"invitation_code":"INVITE","tencent_captcha_ticket":"ticket-value","tencent_captcha_randstr":"@rand-value"}`)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Equal(t, 1, verifier.calls)
			require.Equal(t, service.TencentCaptchaProof{Ticket: "ticket-value", Randstr: "@rand-value"}, verifier.proof)
			userCount, err = client.User.Query().Count(context.Background())
			require.NoError(t, err)
			require.Equal(t, 1, userCount)
		})
	}
}

func completeLegacyOAuthRegistrationRequest(
	t *testing.T,
	handler *AuthHandler,
	session *dbent.PendingAuthSession,
	complete func(*AuthHandler, *gin.Context),
	body string,
) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/"+session.ProviderType+"/complete-registration", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: oauthPendingSessionCookieName, Value: encodeCookieValue(session.SessionToken)})
	req.AddCookie(&http.Cookie{Name: oauthPendingBrowserCookieName, Value: encodeCookieValue(session.BrowserSessionKey)})
	c.Request = req
	complete(handler, c)
	return recorder
}
