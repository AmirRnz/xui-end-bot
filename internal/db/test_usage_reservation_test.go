package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReserveTestUsageAllowsOneConcurrentOneTimeClaim(t *testing.T) {
	ctx := setupTestDB(t)
	telegramID := time.Now().UnixNano()
	var userID, planID int64
	if err := Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id) VALUES ($1) RETURNING id`, telegramID).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	defer func() { _, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, userID) }()

	planName := fmt.Sprintf("reserve_end_%d", telegramID)
	if err := Pool.QueryRow(ctx, `INSERT INTO test_plans (name) VALUES ($1) RETURNING id`, planName).Scan(&planID); err != nil {
		t.Fatalf("create test plan: %v", err)
	}
	defer func() { _, _ = Pool.Exec(ctx, `DELETE FROM test_plans WHERE id = $1`, planID) }()

	const attempts = 16
	var succeeded atomic.Int32
	var limited atomic.Int32
	errCh := make(chan error, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := ReserveTestUsage(ctx, userID, planID, 0)
			switch {
			case err == nil:
				succeeded.Add(1)
			case errors.Is(err, ErrTestUsageLimitReached):
				limited.Add(1)
			default:
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("reserve test usage: %v", err)
	}
	if got := succeeded.Load(); got != 1 {
		t.Fatalf("successful concurrent claims = %d, want 1", got)
	}
	if got := limited.Load(); got != attempts-1 {
		t.Fatalf("limited concurrent claims = %d, want %d", got, attempts-1)
	}

	if err := ReleaseTestUsage(context.Background(), userID, planID); err != nil {
		t.Fatalf("release confirmed no-write reservation: %v", err)
	}
	if err := ReserveTestUsage(ctx, userID, planID, 0); err != nil {
		t.Fatalf("reserve after confirmed release: %v", err)
	}
	var count int
	if err := Pool.QueryRow(ctx, `SELECT used_count FROM test_usage WHERE user_id = $1 AND plan_id = $2 AND reset_date = '2000-01-01'::date`, userID, planID).Scan(&count); err != nil {
		t.Fatalf("read reserved usage: %v", err)
	}
	if count != 1 {
		t.Fatalf("reserved usage count = %d, want 1", count)
	}
}
