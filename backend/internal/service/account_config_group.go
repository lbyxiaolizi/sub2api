package service

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"reflect"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrAccountConfigGroupStale           = infraerrors.Conflict("ACCOUNT_CONFIG_GROUP_STALE", "account group changed; reload and retry")
	ErrAccountConfigGroupNotFound        = infraerrors.NotFound("ACCOUNT_CONFIG_GROUP_NOT_FOUND", "account group not found")
	ErrAccountConfigGroupConflict        = infraerrors.Conflict("ACCOUNT_CONFIG_GROUP_CONFLICT", "account already belongs to another account group")
	ErrAccountConfigGroupManaged         = infraerrors.Conflict("ACCOUNT_CONFIG_GROUP_MANAGED", "account configuration is managed by its account group; edit the account group instead")
	ErrAccountConfigGroupInvalid         = infraerrors.BadRequest("ACCOUNT_CONFIG_GROUP_INVALID", "account group requires compatible accounts from the same parent group")
	ErrAccountConfigGroupMappingConflict = infraerrors.Conflict("ACCOUNT_CONFIG_GROUP_MAPPING_CONFLICT", "joining this account group would overwrite existing model mappings; review the group mappings and explicitly confirm the overwrite")
)

// AccountConfigGroup is distinct from AccountGroup, the existing account↔group edge.
type AccountConfigGroup struct {
	ID             int64                    `json:"id"`
	Name           string                   `json:"name"`
	GroupID        int64                    `json:"group_id"`
	Platform       string                   `json:"platform"`
	Type           string                   `json:"type"`
	AccountIDs     []int64                  `json:"account_ids"`
	Config         AccountConfigGroupConfig `json:"config"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
	DefaultBaseURL string                   `json:"default_base_url"`
	// One-shot request intent, never persisted or returned as group configuration.
	ConfirmModelMappingOverwrite bool `json:"-"`
}

// AccountConfigGroupConfig contains settings, never account authentication identity
// or provider-observed runtime state. Pointer values are nullable settings, not patches.
type AccountConfigGroupConfig struct {
	Notes                        *string        `json:"notes"`
	ProxyID                      *int64         `json:"proxy_id"`
	PoolID                       *int64         `json:"pool_id"`
	Concurrency                  int            `json:"concurrency"`
	Priority                     int            `json:"priority"`
	RateMultiplier               float64        `json:"rate_multiplier"`
	LoadFactor                   *int           `json:"load_factor"`
	Status                       string         `json:"status"`
	Schedulable                  bool           `json:"schedulable"`
	ExpiresAt                    *int64         `json:"expires_at"`
	AutoPauseOnExpired           bool           `json:"auto_pause_on_expired"`
	DisableAutoTempUnschedulable bool           `json:"disable_auto_temp_unschedulable"`
	Credentials                  map[string]any `json:"credentials"`
	Extra                        map[string]any `json:"extra"`
}

type CreateAccountConfigGroupInput struct {
	Name                         string  `json:"name"`
	GroupID                      int64   `json:"group_id"`
	AccountIDs                   []int64 `json:"account_ids"`
	SourceAccountID              int64   `json:"source_account_id"`
	ConfirmModelMappingOverwrite bool    `json:"confirm_model_mapping_overwrite"`
}
type UpdateAccountConfigGroupInput struct {
	Name                         *string         `json:"name"`
	AccountIDs                   *[]int64        `json:"account_ids"`
	Config                       json.RawMessage `json:"config"`
	ConfirmModelMappingOverwrite bool            `json:"confirm_model_mapping_overwrite"`
}

type AccountConfigGroupRepository interface {
	ListAccountConfigGroups(context.Context, int64) ([]AccountConfigGroup, error)
	GetAccountConfigGroup(context.Context, int64) (*AccountConfigGroup, error)
	// Save locks accounts and checks live parent bindings/platform/type/exclusive
	// membership again, then atomically writes membership, config and outbox events.
	SaveAccountConfigGroup(context.Context, *AccountConfigGroup) error
	DeleteAccountConfigGroup(context.Context, int64) error
	GetAccountConfigGroupByAccount(context.Context, int64) (*AccountConfigGroup, error)
}

// Separate from AdminService to avoid widening unrelated mocks and integrations.
type AccountConfigGroupManager interface {
	ListAccountConfigGroups(context.Context, int64) ([]AccountConfigGroup, error)
	GetAccountConfigGroup(context.Context, int64) (*AccountConfigGroup, error)
	CreateAccountConfigGroup(context.Context, *CreateAccountConfigGroupInput) (*AccountConfigGroup, error)
	UpdateAccountConfigGroup(context.Context, int64, *UpdateAccountConfigGroupInput) (*AccountConfigGroup, error)
	DeleteAccountConfigGroup(context.Context, int64) error
}

// Credential settings are an allowlist: unknown/new provider identity fields must
// never be copied across accounts simply because they are not classified secret.
var accountConfigGroupCredentialKeys = map[string]bool{
	"base_url": true, "api_base_urls": true, "api_protocol": true,
	"model_mapping": true, "compact_model_mapping": true, "header_overrides": true,
	"custom_error_codes": true, "custom_error_codes_enabled": true,
	"pool_mode": true, "pool_mode_retry_count": true, "pool_mode_retry_status_codes": true,
	"temp_unschedulable_enabled": true, "temp_unschedulable_rules": true,
	"openai_capabilities": true, "protocol_rules": true,
	// plan_type is also provider-observed, but the editor exposes an explicit
	// subscription-tier override. A present group value takes precedence over
	// refreshes; an absent value allows each account to auto-detect its own tier.
	"plan_type":               true,
	"header_override_enabled": true, "intercept_warmup_requests": true, "account_scheduling_threshold": true, "account_mode": true, "model_whitelist": true, "aws_region": true, "aws_force_global": true, "location": true, "vertex_location": true,
}

func AccountConfigGroupCredentialSettings(value map[string]any) map[string]any {
	out := make(map[string]any)
	for key, v := range value {
		if accountConfigGroupCredentialKeys[key] {
			out[key] = v
		}
	}
	return out
}

func AccountConfigGroupExtraSettings(value map[string]any) map[string]any {
	out := make(map[string]any)
	for key, v := range value {
		if isAccountConfigGroupExtraSetting(key) {
			out[key] = v
		}
	}
	return out
}

// Unknown extra keys are deliberately per-account until classified here. This
// avoids copying future provider observations or authentication metadata.
var accountConfigGroupExtraKeys = map[string]bool{
	"upstream_billing_probe_enabled":                true,
	"allow_overages":                                true,
	"anthropic_apikey_auth_scheme":                  true,
	"anthropic_passthrough":                         true,
	"auto_pause_5h_disabled":                        true,
	"auto_pause_5h_threshold":                       true,
	"auto_pause_7d_disabled":                        true,
	"auto_pause_7d_threshold":                       true,
	"auto_reset_credit_5h_threshold":                true,
	"auto_reset_credit_7d_threshold":                true,
	"auto_reset_credit_enabled":                     true,
	"base_rpm":                                      true,
	"cache_ttl_override_enabled":                    true,
	"cache_ttl_override_target":                     true,
	"codex_cli_only":                                true,
	"codex_cli_only_allow_app_server":               true,
	"codex_cli_only_allowed_clients":                true,
	"codex_fingerprint_mode":                        true,
	"codex_image_generation_bridge":                 true,
	"codex_image_generation_bridge_enabled":         true,
	"codex_image_generation_explicit_tool_policy":   true,
	"custom_base_url":                               true,
	"custom_base_url_enabled":                       true,
	"enable_tls_fingerprint":                        true,
	"grok_client_tool_cache_enabled":                true,
	"grok_media_eligible":                           true,
	"images_url_to_b64_json":                        true,
	"kiro_credit_unit_price_usd":                    true,
	"max_sessions":                                  true,
	"mixed_scheduling":                              true,
	"openai_apikey_responses_websockets_v2_enabled": true,
	"openai_apikey_responses_websockets_v2_mode":    true,
	"openai_compact_mode":                           true,
	"openai_long_context_billing_enabled":           true,
	"openai_oauth_passthrough":                      true,
	"openai_oauth_responses_websockets_v2_enabled":  true,
	"openai_oauth_responses_websockets_v2_mode":     true,
	"openai_passthrough":                            true,
	"openai_responses_flatten_namespaces":           true,
	"openai_responses_mode":                         true,
	"openai_ws_allow_store_recovery":                true,
	"openai_ws_enabled":                             true,
	"openai_ws_force_http":                          true,
	"quota_daily_limit":                             true,
	"quota_daily_reset_hour":                        true,
	"quota_daily_reset_mode":                        true,
	"quota_limit":                                   true,
	"quota_notify_daily_enabled":                    true,
	"quota_notify_daily_threshold":                  true,
	"quota_notify_daily_threshold_type":             true,
	"quota_notify_total_enabled":                    true,
	"quota_notify_total_threshold":                  true,
	"quota_notify_total_threshold_type":             true,
	"quota_notify_weekly_enabled":                   true,
	"quota_notify_weekly_threshold":                 true,
	"quota_notify_weekly_threshold_type":            true,
	"quota_reset_timezone":                          true,
	"quota_weekly_limit":                            true,
	"quota_weekly_reset_day":                        true,
	"quota_weekly_reset_hour":                       true,
	"quota_weekly_reset_mode":                       true,
	"responses_websockets_v2_enabled":               true,
	"rpm_sticky_buffer":                             true,
	"rpm_strategy":                                  true,
	"session_id_masking_enabled":                    true,
	"session_idle_timeout_minutes":                  true,
	"tls_fingerprint_profile_id":                    true,
	"upstream_request_id_header":                    true,
	"user_msg_queue_enabled":                        true,
	"user_msg_queue_mode":                           true,
	"web_search_emulation":                          true,
	"window_cost_limit":                             true,
	"window_cost_sticky_reserve":                    true,
}

func isAccountConfigGroupExtraSetting(key string) bool { return accountConfigGroupExtraKeys[key] }

// MergeAccountConfigGroupSettings replaces only group-managed map keys, keeping
// each account's authentication identity and mutable quota/provider observations.
func MergeAccountConfigGroupSettings(current, settings map[string]any, credentials bool) map[string]any {
	out := make(map[string]any, len(current)+len(settings))
	for key, value := range current {
		managed := isAccountConfigGroupExtraSetting(key)
		if credentials {
			managed = accountConfigGroupCredentialKeys[key]
			if key == "plan_type" {
				// The absence of a manual override means auto-detection, not
				// deletion of a tier returned by the provider during refresh.
				_, managed = settings[key]
			}
		}
		if !managed {
			out[key] = value
		}
	}
	for key, value := range settings {
		out[key] = value
	}
	if credentials {
		ApplyAccountConfigGroupEndpointOverride(out)
	}
	return out
}

// PrepareAccountConfigGroupMemberExtra applies the same setting-dependent local
// state transitions as the individual editor. The caller passes the locked member
// snapshot, so no generated seed, usage or provider observation is shared.
func PrepareAccountConfigGroupMemberExtra(account *Account, settings map[string]any) map[string]any {
	extra := MergeAccountConfigGroupSettings(account.Extra, settings, false)
	if account.IsOpenAIOAuthLike() {
		extra = prepareCodexFingerprintExtraForUpdate(account, extra)
	}
	if account.Platform == PlatformAntigravity {
		next := &Account{Platform: account.Platform, Extra: extra}
		wasEnabled, enabled := account.IsOveragesEnabled(), next.IsOveragesEnabled()
		if wasEnabled != enabled {
			delete(extra, "antigravity_credits_overages")
			if enabled {
				delete(extra, modelRateLimitsKey)
			} else if limits, ok := extra[modelRateLimitsKey].(map[string]any); ok {
				limits = maps.Clone(limits)
				delete(limits, creditsExhaustedKey)
				extra[modelRateLimitsKey] = limits
			}
		}
	}
	// Derived timestamps are calculated per member rather than stored as config.
	ComputeQuotaResetAt(extra)
	NormalizeFixedQuotaWindows(extra)
	return extra
}

func (s *adminServiceImpl) accountConfigGroupRepo() (AccountConfigGroupRepository, error) {
	repo, ok := s.accountRepo.(AccountConfigGroupRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("ACCOUNT_CONFIG_GROUP_UNAVAILABLE", "account group repository is not configured")
	}
	return repo, nil
}

func (s *adminServiceImpl) ListAccountConfigGroups(ctx context.Context, groupID int64) ([]AccountConfigGroup, error) {
	repo, err := s.accountConfigGroupRepo()
	if err != nil {
		return nil, err
	}
	groups, err := repo.ListAccountConfigGroups(ctx, groupID)
	for i := range groups {
		groups[i].DefaultBaseURL = AccountConfigGroupDefaultBaseURL(&groups[i])
	}
	return groups, err
}
func (s *adminServiceImpl) GetAccountConfigGroup(ctx context.Context, id int64) (*AccountConfigGroup, error) {
	repo, err := s.accountConfigGroupRepo()
	if err != nil {
		return nil, err
	}
	group, err := repo.GetAccountConfigGroup(ctx, id)
	if group != nil {
		group.DefaultBaseURL = AccountConfigGroupDefaultBaseURL(group)
	}
	return group, err
}
func (s *adminServiceImpl) DeleteAccountConfigGroup(ctx context.Context, id int64) error {
	repo, err := s.accountConfigGroupRepo()
	if err != nil {
		return err
	}
	return repo.DeleteAccountConfigGroup(ctx, id)
}

func (s *adminServiceImpl) CreateAccountConfigGroup(ctx context.Context, input *CreateAccountConfigGroupInput) (*AccountConfigGroup, error) {
	if input == nil || strings.TrimSpace(input.Name) == "" || len([]rune(strings.TrimSpace(input.Name))) > 100 || input.GroupID <= 0 || len(input.AccountIDs) == 0 {
		return nil, ErrAccountConfigGroupInvalid
	}
	repo, err := s.accountConfigGroupRepo()
	if err != nil {
		return nil, err
	}
	ids, err := accountConfigGroupIDs(input.AccountIDs)
	if err != nil {
		return nil, err
	}
	sourceID := input.SourceAccountID
	if sourceID == 0 {
		sourceID = ids[0]
	}
	found := false
	for _, id := range ids {
		if id == sourceID {
			found = true
		}
	}
	if !found {
		return nil, ErrAccountConfigGroupInvalid
	}
	source, err := s.accountRepo.GetByID(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	group := &AccountConfigGroup{Name: strings.TrimSpace(input.Name), GroupID: input.GroupID, Platform: source.Platform, Type: source.Type, AccountIDs: ids, Config: accountConfigFromAccount(source)}
	group.ConfirmModelMappingOverwrite = input.ConfirmModelMappingOverwrite
	if err := s.validateAccountConfigGroup(ctx, group); err != nil {
		return nil, err
	}
	if err := repo.SaveAccountConfigGroup(ctx, group); err != nil {
		return nil, err
	}
	return repo.GetAccountConfigGroup(ctx, group.ID)
}

func (s *adminServiceImpl) UpdateAccountConfigGroup(ctx context.Context, id int64, input *UpdateAccountConfigGroupInput) (*AccountConfigGroup, error) {
	if input == nil {
		return nil, ErrAccountConfigGroupInvalid
	}
	repo, err := s.accountConfigGroupRepo()
	if err != nil {
		return nil, err
	}
	group, err := repo.GetAccountConfigGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	group.ConfirmModelMappingOverwrite = input.ConfirmModelMappingOverwrite
	if input.Name != nil {
		group.Name = strings.TrimSpace(*input.Name)
	}
	if input.AccountIDs != nil {
		group.AccountIDs, err = accountConfigGroupIDs(*input.AccountIDs)
		if err != nil {
			return nil, err
		}
	}
	if len(input.Config) > 0 {
		if err := mergeAccountConfigGroupPatch(&group.Config, input.Config); err != nil {
			return nil, err
		}
	}
	if err := s.validateAccountConfigGroup(ctx, group); err != nil {
		return nil, err
	}
	if err := repo.SaveAccountConfigGroup(ctx, group); err != nil {
		return nil, err
	}
	return repo.GetAccountConfigGroup(ctx, group.ID)
}

func accountConfigGroupIDs(input []int64) ([]int64, error) {
	out := make([]int64, 0, len(input))
	seen := make(map[int64]bool)
	for _, id := range input {
		if id <= 0 || seen[id] {
			return nil, ErrAccountConfigGroupInvalid
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func accountConfigFromAccount(account *Account) AccountConfigGroupConfig {
	config := AccountConfigGroupConfig{Notes: account.Notes, ProxyID: account.ProxyID, PoolID: account.PoolID, Concurrency: account.Concurrency, Priority: account.Priority, RateMultiplier: account.BillingRateMultiplier(), LoadFactor: account.LoadFactor, Status: account.Status, Schedulable: account.Schedulable, AutoPauseOnExpired: account.AutoPauseOnExpired, DisableAutoTempUnschedulable: account.DisableAutoTempUnschedulable, Credentials: AccountConfigGroupCredentialSettings(account.Credentials), Extra: AccountConfigGroupExtraSettings(account.Extra)}
	if account.ExpiresAt != nil {
		unix := account.ExpiresAt.Unix()
		config.ExpiresAt = &unix
	}
	return config
}

func mergeAccountConfigGroupPatch(config *AccountConfigGroupConfig, patch json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(patch, &fields); err != nil || fields == nil {
		return ErrAccountConfigGroupInvalid
	}
	currentJSON, err := json.Marshal(config)
	if err != nil {
		return err
	}
	var current map[string]json.RawMessage
	if err := json.Unmarshal(currentJSON, &current); err != nil {
		return err
	}
	for key, value := range fields {
		if _, ok := current[key]; !ok {
			return infraerrors.BadRequest("ACCOUNT_CONFIG_GROUP_UNKNOWN_SETTING", "unknown account group setting: "+key)
		}
		if string(value) == "null" {
			switch key {
			case "notes", "proxy_id", "pool_id", "load_factor", "expires_at":
			default:
				return ErrAccountConfigGroupInvalid
			}
		}
		current[key] = value
	}
	merged, err := json.Marshal(current)
	if err != nil {
		return err
	}
	// Decode into a fresh value: encoding/json reuses non-nil maps otherwise,
	// turning replacement objects (including {}) into accidental merge patches.
	var next AccountConfigGroupConfig
	if err := json.Unmarshal(merged, &next); err != nil {
		return ErrAccountConfigGroupInvalid
	}
	*config = next
	return nil
}

func (s *adminServiceImpl) validateAccountConfigGroup(ctx context.Context, group *AccountConfigGroup) error {
	if group.Name == "" || len([]rune(group.Name)) > 100 {
		return ErrAccountConfigGroupInvalid
	}
	if err := s.ValidateAccountGroupBindings(ctx, []int64{group.GroupID}); err != nil {
		return err
	}
	if _, err := s.groupRepo.GetByID(ctx, group.GroupID); err != nil {
		return err
	}
	config := &group.Config
	config.Concurrency = normalizeAccountConcurrency(group.Platform, group.Type, config.Concurrency)
	if config.Concurrency < 0 || config.Priority < 0 || config.RateMultiplier < 0 || math.IsNaN(config.RateMultiplier) || math.IsInf(config.RateMultiplier, 0) || (config.LoadFactor != nil && (*config.LoadFactor < 0 || *config.LoadFactor > 10000)) {
		return ErrAccountConfigGroupInvalid
	}
	if config.Status != StatusActive && config.Status != "inactive" && config.Status != StatusError {
		return ErrAccountConfigGroupInvalid
	}
	if config.ProxyID != nil && *config.ProxyID <= 0 {
		config.ProxyID = nil
	}
	if config.PoolID != nil && *config.PoolID <= 0 {
		config.PoolID = nil
	}
	if config.PoolID != nil {
		config.ProxyID = nil
	}
	if config.LoadFactor != nil && *config.LoadFactor == 0 {
		config.LoadFactor = nil
	}
	if config.ExpiresAt != nil && *config.ExpiresAt <= 0 {
		config.ExpiresAt = nil
	}
	if config.ProxyID != nil {
		if _, err := s.proxyRepo.GetByID(ctx, *config.ProxyID); err != nil {
			return err
		}
	}
	if config.PoolID != nil {
		if s.poolRepo == nil {
			return ErrProxyPoolNotFound
		}
		if _, err := s.poolRepo.GetPoolByID(ctx, *config.PoolID); err != nil {
			return err
		}
	}
	for key := range config.Credentials {
		if !accountConfigGroupCredentialKeys[key] {
			return infraerrors.BadRequest("ACCOUNT_CONFIG_GROUP_IDENTITY_SETTING", "authentication identity must remain on the individual account: "+key)
		}
	}
	for key := range config.Extra {
		if !isAccountConfigGroupExtraSetting(key) {
			return infraerrors.BadRequest("ACCOUNT_CONFIG_GROUP_RUNTIME_SETTING", "runtime state must remain on the individual account: "+key)
		}
	}
	if err := NormalizeHeaderOverrideCredentials(config.Credentials); err != nil {
		return err
	}
	if err := NormalizeProtocolRulesCredentials(config.Credentials); err != nil {
		return err
	}
	if raw, exists := config.Credentials["plan_type"]; exists {
		if raw == nil {
			delete(config.Credentials, "plan_type")
		} else if planType, ok := raw.(string); !ok {
			return infraerrors.BadRequest("ACCOUNT_CONFIG_GROUP_INVALID_PLAN_TYPE", "plan_type must be a string or null")
		} else if planType = strings.TrimSpace(planType); planType == "" {
			delete(config.Credentials, "plan_type")
		} else {
			config.Credentials["plan_type"] = planType
		}
	}
	normalizedExtra, err := normalizeOpenAIAutoResetCreditExtra(group.Platform, group.Type, false, config.Extra)
	if err != nil {
		return err
	}
	normalizedExtra, err = normalizeGrokMediaEligibilityExtra(group.Platform, normalizedExtra)
	if err != nil {
		return err
	}
	config.Extra = normalizedExtra
	if group.Platform == PlatformKiro {
		if err := ValidateKiroCreditUnitPriceFromExtra(config.Extra); err != nil {
			return err
		}
	}
	if err := ValidateQuotaResetConfig(config.Extra); err != nil {
		return err
	}
	if err := ValidateUpstreamRequestIDHeaderExtra(config.Extra); err != nil {
		return err
	}
	if err := ValidateOpenAILongContextBillingExtra(group.Platform, config.Extra); err != nil {
		return err
	}
	if value, exists := config.Extra[UpstreamBillingProbeEnabledExtraKey]; exists {
		enabled, ok := value.(bool)
		if !ok {
			return infraerrors.BadRequest("INVALID_UPSTREAM_BILLING_PROBE_ENABLED", "upstream_billing_probe_enabled must be a boolean")
		}
		if enabled && !IsUpstreamBillingProbeIdentity(group.Platform, group.Type) {
			return ErrUpstreamBillingProbeAccountInvalid
		}
	}

	accounts, err := s.accountRepo.GetByIDs(ctx, group.AccountIDs)
	if err != nil {
		return err
	}
	if len(accounts) != len(group.AccountIDs) {
		return ErrAccountConfigGroupInvalid
	}
	for _, account := range accounts {
		found := false
		for _, id := range account.GroupIDs {
			if id == group.GroupID {
				found = true
			}
		}
		if !found || account.Platform != group.Platform || account.Type != group.Type || account.IsCredentialShadow() {
			return ErrAccountConfigGroupInvalid
		}
		// Independent billing-rate sync would immediately undo the group value.
		if enabled, _ := account.Extra[UpstreamBillingRateSyncEnabledExtraKey].(bool); enabled {
			return ErrUpstreamBillingRateSyncConflict
		}
	}
	return nil
}

// Guards run before any direct admin write. Authentication refreshes remain local
// and must preserve the shared settings when passing through UpdateAccount.
func (s *adminServiceImpl) guardAccountConfigGroupUpdate(ctx context.Context, account *Account, input *UpdateAccountInput) error {
	repo, ok := s.accountRepo.(AccountConfigGroupRepository)
	if !ok {
		return nil
	}
	group, err := repo.GetAccountConfigGroupByAccount(ctx, account.ID)
	if err != nil {
		return err
	}
	if group == nil {
		return nil
	}
	if input.GroupIDs != nil {
		found := false
		for _, id := range *input.GroupIDs {
			if id == group.GroupID {
				found = true
			}
		}
		if !found {
			return ErrAccountConfigGroupManaged
		}
	}
	if input.Notes != nil || (input.Type != "" && input.Type != account.Type) || input.ProxyID != nil || input.PoolID != nil || input.Concurrency != nil || input.Priority != nil || input.RateMultiplier != nil || input.LoadFactor != nil || input.Status != "" || input.ExpiresAt != nil || input.AutoPauseOnExpired != nil || input.DisableAutoTempUnschedulable != nil || input.ProbeEnabled != nil || input.RateSyncEnabled != nil {
		return ErrAccountConfigGroupManaged
	}
	if input.Extra != nil && !reflect.DeepEqual(AccountConfigGroupExtraSettings(input.Extra), AccountConfigGroupExtraSettings(account.Extra)) {
		return ErrAccountConfigGroupManaged
	}
	if len(input.Credentials) > 0 {
		requested := AccountConfigGroupCredentialSettings(input.Credentials)
		current := AccountConfigGroupCredentialSettings(account.Credentials)
		if account.Platform == PlatformOpenAI && account.Type == AccountTypeOAuth {
			if _, refreshingToken := input.Credentials["access_token"]; refreshingToken {
				// Admin OAuth refresh endpoints use UpdateAccount as well as the
				// background repository path. Their observed tier is not a manual
				// setting edit; an explicit group override is restored below.
				delete(requested, "plan_type")
				delete(current, "plan_type")
			}
		}
		if len(requested) > 0 && !reflect.DeepEqual(requested, current) {
			return ErrAccountConfigGroupManaged
		}
		// Credential-only refresh payloads omit settings; MergePreservingSensitiveCreds
		// normally uses replacement semantics, so restore shared keys explicitly.
		for key, value := range group.Config.Credentials {
			input.Credentials[key] = value
		}
		delete(input.Credentials, AccountConfigGroupBaseURLOverrideKey)
		if override, ok := account.Credentials[AccountConfigGroupBaseURLOverrideKey]; ok {
			input.Credentials[AccountConfigGroupBaseURLOverrideKey] = override
		}
		ApplyAccountConfigGroupEndpointOverride(input.Credentials)
	}
	return nil
}

func (s *adminServiceImpl) guardAccountConfigGroupIDs(ctx context.Context, ids []int64) error {
	repo, ok := s.accountRepo.(AccountConfigGroupRepository)
	if !ok {
		return nil
	}
	for _, id := range ids {
		group, err := repo.GetAccountConfigGroupByAccount(ctx, id)
		if err != nil {
			return err
		}
		if group != nil {
			return ErrAccountConfigGroupManaged
		}
	}
	return nil
}
