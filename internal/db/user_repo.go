package db

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

func CreateUser(ctx context.Context, u *User) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if u.Language == "" {
		u.Language = "en"
	}
	if u.Status == "" {
		u.Status = UserStatusPending
	}

	cleanUsername := strings.TrimPrefix(strings.TrimSpace(u.Username), "@")

	// If there is an existing placeholder user record created by admin via username (telegram_id < 0), update it with real telegram_id
	if cleanUsername != "" && u.TelegramID > 0 {
		var placeholderID int64
		err := Pool.QueryRow(ctx, `SELECT id FROM bot_users WHERE LOWER(username) = LOWER($1) AND telegram_id < 0 LIMIT 1`, cleanUsername).Scan(&placeholderID)
		if err == nil && placeholderID > 0 {
			_, _ = Pool.Exec(ctx, `UPDATE bot_users SET telegram_id = $1, username = $2, first_name = $3, last_name = $4, updated_at = NOW() WHERE id = $5`,
				u.TelegramID, cleanUsername, u.FirstName, u.LastName, placeholderID)
			uFound, err := GetUserByID(ctx, placeholderID)
			if err == nil && uFound != nil {
				*u = *uFound
			}
			return nil
		}
	}

	query := `
		INSERT INTO bot_users (telegram_id, username, first_name, last_name, language, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (telegram_id) DO UPDATE SET
			username = EXCLUDED.username,
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			updated_at = NOW()
		RETURNING id, telegram_id, username, first_name, last_name, language, status, service_name, wallet_balance, created_at, updated_at
	`
	return Pool.QueryRow(ctx, query,
		u.TelegramID, cleanUsername, u.FirstName, u.LastName, u.Language, u.Status,
	).Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName, &u.LastName, &u.Language, &u.Status, &u.ServiceName, &u.WalletBalance, &u.CreatedAt, &u.UpdatedAt)
}

func GetUserByUsername(ctx context.Context, username string) (*User, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	cleanUsername := strings.TrimPrefix(strings.TrimSpace(username), "@")
	if cleanUsername == "" {
		return nil, nil
	}

	query := `SELECT id, telegram_id, username, first_name, last_name, language, status, service_name, wallet_balance, created_at, updated_at FROM bot_users WHERE LOWER(username) = LOWER($1)`
	return scanUser(Pool.QueryRow(ctx, query, cleanUsername))
}

func GetUserByTelegramID(ctx context.Context, telegramID int64) (*User, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	query := `SELECT id, telegram_id, username, first_name, last_name, language, status, service_name, wallet_balance, created_at, updated_at FROM bot_users WHERE telegram_id = $1`
	return scanUser(Pool.QueryRow(ctx, query, telegramID))
}

func GetUserByID(ctx context.Context, id int64) (*User, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	query := `SELECT id, telegram_id, username, first_name, last_name, language, status, service_name, wallet_balance, created_at, updated_at FROM bot_users WHERE id = $1`
	return scanUser(Pool.QueryRow(ctx, query, id))
}

func ListUsers(ctx context.Context, limit, offset int) ([]*User, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := Pool.Query(ctx, `
		SELECT id, telegram_id, username, first_name, last_name, language, status, service_name, wallet_balance, created_at, updated_at
		FROM bot_users
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName, &u.LastName, &u.Language, &u.Status, &u.ServiceName, &u.WalletBalance, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func ListApprovedUsers(ctx context.Context) ([]*User, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, `
		SELECT id, telegram_id, username, first_name, last_name, language, status, service_name, wallet_balance, created_at, updated_at
		FROM bot_users
		WHERE status IN ('approved', 'active')
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName, &u.LastName, &u.Language, &u.Status, &u.ServiceName, &u.WalletBalance, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func UpdateUserStatus(ctx context.Context, telegramID int64, status string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	_, err := Pool.Exec(ctx, `UPDATE bot_users SET status = $1, updated_at = NOW() WHERE telegram_id = $2`, status, telegramID)
	return err
}

func UpdateUserStatusByID(ctx context.Context, id int64, status string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	_, err := Pool.Exec(ctx, `UPDATE bot_users SET status = $1, updated_at = NOW() WHERE id = $2`, status, id)
	return err
}

func UpdateUserServiceName(ctx context.Context, telegramID int64, serviceName string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return errors.New("service name cannot be empty")
	}
	_, err := Pool.Exec(ctx, `UPDATE bot_users SET service_name = $1, updated_at = NOW() WHERE telegram_id = $2`, serviceName, telegramID)
	return err
}

func UpdateUserLanguage(ctx context.Context, telegramID int64, language string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if language == "" {
		language = "en"
	}
	_, err := Pool.Exec(ctx, `UPDATE bot_users SET language = $1, updated_at = NOW() WHERE telegram_id = $2`, language, telegramID)
	return err
}

func GetAllUsersCount(ctx context.Context) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var count int
	err := Pool.QueryRow(ctx, "SELECT count(*) FROM bot_users").Scan(&count)
	return count, err
}

func GetApprovedUsersCount(ctx context.Context) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var count int
	err := Pool.QueryRow(ctx, "SELECT count(*) FROM bot_users WHERE status IN ('approved', 'active')").Scan(&count)
	return count, err
}

func scanUser(row pgx.Row) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName, &u.LastName, &u.Language, &u.Status, &u.ServiceName, &u.WalletBalance, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return u, nil
}
