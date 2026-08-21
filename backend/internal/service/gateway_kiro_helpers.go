package service

import (
	"time"

	"github.com/tidwall/gjson"
)

func kiroCreditsFromUsageGJSON(usage gjson.Result) float64 {
	if !usage.Exists() {
		return 0
	}
	for _, key := range []string{"_sub2api_kiro_credits", "kiro_credits", "kiroCredits", "credits", "creditsUsed", "creditUsage"} {
		if v := usage.Get(key); v.Exists() && v.Float() > 0 {
			return v.Float()
		}
	}
	return 0
}

func mergeKiroCreditsFromAnthropicPayload(dst *ClaudeUsage, payload string) {
	if dst == nil || payload == "" || !gjson.Valid(payload) {
		return
	}
	if credits := kiroCreditsFromUsageGJSON(gjson.Get(payload, "usage")); credits > 0 {
		dst.KiroCredits = credits
		return
	}
	if credits := kiroCreditsFromUsageGJSON(gjson.Get(payload, "message.usage")); credits > 0 {
		dst.KiroCredits = credits
	}
}

func (s *GatewayService) streamKeepaliveIntervalForAccount(account *Account) time.Duration {
	if account != nil && account.Platform == PlatformKiro {
		if s != nil && s.cfg != nil && s.cfg.Gateway.KiroStreamKeepaliveInterval > 0 {
			return time.Duration(s.cfg.Gateway.KiroStreamKeepaliveInterval) * time.Second
		}
		return defaultKiroStreamKeepalive
	}
	if s != nil && s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		return time.Duration(s.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	return 0
}
