package handlers

import (
	"time"

	"xui-end-bot/internal/db"
	"xui-end-bot/internal/xui"
)

type walletRemoteOutcome string

const (
	walletRemoteSucceeded  walletRemoteOutcome = "succeeded"
	walletRemoteUnknown    walletRemoteOutcome = "unknown"
	walletRemoteDefinitive walletRemoteOutcome = "definitive_failure"
)

func classifyWalletRemoteOutcome(err error) walletRemoteOutcome {
	if err == nil {
		return walletRemoteSucceeded
	}
	if xui.IsUnknownOutcome(err) {
		return walletRemoteUnknown
	}
	return walletRemoteDefinitive
}

func walletRemoteRefundAllowed(err error) bool {
	return classifyWalletRemoteOutcome(err) == walletRemoteDefinitive
}

func walletRemoteReconciliationRequired(err error) bool {
	return classifyWalletRemoteOutcome(err) == walletRemoteUnknown
}

// A local write failure after a confirmed remote write is always a
// reconciliation case. The remote state may already be changed, so refunding
// here would make the operation free even though the panel accepted it.
func walletLocalWriteReconciliationRequired(err error) bool {
	return err != nil
}

type subscriptionWalletState struct {
	ipLimit    int
	endDate    time.Time
	expireTime *int64
	isActive   bool
}

func snapshotSubscriptionWalletState(sub *db.Subscription) subscriptionWalletState {
	state := subscriptionWalletState{}
	if sub == nil {
		return state
	}
	state.ipLimit = sub.IPLimit
	state.endDate = sub.EndDate
	state.isActive = sub.IsActive
	if sub.ExpireTime != nil {
		expiry := *sub.ExpireTime
		state.expireTime = &expiry
	}
	return state
}

func restoreSubscriptionWalletState(sub *db.Subscription, state subscriptionWalletState) {
	if sub == nil {
		return
	}
	sub.IPLimit = state.ipLimit
	sub.EndDate = state.endDate
	sub.IsActive = state.isActive
	if state.expireTime == nil {
		sub.ExpireTime = nil
		return
	}
	expiry := *state.expireTime
	sub.ExpireTime = &expiry
}
