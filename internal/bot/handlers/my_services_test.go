package handlers

import (
	"testing"

	"xui-end-bot/internal/db"
)

func TestReconcileServicesPagePreservesSubscriptionMissingFromXUI(t *testing.T) {
	sub := &db.Subscription{
		ID:          42,
		ClientEmail: "drift@example.com",
		SubID:       "missing-from-xui",
		PlanType:    db.PlanTypePaid,
		IsActive:    true,
	}

	got := reconcileServicesPageSubscriptions([]*db.Subscription{sub}, nil)
	if len(got) != 1 || got[0] != sub {
		t.Fatalf("missing remote client must preserve the local subscription row: got %#v", got)
	}
}
