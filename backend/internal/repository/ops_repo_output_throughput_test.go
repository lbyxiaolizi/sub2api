package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsOutputThroughputKeepsConsumptionSeparate(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}
	start := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	mock.ExpectQuery(`SUM\(output_tokens\)`).WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"success_count", "token_consumed", "output_tokens"}).AddRow(2, 300600, 600))
	requests, consumed, output, err := repo.queryUsageCounts(context.Background(), nil, start, end)
	require.NoError(t, err)
	require.Equal(t, int64(2), requests)
	require.Equal(t, int64(300600), consumed)
	require.Equal(t, int64(600), output)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsOutputThroughputTrendUsesOutputOnly(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}
	start := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	group := int64(1)
	filter := &service.OpsDashboardFilter{StartTime: start, EndTime: start.Add(2 * time.Minute), GroupID: &group}
	mock.ExpectQuery(`SUM\(output_tokens\)`).
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "request_count", "token_consumed", "switch_count", "output_tokens"}).
			AddRow(start, 2, 300600, 0, 600).
			AddRow(start.Add(time.Minute), 1, 900000, 0, 0))
	result, err := repo.GetThroughputTrend(context.Background(), filter, 60)
	require.NoError(t, err)
	require.Len(t, result.Points, 2)
	require.Equal(t, float64(10), result.Points[0].TPS)
	require.Equal(t, int64(300600), result.Points[0].TokenConsumed)
	require.Zero(t, result.Points[1].TPS)
	require.NoError(t, mock.ExpectationsWereMet())
}
