package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/gin-gonic/gin"
)

func (h *AuthHandler) verifyLegacyOAuthCompleteRegistrationCaptcha(
	c *gin.Context,
	turnstileToken string,
	tencentTicket string,
	tencentRandstr string,
) error {
	proof := captchaProof(turnstileToken, tencentTicket, tencentRandstr)
	return h.authService.VerifyCaptcha(c.Request.Context(), proof, ip.GetClientIP(c))
}
