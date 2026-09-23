package handlers

import (
	"database/sql"
	"net/http"
	"strconv"

	"sowp/internal/auth"
)

type NotificationHandler struct {
	db       *sql.DB
	sessions *auth.SessionStore
}

func NewNotificationHandler(db *sql.DB, sessions *auth.SessionStore) *NotificationHandler {
	return &NotificationHandler{db: db, sessions: sessions}
}

type notificationResponse struct {
	ID        int64  `json:"id"`
	OrderID   *int64 `json:"order_id,omitempty"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	IsRead    bool   `json:"is_read"`
	CreatedAt string `json:"created_at"`
}

func (handler *NotificationHandler) List(writer http.ResponseWriter, request *http.Request) {
	userID, authenticated := handler.sessions.UserID(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}
	rows, err := handler.db.QueryContext(request.Context(), `
		SELECT id, order_id, type, title, body, is_read, created_at
		FROM notifications
		WHERE user_id = ?
		ORDER BY created_at DESC, id DESC
		LIMIT 100`, userID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not load notifications")
		return
	}
	defer rows.Close()

	notifications := make([]notificationResponse, 0)
	for rows.Next() {
		var item notificationResponse
		var orderID sql.NullInt64
		var isRead int
		if err := rows.Scan(&item.ID, &orderID, &item.Type, &item.Title, &item.Body, &isRead, &item.CreatedAt); err != nil {
			writeError(writer, http.StatusInternalServerError, "could not read notifications")
			return
		}
		if orderID.Valid {
			item.OrderID = &orderID.Int64
		}
		item.IsRead = isRead == 1
		notifications = append(notifications, item)
	}
	if err := rows.Err(); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not read notifications")
		return
	}
	writeJSON(writer, http.StatusOK, notifications)
}

func (handler *NotificationHandler) MarkRead(writer http.ResponseWriter, request *http.Request) {
	userID, authenticated := handler.sessions.UserID(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}
	notificationID, err := strconv.ParseInt(request.PathValue("notificationID"), 10, 64)
	if err != nil || notificationID < 1 {
		writeError(writer, http.StatusBadRequest, "invalid notification id")
		return
	}
	result, err := handler.db.ExecContext(request.Context(), `
		UPDATE notifications SET is_read = 1 WHERE id = ? AND user_id = ?`, notificationID, userID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not update notification")
		return
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		writeError(writer, http.StatusNotFound, "notification not found")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"id": notificationID, "is_read": true})
}
