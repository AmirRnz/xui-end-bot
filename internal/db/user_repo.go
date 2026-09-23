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
	if Pool == nil {
		return errors.New("database pool is not initialized")
	}

	if u.Language == "" {
		u.Language = "en"
	}
	if u.Status == "" {
		u.Status = UserStatusPending
	}

	cleanUsername := strings.TrimPrefix(strings.TrimSpace(u.Username), "@")

	// Claim an explicitly pre-created admin placeholder by username. Lock and
	// update it in one transaction so a failed update cannot return a user that
	// still has a negative Telegram ID, and ambiguous usernames are never claimed.
	if cleanUsername != "" && u.TelegramID > 0 {
		var existingID int64
		existingErr := Pool.QueryRow(ctx, `SELECT id FROM bot_users WHERE telegram_id = $1`, u.TelegramID).Scan(&existingID)
		if existingErr != nil && !errors.Is(existingErr, pgx.ErrNoRows) {
			return existingErr
		}
		if errors.Is(existingErr, pgx.ErrNoRows) {
			tx, err := Pool.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(LOWER($1))::bigint)`, cleanUsername); err != nil {
				return err
			}

			rows, err := tx.Query(ctx, `
				SELECT id FROM bot_users
				WHERE LOWER(username) = LOWER($1) AND telegram_id < 0
				ORDER BY id
				LIMIT 2
				FOR UPDATE
			`, cleanUsername)
			if err != nil {
				return err
			}
			var placeholderIDs []int64
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				placeholderIDs = append(placeholderIDs, id)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()

			switch len(placeholderIDs) {
			case 0:
				if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
					return err
				}
			case 1:
				updated := &User{}
				err := tx.QueryRow(ctx, `
					UPDATE bot_users
					SET telegram_id = $1, username = $2, first_name = $3, last_name = $4, updated_at = NOW()
					WHERE id = $5 AND telegram_id < 0
					RETURNING id, telegram_id, username, first_name, last_name, language, status, service_name, wallet_balance, created_at, updated_at
				`, u.TelegramID, cleanUsername, u.FirstName, u.LastName, placeholderIDs[0]).Scan(
					&updated.ID, &updated.TelegramID, &updated.Username, &updated.FirstName, &updated.LastName,
					&updated.Language, &updated.Status, &updated.ServiceName, &updated.WalletBalance, &updated.CreatedAt, &updated.UpdatedAt,
				)
				if err != nil {
					return err
				}
				if err := tx.Commit(ctx); err != nil {
					return err
				}
				*u = *updated
				return nil
			default:
				return errors.New("multiple admin placeholders match this Telegram username")
			}
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

	if Pool == nil {
		return nil, errors.New("database pool is not initialized")
	}
	query := `
		SELECT id, telegram_id, username, first_name, last_name, language, status, service_name, wallet_balance, created_at, updated_at
		FROM bot_users WHERE LOWER(username) = LOWER($1)
		ORDER BY id
		LIMIT 2
	`
	rows, err := Pool.Query(ctx, query, cleanUsername)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var user *User
	for rows.Next() {
		candidate := &User{}
		if err := rows.Scan(&candidate.ID, &candidate.TelegramID, &candidate.Username, &candidate.FirstName, &candidate.LastName, &candidate.Language, &candidate.Status, &candidate.ServiceName, &candidate.WalletBalance, &candidate.CreatedAt, &candidate.UpdatedAt); err != nil {
			return nil, err
		}
		if user != nil {
			return nil, errors.New("multiple users match this Telegram username")
		}
		user = candidate
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return user, nil
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
