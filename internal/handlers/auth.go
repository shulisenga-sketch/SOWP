package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"sowp/internal/auth"
)

type AuthHandler struct {
	db       *sql.DB
	sessions *auth.SessionStore
}

func NewAuthHandler(db *sql.DB, sessions *auth.SessionStore) *AuthHandler {
	return &AuthHandler{db: db, sessions: sessions}
}

type credentials struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (handler *AuthHandler) Register(writer http.ResponseWriter, request *http.Request) {
	var input credentials
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input.FullName = strings.TrimSpace(input.FullName)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if input.FullName == "" || !strings.Contains(input.Email, "@") || len(input.Password) < 8 {
		writeError(writer, http.StatusBadRequest, "full_name, valid email, and password of at least 8 characters are required")
		return
	}

	hash, err := auth.HashPassword(input.Password)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not secure password")
		return
	}
	result, err := handler.db.ExecContext(request.Context(),
		`INSERT INTO users (full_name, email, password_hash) VALUES (?, ?, ?)`,
		input.FullName, input.Email, hash,
	)
	if err != nil {
		if isDuplicateError(err) {
			writeError(writer, http.StatusConflict, "email is already registered")
			return
		}
		writeError(writer, http.StatusInternalServerError, "could not create account")
		return
	}

	userID, _ := result.LastInsertId()
	if err := handler.startSession(writer, request, userID); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not start session")
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]string{"message": "account created"})
}

func isDuplicateError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint failed") ||
		strings.Contains(message, "duplicate entry") ||
		strings.Contains(message, "error 1062")
}

func (handler *AuthHandler) Login(writer http.ResponseWriter, request *http.Request) {
	var input credentials
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid JSON body")
		return
	}

	var userID int64
	var hash string
	err := handler.db.QueryRowContext(request.Context(),
		`SELECT id, password_hash FROM users WHERE email = ? AND is_active = 1`,
		strings.ToLower(strings.TrimSpace(input.Email))).Scan(&userID, &hash)
	if errors.Is(err, sql.ErrNoRows) || !auth.CheckPassword(hash, input.Password) {
		writeError(writer, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "could not authenticate")
		return
	}

	if err := handler.startSession(writer, request, userID); err != nil {
		writeError(writer, http.StatusInternalServerError, "could not start session")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"message": "signed in"})
}

func (handler *AuthHandler) Logout(writer http.ResponseWriter, request *http.Request) {
	handler.sessions.Delete(request.Context(), request, writer)
	writeJSON(writer, http.StatusOK, map[string]string{"message": "signed out"})
}

func (handler *AuthHandler) Me(writer http.ResponseWriter, request *http.Request) {
	role, userID, authenticated := handler.sessions.Role(request.Context(), request)
	if !authenticated {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}

	var fullName, email string
	if err := handler.db.QueryRowContext(request.Context(),
		`SELECT full_name, email FROM users WHERE id = ? AND is_active = 1`, userID,
	).Scan(&fullName, &email); err != nil {
		writeError(writer, http.StatusUnauthorized, "sign in is required")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{
		"full_name": fullName,
		"email":     email,
		"role":      role,
	})
}

func (handler *AuthHandler) startSession(writer http.ResponseWriter, request *http.Request, userID int64) error {
	token, expiresAt, err := handler.sessions.Create(request.Context(), userID)
	if err != nil {
		return err
	}
	handler.sessions.SetCookie(writer, token, expiresAt)
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}
