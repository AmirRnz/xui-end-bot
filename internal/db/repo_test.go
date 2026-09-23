package db

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"xui-end-bot/internal/config"
)

func setupTestDB(t *testing.T) context.Context {
	ctx := context.Background()

	testURL := os.Getenv("TEST_DATABASE_URL")
	if testURL == "" {
		t.Skip("Skipping test: TEST_DATABASE_URL is not set (isolated test database required to protect production)")
	}

	cfg := &config.DatabaseConfig{URL: testURL}
	if config.Global == nil {
		config.Global = &config.Config{Database: *cfg}
	} else {
		config.Global.Database = *cfg
	}

	t.Logf("Connecting to test database via TEST_DATABASE_URL")
	err := Connect(ctx, cfg)
	if err != nil {
		t.Skipf("Skipping test: database connection failed: %v", err)
	}

	// Migrate (ensures tables are present)
	err = Migrate(ctx)
	if err != nil {
		t.Skipf("Skipping test: migration failed: %v", err)
	}

	return ctx
}

func TestSettingsRepo(t *testing.T) {
	ctx := setupTestDB(t)

	// Clean up after test
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_settings WHERE key LIKE 'test_key_%'")
		}
	}()

	// 1. Get non-existent key
	val, err := GetSetting(ctx, "test_key_non_existent")
	if err != nil {
		t.Fatalf("expected no error for non-existent setting, got: %v", err)
	}
	if val != "" {
		t.Fatalf("expected empty string for non-existent setting, got: %q", val)
	}

	// 2. Set key
	err = SetSetting(ctx, "test_key_1", "value_1")
	if err != nil {
		t.Fatalf("failed to set setting: %v", err)
	}

	// 3. Get key
	val, err = GetSetting(ctx, "test_key_1")
	if err != nil {
		t.Fatalf("failed to get setting: %v", err)
	}
	if val != "value_1" {
		t.Fatalf("expected 'value_1', got %q", val)
	}

	// 4. Update key (ON CONFLICT DO UPDATE)
	err = SetSetting(ctx, "test_key_1", "value_1_updated")
	if err != nil {
		t.Fatalf("failed to update setting: %v", err)
	}

	val, err = GetSetting(ctx, "test_key_1")
	if err != nil {
		t.Fatalf("failed to get setting after update: %v", err)
	}
	if val != "value_1_updated" {
		t.Fatalf("expected 'value_1_updated', got %q", val)
	}
}

func TestSubscriptionRepo(t *testing.T) {
	ctx := setupTestDB(t)

	// Clean up after test
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM subscriptions WHERE client_email LIKE 'test_email_%'")
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id = 999999999")
		}
	}()

	// We need a user to reference in subscriptions
	// Create user
	var userID int64
	err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name, last_name)
		VALUES (999999999, 'test_user', 'Test', 'User')
		ON CONFLICT (telegram_id) DO UPDATE SET username = EXCLUDED.username
		RETURNING id
	`).Scan(&userID)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	planID := 42
	expireTime := int64(time.Now().Add(24 * time.Hour).Unix())

	sub := &Subscription{
		UserID:      userID,
		PlanID:      &planID,
		PlanType:    "paid",
		ClientEmail: "test_email_1@example.com",
		SubID:       "sub_id_12345",
		DisplayName: "Test Subscription 1",
		IPLimit:     3,
		ExpireTime:  &expireTime,
		IsActive:    true,
	}

	// 1. CreateSubscription
	err = CreateSubscription(ctx, sub)
	if err != nil {
		t.Fatalf("failed to create subscription: %v", err)
	}

	if sub.ID == 0 {
		t.Fatalf("expected assigned subscription ID, got 0")
	}
	if sub.CreatedAt.IsZero() || sub.UpdatedAt.IsZero() {
		t.Fatalf("expected assigned timestamps, got created_at=%v, updated_at=%v", sub.CreatedAt, sub.UpdatedAt)
	}

	// 2. GetSubscriptionByID
	gotSub, err := GetSubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("failed to get subscription by ID: %v", err)
	}
	if gotSub == nil {
		t.Fatalf("subscription not found by ID")
	}
	if gotSub.ClientEmail != sub.ClientEmail || *gotSub.PlanID != *sub.PlanID || gotSub.UserID != sub.UserID {
		t.Fatalf("retrieved subscription does not match, got: %+v, expected: %+v", gotSub, sub)
	}

	// 3. GetSubscriptionByEmail
	gotSub2, err := GetSubscriptionByEmail(ctx, "test_email_1@example.com")
	if err != nil {
		t.Fatalf("failed to get subscription by email: %v", err)
	}
	if gotSub2 == nil {
		t.Fatalf("subscription not found by email")
	}
	if gotSub2.ID != sub.ID {
		t.Fatalf("retrieved subscription ID mismatch, got %d, expected %d", gotSub2.ID, sub.ID)
	}

	// 4. GetSubscriptionByID non-existent
	nonExistentSub, err := GetSubscriptionByID(ctx, -999)
	if err != nil {
		t.Fatalf("expected nil and no error for non-existent ID, got: %v", err)
	}
	if nonExistentSub != nil {
		t.Fatalf("expected nil for non-existent ID, got: %+v", nonExistentSub)
	}

	// 5. GetSubscriptionByEmail non-existent
	nonExistentSubEmail, err := GetSubscriptionByEmail(ctx, "test_email_non_existent@example.com")
	if err != nil {
		t.Fatalf("expected nil and no error for non-existent email, got: %v", err)
	}
	if nonExistentSubEmail != nil {
		t.Fatalf("expected nil for non-existent email, got: %+v", nonExistentSubEmail)
	}

	// 6. GetActiveSubscriptionsByUserID
	activeSubs, err := GetActiveSubscriptionsByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("failed to get active subscriptions by user ID: %v", err)
	}
	if len(activeSubs) != 1 {
		t.Fatalf("expected 1 active subscription, got %d", len(activeSubs))
	}
	if activeSubs[0].ID != sub.ID {
		t.Fatalf("active subscription ID mismatch, got %d, expected %d", activeSubs[0].ID, sub.ID)
	}

	// 7. UpdateSubscription
	sub.IPLimit = 5
	sub.IsActive = false
	// Sleep a bit to ensure updated_at changes
	time.Sleep(10 * time.Millisecond)

	err = UpdateSubscription(ctx, sub)
	if err != nil {
		t.Fatalf("failed to update subscription: %v", err)
	}

	// Fetch again to verify changes
	gotSubUpdated, err := GetSubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("failed to get updated subscription: %v", err)
	}
	if gotSubUpdated.IPLimit != 5 || gotSubUpdated.IsActive {
		t.Fatalf("subscription fields not updated correctly: %+v", gotSubUpdated)
	}

	// Since we set IsActive to false, GetActiveSubscriptionsByUserID should return 0 results now
	activeSubs2, err := GetActiveSubscriptionsByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("failed to get active subscriptions by user ID after update: %v", err)
	}
	if len(activeSubs2) != 0 {
		t.Fatalf("expected 0 active subscriptions, got %d", len(activeSubs2))
	}

	// 8. DeleteSubscription
	err = DeleteSubscription(ctx, sub.ID)
	if err != nil {
		t.Fatalf("failed to delete subscription: %v", err)
	}

	// Verify the auditable row remains.
	gotSubDeleted, err := GetSubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("failed to check deleted subscription: %v", err)
	}
	if gotSubDeleted == nil || gotSubDeleted.Status != SubscriptionStatusDeleted || gotSubDeleted.IsActive {
		t.Fatalf("subscription was not marked deleted: %+v", gotSubDeleted)
	}
}

func TestDeletePlanWithUsage(t *testing.T) {
	ctx := setupTestDB(t)

	// Clean up after test
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM test_usage WHERE plan_id IN (SELECT id FROM test_plans WHERE name = 'Test Plan Delete')")
			_, _ = Pool.Exec(ctx, "DELETE FROM test_plans WHERE name = 'Test Plan Delete'")
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id = 999999998")
		}
	}()

	// 1. Create a user
	var userID int64
	err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name, last_name)
		VALUES (999999998, 'test_user_del_plan', 'Test', 'User Del Plan')
		ON CONFLICT (telegram_id) DO UPDATE SET username = EXCLUDED.username
		RETURNING id
	`).Scan(&userID)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	// 2. Create a test plan
	var planID int64
	err = Pool.QueryRow(ctx, `
		INSERT INTO test_plans (name, description, inbound_ids, expire_seconds, max_data_bytes, ip_limit, max_per_day, is_global, enabled)
		VALUES ('Test Plan Delete', 'Desc', '[]'::jsonb, 3600, 0, 1, 1, true, true)
		RETURNING id
	`).Scan(&planID)
	if err != nil {
		t.Fatalf("failed to create test plan: %v", err)
	}

	// 3. Create test usage record
	_, err = Pool.Exec(ctx, `
		INSERT INTO test_usage (user_id, plan_id, used_count, reset_date)
		VALUES ($1, $2, 1, CURRENT_DATE)
	`, userID, planID)
	if err != nil {
		t.Fatalf("failed to create test usage: %v", err)
	}

	// 4. Delete the plan
	err = DeletePlan(ctx, PlanTypeTest, planID)
	if err != nil {
		t.Fatalf("failed to delete test plan with usage: %v", err)
	}

	// 5. Verify it is deleted from test_plans
	var exists bool
	err = Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM test_plans WHERE id = $1)", planID).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to check if plan exists: %v", err)
	}
	if exists {
		t.Fatalf("expected test plan to be deleted, but it still exists")
	}

	// 6. Verify usage record is also deleted
	var usageExists bool
	err = Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM test_usage WHERE plan_id = $1)", planID).Scan(&usageExists)
	if err != nil {
		t.Fatalf("failed to check if usage exists: %v", err)
	}
	if usageExists {
		t.Fatalf("expected test usage record to be deleted, but it still exists")
	}
}

func TestPlanSyncSubs(t *testing.T) {
	ctx := setupTestDB(t)

	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM test_plans WHERE name LIKE 'Test Sync %'")
			_, _ = Pool.Exec(ctx, "DELETE FROM paid_plans WHERE name LIKE 'Paid Sync %'")
		}
	}()

	// 1. Test Plan with SyncSubs = false
	tp := &TestPlan{
		Name:          "Test Sync False",
		Description:   "Desc",
		InboundIDs:    []int{1},
		ExpireSeconds: 3600,
		MaxDataBytes:  0,
		Flow:          "",
		MaxPerDay:     1,
		IsGlobal:      true,
		Enabled:       true,
		SyncSubs:      false,
	}
	err := CreateTestPlan(ctx, tp)
	if err != nil {
		t.Fatalf("failed to create test plan: %v", err)
	}

	gotTp, err := GetTestPlanByID(ctx, tp.ID)
	if err != nil {
		t.Fatalf("failed to get test plan: %v", err)
	}
	if gotTp.SyncSubs {
		t.Fatalf("expected SyncSubs to be false, got true")
	}

	// Update to true
	gotTp.SyncSubs = true
	err = UpdateTestPlan(ctx, gotTp)
	if err != nil {
		t.Fatalf("failed to update test plan: %v", err)
	}

	gotTp2, err := GetTestPlanByID(ctx, tp.ID)
	if err != nil {
		t.Fatalf("failed to get test plan: %v", err)
	}
	if !gotTp2.SyncSubs {
		t.Fatalf("expected SyncSubs to be true after update, got false")
	}

	// 2. Paid Plan with SyncSubs = false
	pp := &PaidPlan{
		Name:            "Paid Sync False",
		InboundIDs:      []int{1},
		BasePrice:       100.0,
		BaseIPLimit:     1,
		MaxIPLimit:      2,
		PricePerExtraIP: 10.0,
		Flow:            "",
		DiscountTiers:   []DiscountTier{},
		IsGlobal:        true,
		Enabled:         true,
		SyncSubs:        false,
	}
	err = CreatePaidPlan(ctx, pp)
	if err != nil {
		t.Fatalf("failed to create paid plan: %v", err)
	}

	gotPp, err := GetPaidPlanByID(ctx, pp.ID)
	if err != nil {
		t.Fatalf("failed to get paid plan: %v", err)
	}
	if gotPp.SyncSubs {
		t.Fatalf("expected SyncSubs to be false for paid plan, got true")
	}

	// Update to true
	gotPp.SyncSubs = true
	err = UpdatePaidPlan(ctx, gotPp)
	if err != nil {
		t.Fatalf("failed to update paid plan: %v", err)
	}

	gotPp2, err := GetPaidPlanByID(ctx, pp.ID)
	if err != nil {
		t.Fatalf("failed to get paid plan: %v", err)
	}
	if !gotPp2.SyncSubs {
		t.Fatalf("expected SyncSubs to be true for paid plan after update, got false")
	}
}

func TestPurchaseRollbackAndClaim(t *testing.T) {
	ctx := setupTestDB(t)

	// Clean up after test
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM purchase_requests WHERE client_email LIKE 'test_claim_%'")
			_, _ = Pool.Exec(ctx, "DELETE FROM transactions WHERE reference_type = 'purchase_request'")
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id = 999999998")
		}
	}()

	// Create user
	var userID int64
	err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name, last_name)
		VALUES (999999998, 'test_user_claim', 'Test', 'User')
		ON CONFLICT (telegram_id) DO UPDATE SET username = EXCLUDED.username
		RETURNING id
	`).Scan(&userID)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	// Create purchase request (for claim)
	req := &PurchaseRequest{
		UserID:         userID,
		Type:           "claim",
		Price:          100.0,
		Months:         1,
		IPLimit:        1,
		DataGB:         10,
		CustomName:     "sub_claim_123",
		ClientEmail:    "test_claim_email@example.com",
		TelegramFileID: "claim",
		Status:         "pending",
	}

	err = CreatePurchaseRequest(ctx, req)
	if err != nil {
		t.Fatalf("failed to create purchase request: %v", err)
	}

	// 1. Verify HasPendingClaimRequest
	hasPending, err := HasPendingClaimRequest(ctx, "sub_claim_123")
	if err != nil {
		t.Fatalf("failed to check pending claim: %v", err)
	}
	if !hasPending {
		t.Fatalf("expected HasPendingClaimRequest to be true, got false")
	}

	// 2. Approve the purchase request
	userIDPtr, claimReqID := userID, req.ID
	claimWork := &ReconciliationRecord{OperationKey: "claim:" + strconv.FormatInt(req.ID, 10), Kind: "subscription_claim_adoption", UserID: &userIDPtr, PurchaseRequestID: &claimReqID, DesiredState: map[string]any{"purchase_request_id": req.ID, "user_id": userID}}
	approvedReq, err := ApprovePurchaseRequest(ctx, req.ID, 999999998, claimWork)
	if err != nil {
		t.Fatalf("failed to approve purchase request: %v", err)
	}
	if approvedReq == nil {
		t.Fatalf("approved request is nil")
	}
	if approvedReq.Status != "approved" || approvedReq.ProvisioningStatus != PurchaseProvisioningPending {
		t.Fatalf("expected payment approved and provisioning pending, got status=%q provisioning=%q", approvedReq.Status, approvedReq.ProvisioningStatus)
	}

	// Claim adoption is not a paid commerce action and creates no debit.
	var txCount int
	err = Pool.QueryRow(ctx, "SELECT count(*) FROM transactions WHERE reference_type = 'purchase_request' AND reference_id = $1", req.ID).Scan(&txCount)
	if err != nil {
		t.Fatalf("failed to count transactions: %v", err)
	}
	if txCount != 0 {
		t.Fatalf("expected no financial transaction for legacy claim adoption, got %d", txCount)
	}

	// Verify HasPendingClaimRequest is now false since status is no longer 'pending'
	hasPending, err = HasPendingClaimRequest(ctx, "sub_claim_123")
	if err != nil {
		t.Fatalf("failed to check pending claim: %v", err)
	}
	if hasPending {
		t.Fatalf("expected HasPendingClaimRequest to be false after approval, got true")
	}

	// 3. Record a provisioning failure/retry state. This must not undo the
	// approved status or delete its durable adoption work.
	err = RollbackPurchaseRequest(ctx, req.ID)
	if err != nil {
		t.Fatalf("failed to rollback purchase request: %v", err)
	}

	// Verify approval remains intact while claim adoption is retryable.
	rolledReq, err := GetPurchaseRequestByID(ctx, req.ID)
	if err != nil {
		t.Fatalf("failed to get purchase request: %v", err)
	}
	if rolledReq.Status != "approved" || rolledReq.ProvisioningStatus != PurchaseProvisioningRetryable || rolledReq.AdminID == nil {
		t.Fatalf("expected approved payment and retryable provisioning, got status %q provisioning %q AdminID %v", rolledReq.Status, rolledReq.ProvisioningStatus, rolledReq.AdminID)
	}

	// Verify the original financial transaction remains intact.
	err = Pool.QueryRow(ctx, "SELECT count(*) FROM transactions WHERE reference_type = 'purchase_request' AND reference_id = $1", req.ID).Scan(&txCount)
	if err != nil {
		t.Fatalf("failed to count transactions after rollback: %v", err)
	}
	if txCount != 1 {
		t.Fatalf("expected 1 transaction after provisioning failure, got %d", txCount)
	}

	// The request remains approved, so it is no longer pending.
	hasPending, err = HasPendingClaimRequest(ctx, "sub_claim_123")
	if err != nil {
		t.Fatalf("failed to check pending claim: %v", err)
	}
	if hasPending {
		t.Fatalf("expected HasPendingClaimRequest to remain false after provisioning failure")
	}
}

func TestApproveTopupIsDurableAndReplaySafe(t *testing.T) {
	ctx := setupTestDB(t)
	const telegramID int64 = 999999996
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM transactions WHERE reference_type = 'topup_request' AND reference_id IN (SELECT id FROM topup_requests WHERE user_id IN (SELECT id FROM bot_users WHERE telegram_id = $1))", telegramID)
			_, _ = Pool.Exec(ctx, "DELETE FROM topup_requests WHERE user_id IN (SELECT id FROM bot_users WHERE telegram_id = $1)", telegramID)
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id = $1", telegramID)
		}
	}()

	var userID int64
	if err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name, wallet_balance)
		VALUES ($1, 'topup_operation_test', 'Topup', 0)
		RETURNING id
	`, telegramID).Scan(&userID); err != nil {
		t.Fatalf("failed to create top-up test user: %v", err)
	}

	req := &TopupRequest{UserID: userID, TelegramFileID: "topup-operation-test", Status: "pending"}
	if err := CreateTopupRequest(ctx, req); err != nil {
		t.Fatalf("failed to create top-up request: %v", err)
	}
	approved, err := ApproveTopupRequest(ctx, req.ID, 999999996, 250)
	if err != nil || approved == nil {
		t.Fatalf("failed to approve top-up request: approved=%#v err=%v", approved, err)
	}

	var operationKey string
	if err := Pool.QueryRow(ctx, "SELECT operation_key FROM transactions WHERE reference_type = 'topup_request' AND reference_id = $1", req.ID).Scan(&operationKey); err != nil {
		t.Fatalf("failed to read top-up approval operation key: %v", err)
	}
	if expected := "topup_approval:" + strconv.FormatInt(req.ID, 10); operationKey != expected {
		t.Fatalf("expected top-up approval operation key %q, got %q", expected, operationKey)
	}

	// The pending guard rejects a replay before any balance or transaction
	// mutation. The operation key remains the durable audit identifier.
	if _, err := ApproveTopupRequest(ctx, req.ID, 999999996, 250); err == nil {
		t.Fatal("expected replayed top-up approval to be rejected")
	}
	var balance int64
	if err := Pool.QueryRow(ctx, "SELECT wallet_balance FROM bot_users WHERE id = $1", userID).Scan(&balance); err != nil {
		t.Fatalf("failed to read wallet balance: %v", err)
	}
	if balance != 250 {
		t.Fatalf("expected exactly one top-up credit, got balance %d", balance)
	}
	var transactionCount int
	if err := Pool.QueryRow(ctx, "SELECT count(*) FROM transactions WHERE operation_key = $1", operationKey).Scan(&transactionCount); err != nil {
		t.Fatalf("failed to count top-up approval transactions: %v", err)
	}
	if transactionCount != 1 {
		t.Fatalf("expected exactly one top-up approval transaction, got %d", transactionCount)
	}
}

func TestPurchaseRequestIntentKeyIsPersistedAndUsedForApproval(t *testing.T) {
	ctx := setupTestDB(t)
	const telegramID int64 = 999999995
	const operationKey = "direct_purchase_intent:test-receipt-1"
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM transactions WHERE operation_key = $1", operationKey)
			_, _ = Pool.Exec(ctx, "DELETE FROM purchase_requests WHERE operation_key = $1", operationKey)
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id = $1", telegramID)
		}
	}()

	var userID int64
	if err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name, wallet_balance)
		VALUES ($1, 'purchase_intent_test', 'Purchase', 0)
		RETURNING id
	`, telegramID).Scan(&userID); err != nil {
		t.Fatalf("failed to create purchase intent test user: %v", err)
	}

	priceToman := int64(321)
	req := &PurchaseRequest{
		UserID:         userID,
		Type:           "buy",
		PriceToman:     &priceToman,
		Price:          321,
		Months:         1,
		IPLimit:        1,
		DataGB:         10,
		ClientEmail:    "purchase-intent@example.com",
		TelegramFileID: "purchase-intent-test",
		Status:         "pending",
		OperationKey:   operationKey,
	}
	if err := CreatePurchaseRequest(ctx, req); err != nil {
		t.Fatalf("failed to create purchase request: %v", err)
	}
	loaded, err := GetPurchaseRequestByID(ctx, req.ID)
	if err != nil || loaded == nil || loaded.OperationKey != operationKey {
		t.Fatalf("purchase intent key was not persisted: loaded=%#v err=%v", loaded, err)
	}

	duplicate := *req
	duplicate.ID = 0
	if err := CreatePurchaseRequest(ctx, &duplicate); err == nil {
		t.Fatal("expected the same confirmation intent to be rejected by the unique key")
	}

	userPtr, purchaseID := userID, req.ID
	workItem := &ReconciliationRecord{OperationKey: "direct_payment:" + strconv.FormatInt(req.ID, 10) + ":provisioning", Kind: "direct_payment_provisioning_retry", UserID: &userPtr, PurchaseRequestID: &purchaseID, DesiredState: map[string]any{"purchase_request_id": req.ID, "user_id": userID}}
	approved, err := ApprovePurchaseRequest(ctx, req.ID, 999999995, workItem)
	if err != nil || approved == nil {
		t.Fatalf("failed to approve purchase request: approved=%#v err=%v", approved, err)
	}
	var transactionKey string
	if err := Pool.QueryRow(ctx, "SELECT operation_key FROM transactions WHERE reference_type = 'purchase_request' AND reference_id = $1", req.ID).Scan(&transactionKey); err != nil {
		t.Fatalf("failed to read purchase transaction key: %v", err)
	}
	if transactionKey != operationKey {
		t.Fatalf("expected approval transaction key %q, got %q", operationKey, transactionKey)
	}
}
