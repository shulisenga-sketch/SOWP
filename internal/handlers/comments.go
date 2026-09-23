package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"sowp/internal/auth"
	"sowp/internal/notifications"
)

type CommentHandler struct {
	db       *sql.DB
	sessions *auth.SessionStore
}

func NewCommentHandler(db *sql.DB, sessions *auth.SessionStore) *CommentHandler {
	return &CommentHandler{db: db, sessions: sessions}
}

type commentResponse struct {
	ID         int64  `json:"id"`
	AuthorID   int64  `json:"author_id"`
	AuthorName string `json:"author_name"`
	Body       string `json:"body"`
	CreatedAt  string `json:"created_at"`
}

func (handler *CommentHandler) List(writer http.ResponseWriter, request *http.Request) {
	role, userID, authenticated := handler.sessions.Role(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}
	orderID, ok := parseOrderID(writer, request)
	if !ok {
		return
	}
	if !handler.canAccessOrder(request, orderID, userID, role) {
		writeError(writer, http.StatusNotFound, "order not found")
		return
	}

	rows, err := handler.db.QueryContext(request.Context(), `
		SELECT comments.id, comments.author_id, users.full_name, comments.body, comments.created_at
		FROM comments
		JOIN users ON users.id = comments.author_id
		WHERE comments.order_id = ?
		ORDER BY comments.created_at ASC, comments.id ASC`, orderID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not load comments")
		return
	}
	defer rows.Close()

	comments := make([]commentResponse, 0)
	for rows.Next() {
		var comment commentResponse
		if err := rows.Scan(&comment.ID, &comment.AuthorID, &comment.AuthorName, &comment.Body, &comment.CreatedAt); err != nil {
			writeError(writer, http.StatusInternalServerError, "could not read comments")
			return
		}
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not read comments")
		return
	}
	writeJSON(writer, http.StatusOK, comments)
}

func (handler *CommentHandler) Create(writer http.ResponseWriter, request *http.Request) {
	role, userID, authenticated := handler.sessions.Role(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}
	orderID, ok := parseOrderID(writer, request)
	if !ok {
		return
	}

	var customerID int64
	if err := handler.db.QueryRowContext(request.Context(),
		`SELECT customer_id FROM orders WHERE id = ?`, orderID).Scan(&customerID); err != nil {
		if err == sql.ErrNoRows {
			writeError(writer, http.StatusNotFound, "order not found")
			return
		}
		writeError(writer, http.StatusInternalServerError, "could not validate order")
		return
	}
	if role != "admin" && customerID != userID {
		writeError(writer, http.StatusNotFound, "order not found")
		return
	}

	var input struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 64*1024)).Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input.Body = strings.TrimSpace(input.Body)
	if input.Body == "" || len(input.Body) > 10000 {
		writeError(writer, http.StatusBadRequest, "body is required and must be at most 10000 characters")
		return
	}

	tx, err := handler.db.BeginTx(request.Context(), nil)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not create comment")
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(request.Context(),
		`INSERT INTO comments (order_id, author_id, body) VALUES (?, ?, ?)`, orderID, userID, input.Body)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not save comment")
		return
	}
	commentID, _ := result.LastInsertId()

	if role == "admin" {
		if err := notifications.Create(request.Context(), tx, customerID, orderID, "comment", "New message on your order", input.Body); err != nil {
			writeError(writer, http.StatusInternalServerError, "could not create notification")
			return
		}
	} else {
		rows, err := tx.QueryContext(request.Context(), `SELECT id FROM users WHERE role = 'admin' AND is_active = 1`)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "could not find admin recipients")
			return
		}
		adminIDs := make([]int64, 0)
		for rows.Next() {
			var adminID int64
			if err := rows.Scan(&adminID); err != nil {
				rows.Close()
				writeError(writer, http.StatusInternalServerError, "could not read admin recipients")
				return
			}
			adminIDs = append(adminIDs, adminID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			writeError(writer, http.StatusInternalServerError, "could not read admin recipients")
			return
		}
		rows.Close()
		for _, adminID := range adminIDs {
			if err := notifications.Create(request.Context(), tx, adminID, orderID, "comment", "New customer message", input.Body); err != nil {
				writeError(writer, http.StatusInternalServerError, "could not create notification")
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not create comment")
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"id": commentID, "body": input.Body})
}

func (handler *CommentHandler) canAccessOrder(request *http.Request, orderID, userID int64, role string) bool {
	if role == "admin" {
		var exists bool
		return handler.db.QueryRowContext(request.Context(), `SELECT EXISTS (SELECT 1 FROM orders WHERE id = ?)`, orderID).Scan(&exists) == nil && exists
	}
	var exists bool
	return handler.db.QueryRowContext(request.Context(), `SELECT EXISTS (SELECT 1 FROM orders WHERE id = ? AND customer_id = ?)`, orderID, userID).Scan(&exists) == nil && exists
}
