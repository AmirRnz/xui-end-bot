package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/db"
	"xui-end-bot/internal/xui"
)

func TestWalletRemoteOutcomeSafety(t *testing.T) {
	unknown := &xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("verification unavailable")}
	definitive := &xui.WriteError{Outcome: xui.WriteDefinitiveFailure, Err: errors.New("rejected")}
	tests := []struct {
		name       string
		err        error
		wantRefund bool
		wantRecon  bool
	}{
		{name: "success", err: nil},
		{name: "unknown", err: unknown, wantRecon: true},
		{name: "definitive failure", err: definitive, wantRefund: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := walletRemoteRefundAllowed(tt.err); got != tt.wantRefund {
				t.Fatalf("refund allowed=%t, want %t", got, tt.wantRefund)
			}
			if got := walletRemoteReconciliationRequired(tt.err); got != tt.wantRecon {
				t.Fatalf("reconciliation required=%t, want %t", got, tt.wantRecon)
			}
		})
	}
}

func TestWalletLocalWriteFailureRequiresReconciliationWithoutRefund(t *testing.T) {
	if !walletLocalWriteReconciliationRequired(errors.New("database unavailable")) {
		t.Fatal("local write failure must require reconciliation")
	}
}

func TestParseSubscriptionPairPayloadAcceptsTokenizedCallback(t *testing.T) {
	for _, payload := range []string{"sub_limit:42", "sub_limit:42:0123456789abcdef"} {
		subID, err := parseSubscriptionPairPayload(payload)
		if err != nil {
			t.Fatalf("payload %q should parse: %v", payload, err)
		}
		if subID != 42 {
			t.Fatalf("payload %q parsed subscription %d, want 42", payload, subID)
		}
	}
	if _, err := parseSubscriptionPairPayload("sub_limit:42:token:extra"); err == nil {
		t.Fatal("payload with an unexpected number of fields should be rejected")
	}
}

func TestWalletExtensionAndIPOutcomeSafety(t *testing.T) {
	for _, operation := range []string{"extension", "ip_upgrade"} {
		t.Run(operation+" unknown", func(t *testing.T) {
			originalExpiry := int64(100)
			sub := &db.Subscription{IPLimit: 1, ExpireTime: &originalExpiry, EndDate: time.Unix(100, 0), IsActive: true}
			original := snapshotSubscriptionWalletState(sub)
			sub.IPLimit = 3
			updatedExpiry := int64(200)
			sub.ExpireTime = &updatedExpiry
			sub.EndDate = time.Unix(200, 0)
			if !walletRemoteReconciliationRequired(&xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("timeout")}) {
				t.Fatal("UNKNOWN remote result must require reconciliation")
			}
			restoreSubscriptionWalletState(sub, original)
			if sub.IPLimit != 1 || sub.ExpireTime == nil || *sub.ExpireTime != 100 || !sub.EndDate.Equal(time.Unix(100, 0)) || !sub.IsActive {
				t.Fatalf("UNKNOWN outcome must preserve original local state: %+v", sub)
			}
			if walletRemoteRefundAllowed(&xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("timeout")}) {
				t.Fatal("UNKNOWN remote result must not refund")
			}
		})

		t.Run(operation+" definitive failure", func(t *testing.T) {
			originalExpiry := int64(100)
			sub := &db.Subscription{IPLimit: 1, ExpireTime: &originalExpiry, IsActive: true}
			original := snapshotSubscriptionWalletState(sub)
			sub.IPLimit = 3
			restoreSubscriptionWalletState(sub, original)
			if walletRemoteOutcome := classifyWalletRemoteOutcome(&xui.WriteError{Outcome: xui.WriteDefinitiveFailure, Err: errors.New("rejected")}); walletRemoteOutcome != walletRemoteDefinitive {
				t.Fatalf("expected definitive failure, got %s", walletRemoteOutcome)
			}
			if !walletRemoteRefundAllowed(&xui.WriteError{Outcome: xui.WriteDefinitiveFailure, Err: errors.New("rejected")}) {
				t.Fatal("definitive remote failure must allow one idempotent refund")
			}
			if sub.IPLimit != 1 || sub.ExpireTime == nil || *sub.ExpireTime != 100 || !sub.IsActive {
				t.Fatal("definitive failure must preserve the observed local state")
			}
		})

		t.Run(operation+" remote success local write failure", func(t *testing.T) {
			if !walletLocalWriteReconciliationRequired(errors.New("database unavailable")) {
				t.Fatal("DB failure after remote success must require reconciliation")
			}
			if walletRemoteRefundAllowed(nil) {
				t.Fatal("remote success must not be refunded")
			}
		})
	}
}

func TestNilXUIClientReturnsExplicitErrorAndTriggersRefund(t *testing.T) {
	bot.XUIClient = nil
	sub := &db.Subscription{ClientEmail: "test@example.com"}
	err := updateXUIFromSubscription(sub)
	if err == nil {
		t.Fatal("updateXUIFromSubscription with nil XUIClient must return an error, got nil")
	}
	if !errors.Is(err, ErrXUIClientUnavailable) {
		t.Fatalf("expected ErrXUIClientUnavailable, got %v", err)
	}

	renameErr := updateXUIRename("old@example.com", sub)
	if renameErr == nil {
		t.Fatal("updateXUIRename with nil XUIClient must return an error, got nil")
	}
	if !errors.Is(renameErr, ErrXUIClientUnavailable) {
		t.Fatalf("expected ErrXUIClientUnavailable, got %v", renameErr)
	}

	if !walletRemoteRefundAllowed(err) {
		t.Fatal("unavailable XUI client must be classified as a definitive remote failure allowing refund")
	}
	if walletRemoteReconciliationRequired(err) {
		t.Fatal("unavailable XUI client is a known failure and must not be marked unknown")
	}
}

func TestRemoteCreateSuccessDbFailureCompensation(t *testing.T) {
	user := &db.User{ID: 100, TelegramID: 1000}
	plan := &db.PaidPlan{ID: 1, Name: "Test Plan"}
	client := xui.ClientConfig{Email: "user@example.com", ID: "uuid-1", SubID: "sub-1"}
	inbounds := []int{1, 2}
	dbErr := errors.New("db insert duplicate key error")

	// a. remote create succeeds -> DB insert fails -> remote delete confirmed absent -> exactly one refund allowed
	t.Run("Scenario A: delete confirmed absent -> exactly one refund allowed", func(t *testing.T) {
		refundCalls := 0
		reconCalls := 0
		deleteCalls := 0
		verifyCalls := 0

		res := compensateRemoteCreateDbFailure(
			nil, user, plan, client, inbounds, "display", 500, "op_a", dbErr,
			func(email string) error {
				deleteCalls++
				return nil // delete succeeded cleanly
			},
			func(email string) (*xui.XUIClientInfo, error) {
				verifyCalls++
				return nil, nil
			},
			func(ctx context.Context, userID int64, amount float64, desc, origKey, refKey string, subID *int64, extra map[string]any) SafeRefundResult {
				refundCalls++
				if refKey != "op_a:refund" {
					t.Fatalf("unexpected refund key: %s", refKey)
				}
				return SafeRefundResult{Refunded: true}
			},
			func(ctx context.Context, rec *db.ReconciliationRecord) error {
				reconCalls++
				return nil
			},
		)

		if res.Outcome != CompensationRefunded || !res.Refunded {
			t.Fatalf("expected refunded outcome, got %+v", res)
		}
		if deleteCalls != 1 {
			t.Fatalf("expected 1 delete call, got %d", deleteCalls)
		}
		if verifyCalls != 0 {
			t.Fatalf("expected 0 verify calls on clean delete, got %d", verifyCalls)
		}
		if refundCalls != 1 {
			t.Fatalf("expected exactly one refund call, got %d", refundCalls)
		}
		if reconCalls != 0 {
			t.Fatalf("expected 0 reconciliation calls on clean refund, got %d", reconCalls)
		}
	})

	// Case B: remote delete confirmed -> refund fails -> refund reconciliation persists -> outcome = refund pending
	t.Run("Scenario A2 (Case B): delete confirmed -> refund fails -> refund recon persists -> CompensationRefundPending", func(t *testing.T) {
		refundCalls := 0
		deleteCalls := 0
		refundErr := errors.New("wallet credit failed")

		res := compensateRemoteCreateDbFailure(
			nil, user, plan, client, inbounds, "display", 500, "op_case_b", dbErr,
			func(email string) error {
				deleteCalls++
				return nil // delete confirmed
			},
			nil,
			func(ctx context.Context, userID int64, amount float64, desc, origKey, refKey string, subID *int64, extra map[string]any) SafeRefundResult {
				refundCalls++
				return SafeRefundResult{
					Refunded:                false,
					RefundErr:               refundErr,
					ReconciliationPersisted: true,
					ReconciliationErr:       nil,
				}
			},
			nil,
		)

		if res.Outcome != CompensationRefundPending {
			t.Fatalf("expected outcome %s, got %s", CompensationRefundPending, res.Outcome)
		}
		if res.Refunded {
			t.Fatal("must not report refunded")
		}
		if !errors.Is(res.RefundErr, refundErr) {
			t.Fatalf("expected RefundErr %v, got %v", refundErr, res.RefundErr)
		}
		if res.ReconErr != nil {
			t.Fatalf("expected nil ReconErr, got %v", res.ReconErr)
		}
		if deleteCalls != 1 || refundCalls != 1 {
			t.Fatalf("expected 1 delete call and 1 refund call, got %d and %d", deleteCalls, refundCalls)
		}

		// Formatter assertions:
		msg := formatCompensationUserMessage(res, "op_case_b")
		if strings.Contains(msg, "نامشخص") {
			t.Fatalf("formatter must NOT say delete is unknown for confirmed deletion, got: %s", msg)
		}
		if !strings.Contains(msg, "سرویس ایجاد شده در پنل با موفقیت حذف شد") {
			t.Fatalf("formatter must state panel service was deleted, got: %s", msg)
		}
		if !strings.Contains(msg, "درخواست استرداد وجه برای بررسی پشتیبانی ثبت شد") {
			t.Fatalf("formatter must say refund is pending/registered, got: %s", msg)
		}
	})

	// Case C: remote delete confirmed -> refund fails -> refund reconciliation also fails -> CompensationRefundPending with ReconErr
	t.Run("Scenario A3 (Case C): delete confirmed -> refund fails -> refund recon also fails -> CompensationRefundPending with ReconErr", func(t *testing.T) {
		refundCalls := 0
		deleteCalls := 0
		refundErr := errors.New("wallet credit failed")
		reconErr := errors.New("reconciliation insert failed")

		res := compensateRemoteCreateDbFailure(
			nil, user, plan, client, inbounds, "display", 500, "op_case_c", dbErr,
			func(email string) error {
				deleteCalls++
				return nil // delete confirmed
			},
			nil,
			func(ctx context.Context, userID int64, amount float64, desc, origKey, refKey string, subID *int64, extra map[string]any) SafeRefundResult {
				refundCalls++
				return SafeRefundResult{
					Refunded:                false,
					RefundErr:               refundErr,
					ReconciliationPersisted: false,
					ReconciliationErr:       reconErr,
				}
			},
			nil,
		)

		if res.Outcome != CompensationRefundPending {
			t.Fatalf("expected outcome %s, got %s", CompensationRefundPending, res.Outcome)
		}
		if res.Refunded {
			t.Fatal("must not report refunded")
		}
		if !errors.Is(res.RefundErr, refundErr) {
			t.Fatalf("expected RefundErr %v, got %v", refundErr, res.RefundErr)
		}
		if !errors.Is(res.ReconErr, reconErr) {
			t.Fatalf("expected ReconErr %v, got %v", reconErr, res.ReconErr)
		}
		if deleteCalls != 1 || refundCalls != 1 {
			t.Fatalf("expected 1 delete call and 1 refund call, got %d and %d", deleteCalls, refundCalls)
		}

		// Formatter assertions:
		msg := formatCompensationUserMessage(res, "op_case_c")
		if strings.Contains(msg, "نامشخص") {
			t.Fatalf("formatter must NOT say delete is unknown for confirmed deletion, got: %s", msg)
		}
		if !strings.Contains(msg, "سرویس ایجاد شده در پنل با موفقیت حذف شد") {
			t.Fatalf("formatter must state panel service was deleted, got: %s", msg)
		}
		if !strings.Contains(msg, "هیچ درخواستی به‌طور خودکار ثبت نشده است") {
			t.Fatalf("formatter must say no automatic request was durably registered, got: %s", msg)
		}
	})

	// b. remote create succeeds -> DB insert fails -> delete times out -> verification unavailable -> no refund, reconciliation required
	t.Run("Scenario B: delete timeout -> verify unavailable -> no refund, reconciliation required", func(t *testing.T) {
		refundCalls := 0
		reconCalls := 0
		deleteCalls := 0
		verifyCalls := 0
		var savedRec *db.ReconciliationRecord

		res := compensateRemoteCreateDbFailure(
			nil, user, plan, client, inbounds, "display", 500, "op_b", dbErr,
			func(email string) error {
				deleteCalls++
				return &xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("delete timeout")}
			},
			func(email string) (*xui.XUIClientInfo, error) {
				verifyCalls++
				return nil, errors.New("verify endpoint unavailable")
			},
			func(ctx context.Context, userID int64, amount float64, desc, origKey, refKey string, subID *int64, extra map[string]any) SafeRefundResult {
				refundCalls++
				return SafeRefundResult{Refunded: true}
			},
			func(ctx context.Context, rec *db.ReconciliationRecord) error {
				reconCalls++
				savedRec = rec
				return nil
			},
		)

		if res.Outcome != CompensationReconciliationRequired || res.Refunded {
			t.Fatalf("expected reconciliation_required without refund, got %+v", res)
		}
		if deleteCalls != 1 || verifyCalls != 1 {
			t.Fatalf("expected 1 delete and 1 verify call, got %d, %d", deleteCalls, verifyCalls)
		}
		if refundCalls != 0 {
			t.Fatalf("refund must NEVER be issued when delete outcome is unknown, got %d calls", refundCalls)
		}
		if reconCalls != 1 || savedRec == nil {
			t.Fatalf("expected 1 reconciliation record persisted, got %d", reconCalls)
		}
		if savedRec.DesiredState["action"] != "confirm_delete_and_refund" {
			t.Fatalf("expected confirm_delete_and_refund action, got %v", savedRec.DesiredState["action"])
		}
		if savedRec.DesiredState["client_id"] != client.ID || savedRec.DesiredState["email"] != client.Email {
			t.Fatalf("reconciliation record must retain exact client identity: %+v", savedRec.DesiredState)
		}
	})

	// c. remote create succeeds -> DB insert fails -> delete timeout -> GET confirms absent -> exactly one refund
	t.Run("Scenario C: delete timeout -> GET confirms absent -> exactly one refund", func(t *testing.T) {
		refundCalls := 0
		reconCalls := 0
		deleteCalls := 0
		verifyCalls := 0

		res := compensateRemoteCreateDbFailure(
			nil, user, plan, client, inbounds, "display", 500, "op_c", dbErr,
			func(email string) error {
				deleteCalls++
				return &xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("delete timeout")}
			},
			func(email string) (*xui.XUIClientInfo, error) {
				verifyCalls++
				return nil, xui.ErrNotFound // confirmed absent on readback
			},
			func(ctx context.Context, userID int64, amount float64, desc, origKey, refKey string, subID *int64, extra map[string]any) SafeRefundResult {
				refundCalls++
				if refKey != "op_c:refund" {
					t.Fatalf("unexpected refund key: %s", refKey)
				}
				return SafeRefundResult{Refunded: true}
			},
			func(ctx context.Context, rec *db.ReconciliationRecord) error {
				reconCalls++
				return nil
			},
		)

		if res.Outcome != CompensationRefunded || !res.Refunded {
			t.Fatalf("expected refunded outcome after GET confirmed absent, got %+v", res)
		}
		if deleteCalls != 1 || verifyCalls != 1 {
			t.Fatalf("expected 1 delete and 1 verify call, got %d, %d", deleteCalls, verifyCalls)
		}
		if refundCalls != 1 {
			t.Fatalf("expected exactly 1 refund call, got %d", refundCalls)
		}
		if reconCalls != 0 {
			t.Fatalf("expected 0 reconciliation calls on confirmed absence refund, got %d", reconCalls)
		}
	})

	// d. remote create succeeds -> DB insert fails -> GET confirms client still present -> no refund, reconciliation required
	t.Run("Scenario D: GET confirms client still present -> no refund, reconciliation required", func(t *testing.T) {
		refundCalls := 0
		reconCalls := 0
		deleteCalls := 0
		verifyCalls := 0
		var savedRec *db.ReconciliationRecord

		res := compensateRemoteCreateDbFailure(
			nil, user, plan, client, inbounds, "display", 500, "op_d", dbErr,
			func(email string) error {
				deleteCalls++
				return &xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("delete timeout")}
			},
			func(email string) (*xui.XUIClientInfo, error) {
				verifyCalls++
				return &xui.XUIClientInfo{Email: email}, nil // client still present
			},
			func(ctx context.Context, userID int64, amount float64, desc, origKey, refKey string, subID *int64, extra map[string]any) SafeRefundResult {
				refundCalls++
				return SafeRefundResult{Refunded: true}
			},
			func(ctx context.Context, rec *db.ReconciliationRecord) error {
				reconCalls++
				savedRec = rec
				return nil
			},
		)

		if res.Outcome != CompensationClientStillPresent || res.Refunded {
			t.Fatalf("expected client_still_present without refund, got %+v", res)
		}
		if deleteCalls != 1 || verifyCalls != 1 {
			t.Fatalf("expected 1 delete and 1 verify call, got %d, %d", deleteCalls, verifyCalls)
		}
		if refundCalls != 0 {
			t.Fatalf("must NEVER refund if client is still present on panel, got %d calls", refundCalls)
		}
		if reconCalls != 1 || savedRec == nil {
			t.Fatalf("expected 1 reconciliation record persisted, got %d", reconCalls)
		}
		if savedRec.DesiredState["action"] != "adopt_subscription_or_delete" {
			t.Fatalf("expected adopt_subscription_or_delete action, got %v", savedRec.DesiredState["action"])
		}
		if savedRec.DesiredState["client_id"] != client.ID {
			t.Fatalf("reconciliation record must preserve client identity: %+v", savedRec.DesiredState)
		}
	})
}

func TestSafeRefundWalletOutcomes(t *testing.T) {
	ctx := context.Background()

	// a. refund succeeds
	t.Run("refund succeeds", func(t *testing.T) {
		res := safeRefundWalletWithDeps(ctx, 100, 500, "refund", "orig_key", "ref_key", nil, nil,
			func(ctx context.Context, userID int64, amount float64, description, operationKey string) error {
				return nil
			},
			func(ctx context.Context, record *db.ReconciliationRecord) error {
				t.Fatal("reconciliation must not be called when refund succeeds")
				return nil
			},
		)
		if !res.Refunded || res.AlreadyRefunded || res.RefundErr != nil {
			t.Fatalf("expected refund success, got: %+v", res)
		}
		if res.ReconciliationPersisted || res.ReconciliationErr != nil {
			t.Fatalf("unexpected reconciliation state on refund success: %+v", res)
		}
	})

	// b. refund already applied (idempotent success)
	t.Run("refund already applied", func(t *testing.T) {
		res := safeRefundWalletWithDeps(ctx, 100, 500, "refund", "orig_key", "ref_key", nil, nil,
			func(ctx context.Context, userID int64, amount float64, description, operationKey string) error {
				return db.ErrWalletOperationAlreadyApplied
			},
			func(ctx context.Context, record *db.ReconciliationRecord) error {
				t.Fatal("reconciliation must not be called when refund was already applied")
				return nil
			},
		)
		if !res.Refunded || !res.AlreadyRefunded || res.RefundErr != nil {
			t.Fatalf("expected already refunded success, got: %+v", res)
		}
		if res.ReconciliationPersisted || res.ReconciliationErr != nil {
			t.Fatalf("unexpected reconciliation state: %+v", res)
		}
	})

	// c. refund fails but reconciliation persists
	t.Run("refund fails but reconciliation persists", func(t *testing.T) {
		creditErr := errors.New("db credit failure")
		var savedRec *db.ReconciliationRecord
		res := safeRefundWalletWithDeps(ctx, 100, 500, "refund", "orig_key", "ref_key", nil, nil,
			func(ctx context.Context, userID int64, amount float64, description, operationKey string) error {
				return creditErr
			},
			func(ctx context.Context, record *db.ReconciliationRecord) error {
				savedRec = record
				return nil
			},
		)
		if res.Refunded || res.AlreadyRefunded {
			t.Fatalf("refund must not report success, got: %+v", res)
		}
		if !errors.Is(res.RefundErr, creditErr) {
			t.Fatalf("expected creditErr, got: %v", res.RefundErr)
		}
		if !res.ReconciliationPersisted || res.ReconciliationErr != nil {
			t.Fatalf("expected reconciliation to be persisted without error, got: %+v", res)
		}
		if savedRec == nil || savedRec.OperationKey != "ref_key" || savedRec.Kind != "pending_refund" {
			t.Fatalf("reconciliation record was not correctly populated: %+v", savedRec)
		}
	})

	// d. refund fails and reconciliation persistence also fails
	t.Run("refund fails and reconciliation persistence also fails", func(t *testing.T) {
		creditErr := errors.New("db credit failure")
		reconErr := errors.New("db reconciliation insert failure")
		res := safeRefundWalletWithDeps(ctx, 100, 500, "refund", "orig_key", "ref_key", nil, nil,
			func(ctx context.Context, userID int64, amount float64, description, operationKey string) error {
				return creditErr
			},
			func(ctx context.Context, record *db.ReconciliationRecord) error {
				return reconErr
			},
		)
		if res.Refunded || res.AlreadyRefunded {
			t.Fatalf("refund must not report success, got: %+v", res)
		}
		if !errors.Is(res.RefundErr, creditErr) {
			t.Fatalf("expected creditErr, got: %v", res.RefundErr)
		}
		// Assert the result explicitly indicates that nothing durable was registered
		if res.ReconciliationPersisted {
			t.Fatalf("reconciliation must NOT report persisted when it failed, got: %+v", res)
		}
		if !errors.Is(res.ReconciliationErr, reconErr) {
			t.Fatalf("expected reconErr, got: %v", res.ReconciliationErr)
		}
	})
}

func TestRemoteCreateCompensationReconPersistenceFailure(t *testing.T) {
	user := &db.User{ID: 123, TelegramID: 456}
	plan := &db.PaidPlan{ID: 1, Name: "Test Plan"}
	client := xui.ClientConfig{Email: "user@example.com", ID: "uuid-1", SubID: "sub-1"}
	inbounds := []int{1}
	dbErr := errors.New("db insert failure")
	reconInsertErr := errors.New("db reconciliation connection down")

	// Case 1: delete outcome unknown, recon persistence fails
	t.Run("delete unknown with recon persistence failure", func(t *testing.T) {
		res := compensateRemoteCreateDbFailure(
			context.Background(), user, plan, client, inbounds, "display", 500, "op_recon_fail", dbErr,
			func(email string) error {
				return &xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("delete timeout")}
			},
			func(email string) (*xui.XUIClientInfo, error) {
				return nil, errors.New("verify unavailable")
			},
			nil,
			func(ctx context.Context, rec *db.ReconciliationRecord) error {
				return reconInsertErr
			},
		)

		if res.Outcome != CompensationReconciliationRequired {
			t.Fatalf("expected CompensationReconciliationRequired, got %s", res.Outcome)
		}
		if res.Refunded {
			t.Fatal("must not refund")
		}
		if !errors.Is(res.ReconErr, reconInsertErr) {
			t.Fatalf("expected ReconErr to be returned, got: %v", res.ReconErr)
		}

		// Assert calling decision logic produces critical manual-support message rather than false registration
		msg := formatCompensationUserMessage(res, "op_recon_fail")
		if strings.Contains(msg, "درخواست برای بررسی پشتیبانی ثبت شد") {
			t.Fatalf("must not claim request was successfully registered when ReconErr != nil, got: %s", msg)
		}
		if !strings.Contains(msg, "هیچ درخواستی به‌طور خودکار ثبت نشده است") && !strings.Contains(msg, "خطا مواجه شد") {
			t.Fatalf("expected manual-support/failure message, got: %s", msg)
		}
	})

	// Case 2: client still present, recon persistence fails
	t.Run("client present with recon persistence failure", func(t *testing.T) {
		res := compensateRemoteCreateDbFailure(
			context.Background(), user, plan, client, inbounds, "display", 500, "op_present_recon_fail", dbErr,
			func(email string) error {
				return &xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("delete timeout")}
			},
			func(email string) (*xui.XUIClientInfo, error) {
				return &xui.XUIClientInfo{Email: email}, nil // client still present
			},
			nil,
			func(ctx context.Context, rec *db.ReconciliationRecord) error {
				return reconInsertErr
			},
		)

		if res.Outcome != CompensationClientStillPresent {
			t.Fatalf("expected CompensationClientStillPresent, got %s", res.Outcome)
		}
		if res.Refunded {
			t.Fatal("must not refund")
		}
		if !errors.Is(res.ReconErr, reconInsertErr) {
			t.Fatalf("expected ReconErr to be returned, got: %v", res.ReconErr)
		}

		msg := formatCompensationUserMessage(res, "op_present_recon_fail")
		if strings.Contains(msg, "توسط پشتیبانی ثبت شد") {
			t.Fatalf("must not claim status was registered when ReconErr != nil, got: %s", msg)
		}
		if !strings.Contains(msg, "هیچ درخواستی به‌طور خودکار ثبت نشده است") && !strings.Contains(msg, "خطا مواجه شد") {
			t.Fatalf("expected manual-support/failure message, got: %s", msg)
		}
	})
}
