//go:build integration

package repository

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUsageBodySizeMigrationPreservesOnlyUnambiguousCompletedReads(t *testing.T) {
	tx, err := integrationDB.Begin()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`CREATE TEMP TABLE usage_logs (id bigint PRIMARY KEY);
	CREATE TEMP TABLE request_timing_details (usage_log_id bigint, detail jsonb);
	INSERT INTO usage_logs VALUES (1),(2),(3),(4),(5);
	INSERT INTO request_timing_details VALUES
	(1,'{"body_bytes":0,"body_complete":true}'),(2,'{"body_bytes":89,"body_complete":true}'),
	(3,'{"body_bytes":7,"body_complete":false}'),
	(4,'{"body_bytes":5,"body_complete":true}'),(4,'{"body_bytes":8,"body_complete":true}')`)
	require.NoError(t, err)
	migration, err := migrations.FS.ReadFile("399_usage_request_body_bytes.sql")
	require.NoError(t, err)
	_, err = tx.Exec(string(migration))
	require.NoError(t, err)
	var got string
	require.NoError(t, tx.QueryRow(`SELECT array_agg(request_body_bytes ORDER BY id)::text FROM usage_logs`).Scan(&got))
	require.Equal(t, "{0,89,NULL,NULL,NULL}", got)

	// Sites already upgraded upstream have no diagnostic source. Reapplying
	// the backfill must retain the measured values and tolerate that absence.
	_, err = tx.Exec("DROP TABLE pg_temp.request_timing_details")
	require.NoError(t, err)
	_, err = tx.Exec(string(migration))
	require.NoError(t, err)
	require.NoError(t, tx.QueryRow(`SELECT array_agg(request_body_bytes ORDER BY id)::text FROM usage_logs`).Scan(&got))
	require.Equal(t, "{0,89,NULL,NULL,NULL}", got)
}

func TestUsageBodySizeAndCacheSurviveConcurrentDirectSettlementReplay(t *testing.T) {
	ctx := context.Background()
	repo, command, usage, _ := durableBillingFixture(t)
	size := int64(37)
	usage.RequestBodyBytes = &size
	usage.CacheReadTokens = 3
	ids := []string{command.RequestID, command.RequestID, uuid.NewString()}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, len(ids))
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd, row := command, usage
			cmd.RequestID, row.RequestID = id, id
			<-start
			_, errs[i] = repo.ApplyUsage(ctx, &cmd, &row)
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	var balance, quota float64
	var entries, cached, bytes int
	require.NoError(t, repo.db.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", command.UserID).Scan(&balance))
	require.NoError(t, repo.db.QueryRowContext(ctx, "SELECT quota_used FROM api_keys WHERE id=$1", command.APIKeyID).Scan(&quota))
	require.NoError(t, repo.db.QueryRowContext(ctx, "SELECT count(*), sum(cache_read_tokens), sum(request_body_bytes) FROM usage_logs WHERE api_key_id=$1", command.APIKeyID).Scan(&entries, &cached, &bytes))
	require.Equal(t, 98.0, balance)
	require.Equal(t, 2.0, quota)
	require.Equal(t, 2, entries)
	require.Equal(t, 6, cached)
	require.Equal(t, 74, bytes)
}
