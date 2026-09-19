package handlers

import (
	"errors"
	"testing"
	"time"

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
