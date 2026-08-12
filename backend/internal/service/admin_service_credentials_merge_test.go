//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type updateAccountCredsRepoStub struct {
	mockAccountRepoForGemini
	account     *Account
	updateCalls int
}

type updateAccountPoolRepoStub struct {
	*fakePoolRepo
	countErr error
}

func (r *updateAccountPoolRepoStub) CountAccountsByProxyIDs(context.Context, []int64) (map[int64]int64, error) {
	if r.countErr != nil {
		return nil, r.countErr
	}
	return map[int64]int64{}, nil
}

func (r *updateAccountCredsRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	return r.account, nil
}

func (r *updateAccountCredsRepoStub) Update(ctx context.Context, account *Account) error {
	r.updateCalls++
	r.account = account
	return nil
}

func TestUpdateAccount_PreservesSensitiveCredsWhenIncomingOmits(t *testing.T) {
	accountID := int64(202)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-existing",
				"access_token":  "at-existing",
				"id_token":      "id-existing",
				"base_url":      "https://old.example.com",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	// 模拟前端编辑：仅修改 base_url，没有传 token（脱敏后前端 spread 拿不到敏感键）
	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"base_url": "https://new.example.com",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.updateCalls)

	// 敏感键应保留
	require.Equal(t, "rt-existing", repo.account.Credentials["refresh_token"])
	require.Equal(t, "at-existing", repo.account.Credentials["access_token"])
	require.Equal(t, "id-existing", repo.account.Credentials["id_token"])
	// 非敏感键被替换
	require.Equal(t, "https://new.example.com", repo.account.Credentials["base_url"])
}

func TestUpdateAccount_ExplicitNewTokenOverwrites(t *testing.T) {
	accountID := int64(203)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-old",
				"api_key":       "sk-old",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"refresh_token": "rt-new",
			// api_key 没传 → 应保留旧值
		},
	})
	require.NoError(t, err)
	require.NotNil(t, updated)

	require.Equal(t, "rt-new", repo.account.Credentials["refresh_token"])
	require.Equal(t, "sk-old", repo.account.Credentials["api_key"])
}

func TestUpdateAccount_EmptyCredentialsSkipsUpdate(t *testing.T) {
	accountID := int64(204)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-existing",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{}, // len == 0 → 闸门跳过
		Name:        "renamed",
	})
	require.NoError(t, err)

	require.Equal(t, "rt-existing", repo.account.Credentials["refresh_token"], "空 credentials 不应触碰已有 token")
	require.Equal(t, "renamed", repo.account.Name)
}

func TestUpdateAccount_PoolAssignmentErrorDoesNotPersistMixedBinding(t *testing.T) {
	accountID := int64(205)
	oldProxyID := int64(41)
	accountRepo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:          accountID,
			Name:        "before",
			Platform:    PlatformAnthropic,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			ProxyID:     &oldProxyID,
			Credentials: map[string]any{},
			Extra:       map[string]any{},
		},
	}
	poolRepo := &updateAccountPoolRepoStub{fakePoolRepo: newFakePoolRepo(), countErr: errors.New("count failed")}
	poolRepo.pools[7] = &ProxyPool{ID: 7, Name: "pool", Status: StatusActive}
	proxy := mkPoolProxy(71, 7)
	proxy.PoolHealth = PoolHealthHealthy
	poolRepo.proxies[proxy.ID] = proxy
	svc := &adminServiceImpl{accountRepo: accountRepo, poolRepo: poolRepo}
	poolID := int64(7)

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{PoolID: &poolID})

	require.ErrorContains(t, err, "assign proxy from pool 7")
	require.Nil(t, updated)
	require.Zero(t, accountRepo.updateCalls)
	require.Nil(t, accountRepo.account.PoolID)
	require.Equal(t, oldProxyID, *accountRepo.account.ProxyID)
}
