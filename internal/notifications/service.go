package notifications

import (
	"context"
	"database/sql"
	"fmt"
)

func Create(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, userID, orderID int64, notificationType, title, body string) error {
	var orderValue any
	if orderID > 0 {
		orderValue = orderID
	}
	_, err := executor.ExecContext(ctx, `
		INSERT INTO notifications (user_id, order_id, type, title, body)
		VALUES (?, ?, ?, ?, ?)`, userID, orderValue, notificationType, title, body)
	if err != nil {
		return fmt.Errorf("create notification: %w", err)
	}
	return nil
}

func CreateForAdmins(ctx context.Context, db *sql.DB, orderID int64, notificationType, title, body string) error {
	rows, err := db.QueryContext(ctx, `SELECT id FROM users WHERE role = 'admin' AND is_active = 1`)
	if err != nil {
		return fmt.Errorf("find admin recipients: %w", err)
	}
	adminIDs := make([]int64, 0)
	for rows.Next() {
		var adminID int64
		if err := rows.Scan(&adminID); err != nil {
			rows.Close()
			return fmt.Errorf("read admin recipient: %w", err)
		}
		adminIDs = append(adminIDs, adminID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, adminID := range adminIDs {
		if err := Create(ctx, db, adminID, orderID, notificationType, title, body); err != nil {
			return err
		}
	}
	return nil
}
