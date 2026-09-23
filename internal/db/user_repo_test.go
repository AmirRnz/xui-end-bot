package db

import (
	"fmt"
	"testing"
	"time"
)

func TestCreateUserClaimsOnlyAnUnambiguousPlaceholder(t *testing.T) {
	ctx := setupTestDB(t)
	username := fmt.Sprintf("claim_%d", time.Now().UnixNano())
	telegramID := time.Now().UnixNano()
	if telegramID <= 0 {
		t.Fatal("expected a positive test Telegram ID")
	}
	var placeholderID int64
	if err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name, status)
		VALUES ($1, $2, 'Reserved', 'approved') RETURNING id
	`, -telegramID, username).Scan(&placeholderID); err != nil {
		t.Fatalf("create placeholder user: %v", err)
	}
	defer func() { _, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, placeholderID) }()

	user := &User{TelegramID: telegramID, Username: "@" + username, FirstName: "Real", Language: "fa"}
	if err := CreateUser(ctx, user); err != nil {
		t.Fatalf("claim placeholder: %v", err)
	}
	if user.ID != placeholderID || user.TelegramID != telegramID || user.Username != username || user.FirstName != "Real" {
		t.Fatalf("claimed user mismatch: got %+v, want placeholder id %d and Telegram ID %d", user, placeholderID, telegramID)
	}
	if user.Status != UserStatusApproved {
		t.Fatalf("claim changed placeholder status: got %q", user.Status)
	}

	ambiguousUsername := fmt.Sprintf("ambiguous_%d", time.Now().UnixNano())
	placeholderIDs := []int64{-telegramID - 1, -telegramID - 2}
	for _, id := range placeholderIDs {
		if _, err := Pool.Exec(ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'approved')`, id, ambiguousUsername); err != nil {
			t.Fatalf("create ambiguous placeholder %d: %v", id, err)
		}
	}
	defer func() {
		_, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE username = $1 AND telegram_id < 0`, ambiguousUsername)
	}()

	ambiguous := &User{TelegramID: telegramID + 1, Username: ambiguousUsername}
	if err := CreateUser(ctx, ambiguous); err == nil {
		t.Fatal("expected ambiguous placeholders to be rejected")
	}
	if _, err := GetUserByUsername(ctx, ambiguousUsername); err == nil {
		t.Fatal("expected username lookup to reject ambiguous placeholder records")
	}
	var remaining int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM bot_users WHERE username = $1 AND telegram_id < 0`, ambiguousUsername).Scan(&remaining); err != nil {
		t.Fatalf("count remaining placeholders: %v", err)
	}
	if remaining != len(placeholderIDs) {
		t.Fatalf("ambiguous claim changed placeholder rows: got %d, want %d", remaining, len(placeholderIDs))
	}
}
