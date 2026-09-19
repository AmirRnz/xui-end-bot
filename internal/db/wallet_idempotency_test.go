package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestWalletOperationKeyAppliesOneDebitAndAllowsLaterIntent(t *testing.T) {
	ctx := setupTestDB(t)
	telegramID := time.Now().UnixNano()
	username := fmt.Sprintf("wallet_idempotency_%d", telegramID)
	var userID int64
	if err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, status, wallet_balance)
		VALUES ($1, $2, 'approved', 1000)
		RETURNING id
	`, telegramID, username).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	defer func() {
		_, _ = Pool.Exec(ctx, "DELETE FROM transactions WHERE user_id = $1", userID)
		_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE id = $1", userID)
	}()

	operationKey := fmt.Sprintf("wallet_test_intent:%d", telegramID)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			results <- DebitWalletBalanceWithKey(context.Background(), userID, 100, "same wallet intent", operationKey)
		}()
	}
	wg.Wait()
	close(results)

	applied := 0
	duplicates := 0
	for err := range results {
		switch {
		case err == nil:
			applied++
		case errors.Is(err, ErrWalletOperationAlreadyApplied):
			duplicates++
		default:
			t.Fatalf("unexpected concurrent wallet result: %v", err)
		}
	}
	if applied != 1 || duplicates != 1 {
		t.Fatalf("expected one applied and one duplicate, got applied=%d duplicates=%d", applied, duplicates)
	}

	var balance int64
	if err := Pool.QueryRow(ctx, "SELECT wallet_balance FROM bot_users WHERE id = $1", userID).Scan(&balance); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if balance != 900 {
		t.Fatalf("same intent must debit once, got balance %d", balance)
	}
	var sameIntentTransactions int
	if err := Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM transactions
		WHERE user_id = $1 AND operation_key = $2
	`, userID, operationKey).Scan(&sameIntentTransactions); err != nil {
		t.Fatalf("count same-intent transactions: %v", err)
	}
	if sameIntentTransactions != 1 {
		t.Fatalf("same intent must create exactly one financial transaction, got %d", sameIntentTransactions)
	}
	if err := DebitWalletBalanceWithKey(ctx, userID, 100, "replayed wallet intent", operationKey); !errors.Is(err, ErrWalletOperationAlreadyApplied) {
		t.Fatalf("replay should be rejected as duplicate, got %v", err)
	}

	newKey := operationKey + ":new"
	if err := DebitWalletBalanceWithKey(ctx, userID, 100, "new wallet intent", newKey); err != nil {
		t.Fatalf("new intent should be allowed: %v", err)
	}
	if err := Pool.QueryRow(ctx, "SELECT wallet_balance FROM bot_users WHERE id = $1", userID).Scan(&balance); err != nil {
		t.Fatalf("read balance after new intent: %v", err)
	}
	if balance != 800 {
		t.Fatalf("new intent should debit separately, got balance %d", balance)
	}
	var totalOperationTransactions int
	if err := Pool.QueryRow(ctx, `
		SELECT count(*)
		FROM transactions
		WHERE user_id = $1 AND operation_key IN ($2, $3)
	`, userID, operationKey, newKey).Scan(&totalOperationTransactions); err != nil {
		t.Fatalf("count operation transactions: %v", err)
	}
	if totalOperationTransactions != 2 {
		t.Fatalf("two distinct intents must create two transactions, got %d", totalOperationTransactions)
	}
}
