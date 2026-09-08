//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type disableTempUnschedRepoStub struct {
	mockAccountRepoForGemini
	account     *Account
	updateCalls int
	clearCalls  int
}

func (r *disableTempUnschedRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	return r.account, nil
}

func (r *disableTempUnschedRepoStub) Update(ctx context.Context, account *Account) error {
	r.updateCalls++
	r.account = account
	return nil
}

func (r *disableTempUnschedRepoStub) ClearTempUnschedulable(ctx context.Context, id int64) error {
	r.clearCalls++
	if r.account != nil && r.account.ID == id {
		r.account.TempUnschedulableUntil = nil
		r.account.TempUnschedulableReason = ""
	}
	return nil
}

func TestUpdateAccount_EnableDisableAutoTempUnschedulableClearsActiveMark(t *testing.T) {
	accountID := int64(301)
	until := time.Now().Add(time.Hour)
	repo := &disableTempUnschedRepoStub{
		account: &Account{
			ID:                      accountID,
			Platform:                PlatformOpenAI,
			Type:                    AccountTypeAPIKey,
			Status:                  StatusActive,
			TempUnschedulableUntil:  &until,
			TempUnschedulableReason: "429",
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		DisableAutoTempUnschedulable: &[]bool{true}[0],
	})
	require.NoError(t, err)
	require.True(t, updated.DisableAutoTempUnschedulable)
	require.Equal(t, 1, repo.clearCalls)
	require.Nil(t, updated.TempUnschedulableUntil)
	require.Empty(t, updated.TempUnschedulableReason)
}

func TestUpdateAccount_DisableFlagOffDoesNotClearMark(t *testing.T) {
	accountID := int64(302)
	until := time.Now().Add(time.Hour)
	repo := &disableTempUnschedRepoStub{
		account: &Account{
			ID:                      accountID,
			Platform:                PlatformOpenAI,
			Type:                    AccountTypeAPIKey,
			Status:                  StatusActive,
			TempUnschedulableUntil:  &until,
			TempUnschedulableReason: "429",
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Name: "renamed",
	})
	require.NoError(t, err)
	require.False(t, updated.DisableAutoTempUnschedulable)
	require.Equal(t, 0, repo.clearCalls)
	require.NotNil(t, updated.TempUnschedulableUntil)
}
