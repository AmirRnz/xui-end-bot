package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func GetSubscriptionsByUserID(ctx context.Context, userID int64) ([]*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, subscriptionSelect()+` WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*Subscription
	for rows.Next() {
		s, err := scanSubscriptionRows(rows)
		if err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

// GetManageableSubscriptionsByUserID returns services that are still current
// enough to manage. Cancelled/deleted rows remain queryable through the
// audit-oriented functions so history survives, but terminal rows must not
// appear in the normal My Services screen.
func GetManageableSubscriptionsByUserID(ctx context.Context, userID int64) ([]*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, subscriptionSelect()+` WHERE user_id = $1 AND status NOT IN ('cancelled', 'deleted') ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*Subscription
	for rows.Next() {
		s, err := scanSubscriptionRows(rows)
		if err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

func GetActiveSubscriptionsByUserID(ctx context.Context, userID int64) ([]*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, subscriptionSelect()+` WHERE user_id = $1 AND is_active = TRUE ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*Subscription
	for rows.Next() {
		s, err := scanSubscriptionRows(rows)
		if err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

func GetSubscriptionByID(ctx context.Context, id int) (*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return nil, errors.New("database pool is not initialized")
	}

	row := Pool.QueryRow(ctx, subscriptionSelect()+` WHERE id = $1`, id)
	s, err := scanSubscriptionRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func GetSubscriptionByEmail(ctx context.Context, email string) (*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return nil, errors.New("database pool is not initialized")
	}

	row := Pool.QueryRow(ctx, subscriptionSelect()+` WHERE client_email = $1`, email)
	s, err := scanSubscriptionRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func CreateSubscription(ctx context.Context, s *Subscription) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}

	if s.Status == "" {
		s.Status = SubscriptionStatusActive
	}
	if !IsValidSubscriptionStatus(s.Status) {
		return fmt.Errorf("invalid subscription status: %q", s.Status)
	}
	if err := validateSubscriptionLifecycle(s.Status, s.IsActive); err != nil {
		return err
	}
	if s.DisplayName == "" {
		s.DisplayName = s.ClientEmail
	}
	if s.Status == "" {
		s.Status = SubscriptionStatusActive
	}
	if s.StartDate.IsZero() {
		s.StartDate = time.Now().UTC()
	}
	if s.EndDate.IsZero() && s.ExpireTime != nil && *s.ExpireTime > 0 {
		s.EndDate = time.UnixMilli(*s.ExpireTime)
	}
	var endDate *time.Time
	if !s.EndDate.IsZero() {
		endDate = &s.EndDate
	}

	query := `
		INSERT INTO subscriptions (user_id, plan_id, quote_id, client_email, client_uuid, sub_id, status, plan_type, display_name, ip_limit, expire_time, is_active, start_date, end_date, traffic_limit_bytes, desired_ip_limit, desired_expire_time, desired_is_active, reconciliation_note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		RETURNING id, created_at, updated_at
	`
	return Pool.QueryRow(ctx, query,
		s.UserID, s.PlanID, s.QuoteID, s.ClientEmail, s.ClientUUID, s.SubID, s.Status, s.PlanType, s.DisplayName, s.IPLimit, s.ExpireTime, s.IsActive, s.StartDate, endDate, s.TrafficLimitBytes, s.DesiredIPLimit, s.DesiredExpireTime, s.DesiredIsActive, s.ReconciliationNote,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
}

func UpdateSubscriptionStatus(ctx context.Context, id int, status string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}

	if !IsValidSubscriptionStatus(status) {
		return fmt.Errorf("invalid subscription status: %q", status)
	}

	_, err := Pool.Exec(ctx, `
		UPDATE subscriptions SET status = $1,
			is_active = CASE
				WHEN $1 IN ('active', 'cancellation_requested', 'deprovisioning') THEN TRUE
				WHEN $1 IN ('disabled', 'expired', 'cancelled', 'deleted') THEN FALSE
				ELSE is_active END,
			updated_at = NOW()
		WHERE id = $2
	`, status, id)
	return err
}

func UpdateSubscription(ctx context.Context, s *Subscription) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}

	if !IsValidSubscriptionStatus(s.Status) {
		return fmt.Errorf("invalid subscription status: %q", s.Status)
	}
	if err := validateSubscriptionLifecycle(s.Status, s.IsActive); err != nil {
		return err
	}

	if s.EndDate.IsZero() && s.ExpireTime != nil && *s.ExpireTime > 0 {
		s.EndDate = time.UnixMilli(*s.ExpireTime)
	}
	var endDate *time.Time
	if !s.EndDate.IsZero() {
		endDate = &s.EndDate
	}
	_, err := Pool.Exec(ctx, `
		UPDATE subscriptions
		SET plan_id = $1, client_email = $2, client_uuid = $3, sub_id = $4, status = $5, plan_type = $6,
			display_name = $7, ip_limit = $8, expire_time = $9, is_active = $10, end_date = $11, traffic_limit_bytes = $12,
			desired_ip_limit = $13, desired_expire_time = $14, desired_is_active = $15, reconciliation_note = $16, updated_at = NOW()
		WHERE id = $17
	`, s.PlanID, s.ClientEmail, s.ClientUUID, s.SubID, s.Status, s.PlanType, s.DisplayName, s.IPLimit, s.ExpireTime, s.IsActive, endDate, s.TrafficLimitBytes, s.DesiredIPLimit, s.DesiredExpireTime, s.DesiredIsActive, s.ReconciliationNote, s.ID)
	return err
}

func MarkSubscriptionReconciliationRequired(ctx context.Context, id int, desiredIPLimit *int, desiredExpireTime *int64, desiredIsActive *bool, note string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}

	_, err := Pool.Exec(ctx, `
		UPDATE subscriptions
		SET status = $1, desired_ip_limit = $2, desired_expire_time = $3,
			desired_is_active = $4, reconciliation_note = $5, updated_at = NOW()
		WHERE id = $6
	`, SubscriptionStatusReconciliation, desiredIPLimit, desiredExpireTime, desiredIsActive, note, id)
	return err
}

func DeleteSubscription(ctx context.Context, id int) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}

	_, err := Pool.Exec(ctx, `UPDATE subscriptions SET status = 'deleted', is_active = FALSE, end_date = COALESCE(end_date, NOW()), updated_at = NOW() WHERE id = $1`, id)
	return err
}

func CountTestSubscriptionsForUser(ctx context.Context, userID int64) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var count int
	err := Pool.QueryRow(ctx, `SELECT count(*) FROM subscriptions WHERE user_id = $1 AND plan_type = 'test'`, userID).Scan(&count)
	return count, err
}

func GetTestUsage(ctx context.Context, userID int64, planID int64) (time.Time, bool, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var updatedAt time.Time
	err := Pool.QueryRow(ctx, `
		SELECT updated_at FROM test_usage
		WHERE user_id = $1 AND plan_id = $2 AND reset_date = '2000-01-01'::DATE
	`, userID, planID).Scan(&updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	return updatedAt, true, nil
}

var ErrTestUsageLimitReached = errors.New("test subscription limit reached")

// ReserveTestUsage atomically records a test claim before remote provisioning.
// A zero/non-positive reset period allows exactly one claim for the user/plan.
func ReserveTestUsage(ctx context.Context, userID int64, planID int64, resetDays int) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var usedCount int
	err := Pool.QueryRow(ctx, `
		INSERT INTO test_usage (user_id, plan_id, used_count, reset_date)
		VALUES ($1, $2, 1, '2000-01-01'::DATE)
		ON CONFLICT (user_id, plan_id, reset_date)
		DO UPDATE SET used_count = test_usage.used_count + 1, updated_at = NOW()
		WHERE $3 > 0
		  AND test_usage.updated_at <= NOW() - ($3 * INTERVAL '1 day')
		RETURNING used_count
	`, userID, planID, resetDays).Scan(&usedCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTestUsageLimitReached
	}
	return err
}

// ReleaseTestUsage removes a reservation only when provisioning is known not
// to have created a remote client.
func ReleaseTestUsage(ctx context.Context, userID int64, planID int64) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	_, err := Pool.Exec(ctx, `
		DELETE FROM test_usage
		WHERE user_id = $1 AND plan_id = $2 AND reset_date = '2000-01-01'::DATE
	`, userID, planID)
	return err
}

func IncrementTestUsage(ctx context.Context, userID int64, planID int64, increment int) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if increment <= 0 {
		increment = 1
	}
	_, err := Pool.Exec(ctx, `
		INSERT INTO test_usage (user_id, plan_id, used_count, reset_date)
		VALUES ($1, $2, $3, '2000-01-01'::DATE)
		ON CONFLICT (user_id, plan_id, reset_date)
		DO UPDATE SET used_count = test_usage.used_count + EXCLUDED.used_count, updated_at = NOW()
	`, userID, planID, increment)
	return err
}

func GetActiveSubscriptionsCount(ctx context.Context) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var count int
	err := Pool.QueryRow(ctx, "SELECT count(*) FROM subscriptions WHERE is_active = TRUE").Scan(&count)
	return count, err
}

func GetExpiringSubscriptions(ctx context.Context, days int) ([]*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	targetDay := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	startOfDay := time.Date(targetDay.Year(), targetDay.Month(), targetDay.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.Add(24 * time.Hour)

	rows, err := Pool.Query(ctx, subscriptionSelect()+`
		WHERE is_active = TRUE
		  AND end_date IS NOT NULL
		  AND end_date >= $1 
		  AND end_date < $2
	`, startOfDay, endOfDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*Subscription
	for rows.Next() {
		s, err := scanSubscriptionRows(rows)
		if err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

func subscriptionSelect() string {
	return `SELECT id, user_id, plan_id, quote_id, client_email, client_uuid, sub_id, status, plan_type, display_name, ip_limit, expire_time, is_active, start_date, end_date, traffic_limit_bytes, desired_ip_limit, desired_expire_time, desired_is_active, reconciliation_note, created_at, updated_at FROM subscriptions`
}

func scanSubscriptionRows(rows pgx.Rows) (*Subscription, error) {
	s := &Subscription{}
	var endDate *time.Time
	err := rows.Scan(&s.ID, &s.UserID, &s.PlanID, &s.QuoteID, &s.ClientEmail, &s.ClientUUID, &s.SubID, &s.Status, &s.PlanType, &s.DisplayName, &s.IPLimit, &s.ExpireTime, &s.IsActive, &s.StartDate, &endDate, &s.TrafficLimitBytes, &s.DesiredIPLimit, &s.DesiredExpireTime, &s.DesiredIsActive, &s.ReconciliationNote, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if endDate != nil {
		s.EndDate = *endDate
	} else if s.ExpireTime != nil && *s.ExpireTime > 0 {
		s.EndDate = time.UnixMilli(*s.ExpireTime)
	}
	return s, nil
}

// GetTestsCreatedToday returns the number of test subscriptions created today (UTC).
func GetTestsCreatedToday(ctx context.Context) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var count int
	err := Pool.QueryRow(ctx, `
		SELECT count(*) FROM subscriptions
		WHERE plan_type = 'test'
		  AND created_at >= (NOW() AT TIME ZONE 'UTC')::DATE
	`).Scan(&count)
	return count, err
}

// GetMonthlyRevenue returns the sum of all debit transactions this calendar month (UTC).
func GetMonthlyRevenue(ctx context.Context) (float64, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var total *float64
	err := Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(ABS(amount)), 0)
		FROM transactions
		WHERE type = 'debit'
		  AND status = 'completed'
		  AND created_at >= DATE_TRUNC('month', NOW() AT TIME ZONE 'UTC')
	`).Scan(&total)
	if err != nil {
		return 0, err
	}
	if total == nil {
		return 0, nil
	}
	return *total, nil
}

func GetActiveSubscriptionsByPlan(ctx context.Context, planType string, planID int64) ([]*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, subscriptionSelect()+` WHERE plan_type = $1 AND plan_id = $2 AND is_active = TRUE`, planType, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*Subscription
	for rows.Next() {
		s, err := scanSubscriptionRows(rows)
		if err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

func scanSubscriptionRow(row pgx.Row) (*Subscription, error) {
	s := &Subscription{}
	var endDate *time.Time
	err := row.Scan(&s.ID, &s.UserID, &s.PlanID, &s.QuoteID, &s.ClientEmail, &s.ClientUUID, &s.SubID, &s.Status, &s.PlanType, &s.DisplayName, &s.IPLimit, &s.ExpireTime, &s.IsActive, &s.StartDate, &endDate, &s.TrafficLimitBytes, &s.DesiredIPLimit, &s.DesiredExpireTime, &s.DesiredIsActive, &s.ReconciliationNote, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if endDate != nil {
		s.EndDate = *endDate
	} else if s.ExpireTime != nil && *s.ExpireTime > 0 {
		s.EndDate = time.UnixMilli(*s.ExpireTime)
	}
	return s, nil
}

func GetSubscriptionBySubID(ctx context.Context, subID string) (*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	row := Pool.QueryRow(ctx, subscriptionSelect()+` WHERE sub_id = $1`, subID)
	s, err := scanSubscriptionRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func GetActiveSubscriptions(ctx context.Context) ([]*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, subscriptionSelect()+` WHERE is_active = TRUE ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*Subscription
	for rows.Next() {
		s, err := scanSubscriptionRows(rows)
		if err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}
