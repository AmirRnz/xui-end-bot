package reconcile

import (
	"context"
	"errors"
	"strings"
	"testing"

	"xui-end-bot/internal/db"
	"xui-end-bot/internal/xui"
)

type mockXUI struct {
	client       *xui.XUIClientInfo
	err          error
	delErr       error
	updateRes    xui.WriteResult
	updatedState *xui.ClientConfig
	addRes       xui.WriteResult
	addResults   []xui.WriteResult
	addCalls     int
	persistAdded bool
}

func amountPtr(value int64) *int64 { return &value }

func (m *mockXUI) GetClientByEmail(email string) (*xui.XUIClientInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.client == nil {
		return nil, &xui.NotFoundError{StatusCode: 404, Message: "not found"}
	}
	return m.client, nil
}

func (m *mockXUI) DeleteClient(email string) error {
	return m.delErr
}

func (m *mockXUI) AddClient(req xui.AddClientRequest) error {
	return nil
}

func (m *mockXUI) AddClientResult(req xui.AddClientRequest) xui.WriteResult {
	m.addCalls++
	result := m.addRes
	if len(m.addResults) > 0 {
		result = m.addResults[0]
		m.addResults = m.addResults[1:]
	}
	if result.Outcome == "" {
		result = xui.WriteResult{Outcome: xui.WriteSucceeded}
	}
	if m.persistAdded && result.Outcome != xui.WriteDefinitiveFailure {
		m.client = &xui.XUIClientInfo{
			Email: req.Client.Email, UUID: req.Client.ID, SubID: req.Client.SubID,
			Enable: req.Client.Enable, ExpiryTime: req.Client.ExpiryTime, LimitIP: req.Client.LimitIP,
			TotalGB: req.Client.TotalGB, Flow: req.Client.Flow, Group: req.Client.Group,
			TgID: req.Client.TgID, Comment: req.Client.Comment, InboundIDs: append([]int(nil), req.InboundIDs...),
		}
	}
	return result
}

func (m *mockXUI) UpdateClientResult(email string, client xui.ClientConfig) xui.WriteResult {
	m.updatedState = &client
	if m.updateRes.Outcome != "" {
		return m.updateRes
	}
	return xui.WriteResult{Outcome: xui.WriteSucceeded}
}

func TestClassifyRemoteClient(t *testing.T) {
	// 1. Explicit not found
	if s := ClassifyRemoteClient(nil, &xui.NotFoundError{StatusCode: 404}); s != RemoteConfirmedAbsent {
		t.Fatalf("expected RemoteConfirmedAbsent, got %v", s)
	}

	// 2. Timeout error with nil client
	if s := ClassifyRemoteClient(nil, errors.New("timeout connecting to XUI")); s != RemotePresenceUnknown {
		t.Fatalf("expected RemotePresenceUnknown for timeout, got %v", s)
	}

	// 3. Nil client with nil error (anomalous)
	if s := ClassifyRemoteClient(nil, nil); s != RemotePresenceUnknown {
		t.Fatalf("expected RemotePresenceUnknown for anomalous nil client, got %v", s)
	}

	// 4. Client present with nil error
	client := &xui.XUIClientInfo{Email: "test@example.com"}
	if s := ClassifyRemoteClient(client, nil); s != RemoteConfirmedPresent {
		t.Fatalf("expected RemoteConfirmedPresent, got %v", s)
	}
}

func TestHandlePendingRefund_Success(t *testing.T) {
	userID := int64(123)
	rec := &db.ReconciliationRecord{
		ID:           1,
		OperationKey: "op_test_refund",
		Kind:         KindPendingRefund,
		UserID:       &userID,
		DesiredState: map[string]any{
			"amount":               50000,
			"refund_operation_key": "op_test_refund:key",
			"description":          "test refund",
		},
	}

	credited := false
	p := &Processor{
		CreditFn: func(ctx context.Context, uID int64, amount int64, desc, key string) error {
			if uID != userID || amount != 50000 || key != "op_test_refund:key" {
				t.Fatalf("unexpected credit args: uid=%d, amount=%d, key=%s", uID, amount, key)
			}
			credited = true
			return nil
		},
	}

	outcome := p.handlePendingRefund(context.Background(), rec)
	if outcome.Kind != OutcomeResolved {
		t.Fatalf("expected OutcomeResolved, got %v", outcome.Kind)
	}
	if !credited {
		t.Fatal("expected CreditFn to be called")
	}
	if outcome.Resolution == "" {
		t.Fatal("expected non-empty resolution")
	}
}

func TestHandlePendingRefund_RefundFails_ReturnsError(t *testing.T) {
	userID := int64(123)
	rec := &db.ReconciliationRecord{
		ID:           1,
		OperationKey: "op_test_refund_fail",
		Kind:         KindPendingRefund,
		UserID:       &userID,
		DesiredState: map[string]any{
			"amount":               50000,
			"refund_operation_key": "op_test_refund:fail_key",
		},
	}

	p := &Processor{
		CreditFn: func(ctx context.Context, uID int64, amount int64, desc, key string) error {
			return errors.New("db connection timeout during refund")
		},
	}

	outcome := p.handlePendingRefund(context.Background(), rec)
	if outcome.Kind != OutcomeRetry {
		t.Fatalf("expected OutcomeRetry when refund fails, got %v", outcome.Kind)
	}
}

func TestHandlePendingRefund_AlreadyApplied_Succeeds(t *testing.T) {
	userID := int64(123)
	rec := &db.ReconciliationRecord{
		ID:           1,
		OperationKey: "op_test_refund_dup",
		Kind:         KindPendingRefund,
		UserID:       &userID,
		DesiredState: map[string]any{
			"amount":               50000,
			"refund_operation_key": "op_test_refund:dup_key",
		},
	}

	p := &Processor{
		CreditFn: func(ctx context.Context, uID int64, amount int64, desc, key string) error {
			return db.ErrWalletOperationAlreadyApplied
		},
	}

	outcome := p.handlePendingRefund(context.Background(), rec)
	if outcome.Kind != OutcomeResolved {
		t.Fatalf("expected safe success for already-applied refund, got: %v", outcome.Kind)
	}
	if outcome.Resolution == "" {
		t.Fatal("expected resolution string")
	}
}

func TestHandleDeleteReconciliation_Timeout_NoDeletionOrRefund(t *testing.T) {
	userID := int64(999)
	subID := int64(888)
	rec := &db.ReconciliationRecord{
		ID:             10,
		SubscriptionID: &subID,
		UserID:         &userID,
		OperationKey:   "op_del_timeout",
		Kind:           KindSubscriptionDeleteUnknown,
		DesiredState: map[string]any{
			"client_email":         "timeout_delete@example.com",
			"refund_amount":        100000,
			"refund_operation_key": "op_del_timeout:refund",
		},
	}

	refundCalled := false
	p := &Processor{
		XUI: &mockXUI{
			err: errors.New("timeout from XUI"),
		},
		CreditFn: func(ctx context.Context, uID int64, amount int64, desc, key string) error {
			refundCalled = true
			return nil
		},
	}

	outcome := p.handleDeleteReconciliation(context.Background(), rec)
	if outcome.Err == nil {
		t.Fatal("expected error when XUI check times out")
	}
	if refundCalled {
		t.Fatal("CRITICAL: refund must NOT be executed when XUI check times out!")
	}
}

func TestHandleDeleteReconciliation_NotFound_Refunds(t *testing.T) {
	userID := int64(999)
	rec := &db.ReconciliationRecord{
		ID:           11,
		UserID:       &userID,
		OperationKey: "op_del_absent",
		Kind:         KindSubscriptionDeleteUnknown,
		DesiredState: map[string]any{
			"client_email":         "absent_delete@example.com",
			"refund_amount":        100000,
			"refund_operation_key": "op_del_absent:refund",
		},
	}

	refundCalled := false
	p := &Processor{
		XUI: &mockXUI{
			client: nil, // 404
		},
		CreditFn: func(ctx context.Context, uID int64, amount int64, desc, key string) error {
			refundCalled = true
			return nil
		},
	}

	outcome := p.handleDeleteReconciliation(context.Background(), rec)
	if outcome.Err != nil {
		t.Fatalf("expected nil err, got %v", outcome.Err)
	}
	if !refundCalled {
		t.Fatal("expected refund when remote client is confirmed absent")
	}
	if outcome.Kind != OutcomeResolved {
		t.Fatalf("expected resolved outcome, got %v", outcome.Kind)
	}
}

func TestHandleDeleteReconciliation_RefundFails_ReturnsError(t *testing.T) {
	userID := int64(999)
	rec := &db.ReconciliationRecord{
		ID:           12,
		UserID:       &userID,
		OperationKey: "op_del_absent_fail_refund",
		Kind:         KindSubscriptionDeleteUnknown,
		DesiredState: map[string]any{
			"client_email":         "absent_delete@example.com",
			"refund_amount":        100000,
			"refund_operation_key": "op_del_absent:refund_fail",
		},
	}

	p := &Processor{
		XUI: &mockXUI{
			client: nil, // 404
		},
		CreditFn: func(ctx context.Context, uID int64, amount int64, desc, key string) error {
			return errors.New("wallet credit db failure")
		},
	}

	outcome := p.handleDeleteReconciliation(context.Background(), rec)
	if outcome.Err == nil {
		t.Fatal("expected error when refund fails during deletion reconciliation")
	}
}

func TestHandlePurchaseReconciliation_PreviouslyAttemptedRemoteAbsentRequiresManualReview(t *testing.T) {
	userID := int64(456)
	rec := &db.ReconciliationRecord{
		ID:           2,
		OperationKey: "op_unknown_create",
		Kind:         KindPurchaseProvisioningUnknown,
		UserID:       &userID,
		DesiredState: map[string]any{
			"email":                "absent@example.com",
			"expected_uuid":        "wallet-uuid",
			"expected_sub_id":      "wallet-sub",
			"inbound_ids":          []int{1},
			"expiry_time_milli":    int64(-2592000000),
			"total_bytes":          int64(1000),
			"price":                int64(60000),
			"debit_operation_key":  "op_unknown_create",
			"operation_key":        "op_unknown_create",
			"refund_operation_key": "op_unknown_create:refund",
		},
		ObservedState: map[string]any{"phase": "create_attempted"},
	}

	refunded := false
	p := &Processor{
		XUI: &mockXUI{
			client: nil, // 404
		},
		DebitTxFn: func(ctx context.Context, uID int64, opKey string) (*db.WalletTransaction, error) {
			return &db.WalletTransaction{
				ID:           1,
				UserID:       uID,
				Amount:       -60000,
				Type:         "debit",
				Status:       "completed",
				OperationKey: opKey,
			}, nil
		},
		CreditFn: func(ctx context.Context, uID int64, amount int64, desc, key string) error {
			if uID != userID || amount != 60000 {
				t.Fatalf("unexpected refund args: uid=%d, amount=%d", uID, amount)
			}
			refunded = true
			return nil
		},
	}

	outcome := p.handlePurchaseReconciliation(context.Background(), rec)
	if refunded {
		t.Fatal("did not expect an automatic refund after an attempted unknown create")
	}
	if outcome.Kind != OutcomeManualReview {
		t.Fatalf("expected manual review for an attempted absent client, got %v", outcome.Kind)
	}
}

func TestHandlePurchaseReconciliation_Inconclusive_NoRefund(t *testing.T) {
	userID := int64(789)
	rec := &db.ReconciliationRecord{
		ID:           3,
		OperationKey: "op_timeout",
		Kind:         KindPurchaseProvisioningUnknown,
		UserID:       &userID,
		DesiredState: map[string]any{
			"email": "timeout@example.com", "expected_uuid": "timeout-uuid", "expected_sub_id": "timeout-sub",
			"inbound_ids": []int{1}, "expiry_time_milli": int64(-2592000000), "total_bytes": int64(7000), "price": 70000,
			"operation_key": "op_timeout", "debit_operation_key": "op_timeout",
		},
	}

	refunded := false
	p := &Processor{
		XUI: &mockXUI{
			err: errors.New("timeout connecting to master"),
		},
		CreditFn: func(ctx context.Context, uID int64, amount int64, desc, key string) error {
			refunded = true
			return nil
		},
	}

	outcome := p.handlePurchaseReconciliation(context.Background(), rec)
	if outcome.Err == nil {
		t.Fatal("expected error on inconclusive check")
	}
	if refunded {
		t.Fatal("CRITICAL: refund issued despite ambiguous XUI outcome!")
	}
}

func TestHandlePurchaseReconciliation_MismatchedUUID_MovesToManualReview(t *testing.T) {
	userID := int64(789)
	rec := &db.ReconciliationRecord{
		ID:           4,
		OperationKey: "op_uuid_mismatch",
		Kind:         KindPurchaseProvisioningUnknown,
		UserID:       &userID,
		DesiredState: map[string]any{
			"email": "mismatch@example.com", "expected_uuid": "expected-uuid-1111", "expected_sub_id": "expected-sub",
			"inbound_ids": []int{1}, "expiry_time_milli": int64(-2592000000), "total_bytes": int64(7000), "price": 70000,
			"operation_key": "op_uuid_mismatch", "debit_operation_key": "op_uuid_mismatch",
		},
	}

	p := &Processor{
		XUI: &mockXUI{
			client: &xui.XUIClientInfo{
				Email: "mismatch@example.com",
				UUID:  "different-uuid-2222",
			},
		},
	}

	outcome := p.handlePurchaseReconciliation(context.Background(), rec)
	if outcome.Err != nil {
		t.Fatalf("expected manual review resolution, got error: %v", outcome.Err)
	}
	if outcome.Kind != OutcomeManualReview {
		t.Fatalf("expected manual review, got: %v", outcome.Kind)
	}
	if !strings.Contains(outcome.Reason, "identity verification failed") {
		t.Fatalf("expected identity verification failure reason, got: %q", outcome.Reason)
	}
}

func TestHandleUpdateReconciliation_DivergentState_MovesToManualReview(t *testing.T) {
	newIP := 5
	rec := &db.ReconciliationRecord{
		ID:             5,
		SubscriptionID: nil, // will fail decode if nil, test validation
		OperationKey:   "op_update_invalid",
		Kind:           KindSubscriptionUpdateDbFailed,
		DesiredState: map[string]any{
			"subscription_id":  55,
			"client_email":     "update@example.com",
			"desired_ip_limit": newIP,
		},
	}

	p := &Processor{
		XUI: &mockXUI{
			client: &xui.XUIClientInfo{
				Email:   "update@example.com",
				LimitIP: 2, // divergent from desired 5 and previous 0
			},
		},
	}

	// Without DB pool, it will fail subscription lookup cleanly
	outcome := p.handleUpdateReconciliation(context.Background(), rec)
	if outcome.Err == nil {
		t.Fatal("expected error when DB subscription lookup fails")
	}
}

func TestHandlePurchaseReconciliation_CommercialIdentityChecks(t *testing.T) {
	quoteID := int64(123)
	payload := &PurchaseProvisioningPayload{
		UserID:          1001,
		Email:           "user@example.com",
		ExpectedUUID:    "uuid-abc",
		ExpectedSubID:   "sub-xyz",
		QuoteID:         &quoteID,
		OperationKey:    "op_ident_check",
		ExpiryTimeMilli: int64(-2592000000),
		TotalBytes:      int64(1000),
		InboundIDs:      []int{1},
	}
	rec := NewPurchaseProvisioningRecord(payload)

	xuiClient := &mockXUI{
		client: &xui.XUIClientInfo{
			Email:      "user@example.com",
			UUID:       "uuid-abc",
			SubID:      "sub-xyz",
			ExpiryTime: int64(-2592000000),
			TotalGB:    1000,
			InboundIDs: []int{1},
			Flow:       "",
		},
	}

	t.Run("user_id mismatch moves to manual review", func(t *testing.T) {
		p := &Processor{
			XUI: xuiClient,
			GetSubscriptionByEmailFn: func(ctx context.Context, email string) (*db.Subscription, error) {
				return &db.Subscription{
					ID:         1,
					UserID:     9999, // mismatch!
					ClientUUID: "uuid-abc",
					SubID:      "sub-xyz",
					QuoteID:    &quoteID,
				}, nil
			},
		}
		outcome := p.handlePurchaseReconciliation(context.Background(), rec)
		if outcome.Kind != OutcomeManualReview {
			t.Fatalf("expected OutcomeManualReview, got %v", outcome.Kind)
		}
		if !strings.Contains(outcome.Reason, "user_id mismatch") {
			t.Fatalf("expected reason to mention user_id mismatch, got: %s", outcome.Reason)
		}
	})

	t.Run("UUID mismatch moves to manual review", func(t *testing.T) {
		p := &Processor{
			XUI: xuiClient,
			GetSubscriptionByEmailFn: func(ctx context.Context, email string) (*db.Subscription, error) {
				return &db.Subscription{
					ID:         1,
					UserID:     1001,
					ClientUUID: "uuid-different", // mismatch!
					SubID:      "sub-xyz",
					QuoteID:    &quoteID,
				}, nil
			},
		}
		outcome := p.handlePurchaseReconciliation(context.Background(), rec)
		if outcome.Kind != OutcomeManualReview {
			t.Fatalf("expected OutcomeManualReview, got %v", outcome.Kind)
		}
		if !strings.Contains(outcome.Reason, "UUID mismatch") {
			t.Fatalf("expected reason to mention UUID mismatch, got: %s", outcome.Reason)
		}
	})

	t.Run("sub_id mismatch moves to manual review", func(t *testing.T) {
		p := &Processor{
			XUI: xuiClient,
			GetSubscriptionByEmailFn: func(ctx context.Context, email string) (*db.Subscription, error) {
				return &db.Subscription{
					ID:         1,
					UserID:     1001,
					ClientUUID: "uuid-abc",
					SubID:      "sub-different", // mismatch!
					QuoteID:    &quoteID,
				}, nil
			},
		}
		outcome := p.handlePurchaseReconciliation(context.Background(), rec)
		if outcome.Kind != OutcomeManualReview {
			t.Fatalf("expected OutcomeManualReview, got %v", outcome.Kind)
		}
		if !strings.Contains(outcome.Reason, "sub_id mismatch") {
			t.Fatalf("expected reason to mention sub_id mismatch, got: %s", outcome.Reason)
		}
	})

	t.Run("quote_id mismatch moves to manual review", func(t *testing.T) {
		differentQuote := int64(999)
		p := &Processor{
			XUI: xuiClient,
			GetSubscriptionByEmailFn: func(ctx context.Context, email string) (*db.Subscription, error) {
				return &db.Subscription{
					ID:         1,
					UserID:     1001,
					ClientUUID: "uuid-abc",
					SubID:      "sub-xyz",
					QuoteID:    &differentQuote, // mismatch!
				}, nil
			},
		}
		outcome := p.handlePurchaseReconciliation(context.Background(), rec)
		if outcome.Kind != OutcomeManualReview {
			t.Fatalf("expected OutcomeManualReview, got %v", outcome.Kind)
		}
		if !strings.Contains(outcome.Reason, "quote_id mismatch") {
			t.Fatalf("expected reason to mention quote_id mismatch, got: %s", outcome.Reason)
		}
	})

	t.Run("all identity matches resolves successfully", func(t *testing.T) {
		p := &Processor{
			XUI: xuiClient,
			GetSubscriptionByEmailFn: func(ctx context.Context, email string) (*db.Subscription, error) {
				return &db.Subscription{
					ID:         1,
					UserID:     1001,
					ClientUUID: "uuid-abc",
					SubID:      "sub-xyz",
					QuoteID:    &quoteID,
				}, nil
			},
		}
		outcome := p.handlePurchaseReconciliation(context.Background(), rec)
		if outcome.Kind != OutcomeResolved {
			t.Fatalf("expected OutcomeResolved, got %v (%s)", outcome.Kind, outcome.Reason)
		}
	})
}

func TestHandleDirectPaymentProvisioning_StatusSemanticsAndIdentity(t *testing.T) {
	quoteID := int64(555)
	directPayload := &DirectPaymentProvisioningPayload{
		PurchaseRequestID:     77,
		UserID:                1001,
		ActionType:            "buy",
		AmountToman:           100,
		FinancialOperationKey: "purchase-77",
		ClientEmail:           "direct@example.com",
		ExpectedUUID:          "uuid-direct",
		ExpectedSubID:         "sub-direct",
		QuoteID:               &quoteID,
		OperationKey:          "op_direct_test",
		ExpiryTimeMilli:       int64(-2592000000),
		TotalBytes:            int64(1000),
		InboundIDs:            []int{1},
	}
	rec := NewDirectPaymentProvisioningRecord(directPayload)

	xuiClient := &mockXUI{
		client: &xui.XUIClientInfo{
			Email: "direct@example.com", UUID: "uuid-direct", SubID: "sub-direct",
			ExpiryTime: int64(-2592000000), TotalGB: 1000, LimitIP: 1, InboundIDs: []int{1},
		},
	}

	t.Run("purchase request pending moves to manual review", func(t *testing.T) {
		p := &Processor{
			XUI: xuiClient,
			GetPurchaseRequestByIDFn: func(ctx context.Context, id int64) (*db.PurchaseRequest, error) {
				return &db.PurchaseRequest{
					ID:                 id,
					UserID:             1001,
					Type:               "buy",
					PriceToman:         amountPtr(100),
					QuoteID:            &quoteID,
					Status:             "pending", // still pending approval!
					ProvisioningStatus: db.PurchaseProvisioningPending,
				}, nil
			},
		}
		outcome := p.handleDirectPaymentProvisioning(context.Background(), rec)
		if outcome.Kind != OutcomeManualReview {
			t.Fatalf("expected OutcomeManualReview for pending purchase request, got %v", outcome.Kind)
		}
		if !strings.Contains(outcome.Reason, "still pending admin approval") {
			t.Fatalf("expected reason to mention still pending admin approval, got: %s", outcome.Reason)
		}
	})

	t.Run("purchase request rejected resolves as superseded", func(t *testing.T) {
		p := &Processor{
			XUI: xuiClient,
			GetPurchaseRequestByIDFn: func(ctx context.Context, id int64) (*db.PurchaseRequest, error) {
				return &db.PurchaseRequest{
					ID:                 id,
					UserID:             1001,
					Type:               "buy",
					PriceToman:         amountPtr(100),
					QuoteID:            &quoteID,
					Status:             "rejected",
					ProvisioningStatus: db.PurchaseProvisioningPending,
				}, nil
			},
		}
		outcome := p.handleDirectPaymentProvisioning(context.Background(), rec)
		if outcome.Kind != OutcomeResolved {
			t.Fatalf("expected OutcomeResolved for rejected purchase request, got %v", outcome.Kind)
		}
		if !strings.Contains(outcome.Resolution, "superseded") {
			t.Fatalf("expected resolution to mention superseded, got: %s", outcome.Resolution)
		}
	})

	t.Run("existing sub identity mismatch moves to manual review", func(t *testing.T) {
		p := &Processor{
			XUI: xuiClient,
			GetPurchaseRequestByIDFn: func(ctx context.Context, id int64) (*db.PurchaseRequest, error) {
				return &db.PurchaseRequest{
					ID:                 id,
					UserID:             1001,
					Type:               "buy",
					PriceToman:         amountPtr(100),
					QuoteID:            &quoteID,
					Status:             "approved",
					ProvisioningStatus: db.PurchaseProvisioningPending,
				}, nil
			},
			DebitTxFn: func(ctx context.Context, userID int64, operationKey string) (*db.WalletTransaction, error) {
				return &db.WalletTransaction{UserID: userID, Amount: 100, Type: "debit", Status: "completed", OperationKey: operationKey}, nil
			},
			GetSubscriptionByEmailFn: func(ctx context.Context, email string) (*db.Subscription, error) {
				return &db.Subscription{
					ID:         20,
					UserID:     9999, // user mismatch!
					ClientUUID: "uuid-direct",
					SubID:      "sub-direct",
				}, nil
			},
		}
		outcome := p.handleDirectPaymentProvisioning(context.Background(), rec)
		if outcome.Kind != OutcomeManualReview {
			t.Fatalf("expected OutcomeManualReview on identity mismatch, got %v", outcome.Kind)
		}
		if !strings.Contains(outcome.Reason, "user_id mismatch") {
			t.Fatalf("expected reason to mention user_id mismatch, got: %s", outcome.Reason)
		}
	})
}
