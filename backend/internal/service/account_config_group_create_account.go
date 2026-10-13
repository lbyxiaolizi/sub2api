package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

// AccountConfigGroupBaseURLOverrideKey is an account-local identity field. It is
// never accepted as group configuration, and is restored from the locked account
// on credential writes so a partial refresh cannot discard its endpoint.
const AccountConfigGroupBaseURLOverrideKey = "account_group_base_url_override"

type CreateAccountInConfigGroupInput struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

type AccountConfigGroupAccountCreator interface {
	CreateAccountInConfigGroup(context.Context, int64, *CreateAccountInConfigGroupInput) (*Account, error)
}

type AccountConfigGroupAccountRepository interface {
	// Creates the account, routing edge, exclusive membership, configuration and
	// scheduler outbox in one transaction, checking the group's revision again.
	CreateAccountInConfigGroup(context.Context, *AccountConfigGroup, *Account) error
}

func SupportsAccountConfigGroupKeyCreation(group *AccountConfigGroup) bool {
	return group != nil && (group.Type == AccountTypeAPIKey || group.Type == AccountTypeUpstream)
}

// ApplyAccountConfigGroupEndpointOverride replaces every protocol endpoint for a
// member with an explicit address. Protocol selection and model rules remain
// shared, but adaptive requests must not fall back to another account's host.
func ApplyAccountConfigGroupEndpointOverride(credentials map[string]any) {
	address, _ := credentials[AccountConfigGroupBaseURLOverrideKey].(string)
	if strings.TrimSpace(address) == "" {
		return
	}
	credentials["base_url"] = address
	if protocol, _ := credentials["api_protocol"].(string); strings.TrimSpace(protocol) == APIProtocolAnthropic {
		credentials["base_url"] = strings.TrimSuffix(address, "/v1")
	}
	credentials["api_base_urls"] = map[string]any{
		APIProtocolChatCompletions: address,
		APIProtocolResponses:       address,
		APIProtocolAnthropic:       strings.TrimSuffix(address, "/v1"),
	}
}

// AccountConfigGroupDefaultBaseURL reflects the same protocol/platform fallback
// used by routing, including adaptive accounts that only store api_base_urls.
func AccountConfigGroupDefaultBaseURL(group *AccountConfigGroup) string {
	if group == nil {
		return ""
	}
	account := &Account{Platform: group.Platform, Type: group.Type, Credentials: group.Config.Credentials}
	if account.IsAnthropicProtocol() {
		return account.GetAnthropicProtocolBaseURL()
	}
	if address := account.GetOpenAIBaseURL(); address != "" {
		return address
	}
	if address := account.GetCredential("base_url"); address != "" {
		return address
	}
	switch account.Platform {
	case PlatformAnthropic:
		return "https://api.anthropic.com"
	case PlatformGemini:
		return account.GetGeminiBaseURL("https://generativelanguage.googleapis.com")
	case PlatformGrok:
		return account.GetGrokBaseURL()
	}
	return ""
}

func normalizedAccountGroupEndpoint(address string) string {
	return strings.TrimRight(strings.TrimSpace(address), "/")
}

func (s *adminServiceImpl) CreateAccountInConfigGroup(ctx context.Context, id int64, input *CreateAccountInConfigGroupInput) (*Account, error) {
	if input == nil || strings.TrimSpace(input.APIKey) == "" {
		return nil, infraerrors.BadRequest("ACCOUNT_CONFIG_GROUP_API_KEY_REQUIRED", "API key is required")
	}
	if strings.ContainsAny(input.APIKey, "\r\n") {
		return nil, infraerrors.BadRequest("ACCOUNT_CONFIG_GROUP_INVALID_API_KEY", "API key must not contain line breaks")
	}
	repo, err := s.accountConfigGroupRepo()
	if err != nil {
		return nil, err
	}
	creator, ok := s.accountRepo.(AccountConfigGroupAccountRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("ACCOUNT_CONFIG_GROUP_CREATE_UNAVAILABLE", "account group account creation is not configured")
	}
	group, err := repo.GetAccountConfigGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	if !SupportsAccountConfigGroupKeyCreation(group) {
		return nil, infraerrors.BadRequest("ACCOUNT_CONFIG_GROUP_KEY_CREATION_UNSUPPORTED", "this account type requires its dedicated credential creation flow")
	}
	// Deep-copy the snapshot before validation/normalization; source identity and
	// runtime fields are never read or cloned into the new account.
	raw, err := json.Marshal(group)
	if err != nil {
		return nil, err
	}
	var snapshot AccountConfigGroup
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	group = &snapshot
	if err := s.validateAccountConfigGroup(ctx, group); err != nil {
		return nil, err
	}
	address := normalizedAccountGroupEndpoint(input.BaseURL)
	if address != "" {
		parsed, err := url.Parse(address)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
			return nil, infraerrors.BadRequest("ACCOUNT_CONFIG_GROUP_INVALID_BASE_URL", "API address must be an absolute HTTP(S) URL without credentials, query or fragment")
		}
	}
	credentials := AccountConfigGroupCredentialSettings(group.Config.Credentials)
	credentials["api_key"] = strings.TrimSpace(input.APIKey)
	defaultAddress := AccountConfigGroupDefaultBaseURL(group)
	if address != "" && address != normalizedAccountGroupEndpoint(defaultAddress) {
		credentials[AccountConfigGroupBaseURLOverrideKey] = address
		ApplyAccountConfigGroupEndpointOverride(credentials)
	}
	account, err := buildAccountForCreate(&CreateAccountInput{
		Name:     fmt.Sprintf("group-%d-%s", group.ID, uuid.NewString()[:8]),
		Platform: group.Platform, Type: group.Type, Credentials: credentials,
		Concurrency: group.Config.Concurrency,
	}, map[string]any{})
	if err != nil {
		return nil, err
	}
	if err := creator.CreateAccountInConfigGroup(ctx, group, account); err != nil {
		return nil, err
	}
	account.AccountConfigGroupID = &group.ID
	account.AccountConfigGroupName = group.Name
	return account, nil
}
