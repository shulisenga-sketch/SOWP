package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"
)

const sessionCookieName = "sowp_session"

type SessionStore struct {
	db     *sql.DB
	secure bool
}

func NewSessionStore(db *sql.DB, secureCookies bool) *SessionStore {
	return &SessionStore{db: db, secure: secureCookies}
}

func (store *SessionStore) Create(ctx context.Context, userID int64) (string, time.Time, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", time.Time{}, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, expires_at) VALUES (?, ?, ?)`,
		token, userID, expiresAt,
	); err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}
	return token, expiresAt, nil
}

func (store *SessionStore) SetCookie(writer http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   store.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (store *SessionStore) UserID(ctx context.Context, request *http.Request) (int64, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return 0, false
	}

	var userID int64
	err = store.db.QueryRowContext(ctx, `
		SELECT sessions.user_id
		FROM sessions
		JOIN users ON users.id = sessions.user_id
		WHERE sessions.id = ?
		  AND sessions.expires_at > CURRENT_TIMESTAMP
		  AND users.is_active = 1`, cookie.Value).Scan(&userID)
	if err != nil {
		return 0, false
	}
	return userID, true
}

func (store *SessionStore) Role(ctx context.Context, request *http.Request) (string, int64, bool) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return "", 0, false
	}

	var role string
	var userID int64
	err = store.db.QueryRowContext(ctx, `
		SELECT sessions.user_id, users.role
		FROM sessions
		JOIN users ON users.id = sessions.user_id
		WHERE sessions.id = ?
		  AND sessions.expires_at > CURRENT_TIMESTAMP
		  AND users.is_active = 1`, cookie.Value).Scan(&userID, &role)
	if err != nil {
		return "", 0, false
	}
	return role, userID, true
}

func (store *SessionStore) Delete(ctx context.Context, request *http.Request, writer http.ResponseWriter) {
	if cookie, err := request.Cookie(sessionCookieName); err == nil {
		_, _ = store.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, cookie.Value)
	}
	http.SetCookie(writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   store.secure,
		SameSite: http.SameSiteLaxMode,
	})
}
