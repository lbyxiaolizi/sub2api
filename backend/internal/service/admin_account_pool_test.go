package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type nilPoolLookupRepository struct {
	ProxyPoolRepository
}

func (nilPoolLookupRepository) GetPoolByID(context.Context, int64) (*ProxyPool, error) {
	return nil, nil
}

func TestEnrichPoolNameIgnoresNilRepositoryResult(t *testing.T) {
	poolID := int64(17)
	account := &Account{PoolID: &poolID}
	admin := &adminServiceImpl{poolRepo: nilPoolLookupRepository{}}

	require.NotPanics(t, func() {
		admin.enrichPoolName(context.Background(), account)
	})
	require.Empty(t, account.PoolName)
}
