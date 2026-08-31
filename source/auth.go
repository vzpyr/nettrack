package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"time"
)

const sessionCookieName = "nettrack_session"

type AuthHandler struct {
	config *Config
	db     *DB
}

func NewAuthHandler(config *Config, db *DB) *AuthHandler {
	return &AuthHandler{
		config: config,
		db:     db,
	}
}

func (a *AuthHandler) generateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func (a *AuthHandler) Login(w http.ResponseWriter, r *http.Request, password string) bool {
	if subtle.ConstantTimeCompare([]byte(password), []byte(a.config.Password)) != 1 {
		return false
	}

	token, err := a.generateToken()
	if err != nil {
		return false
	}

	if err := a.db.CreateSession(token); err != nil {
		return false
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(30 * 24 * time.Hour),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	return true
}

func (a *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && cookie != nil {
		a.db.DeleteSession(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *AuthHandler) IsAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie == nil || cookie.Value == "" {
		return false
	}
	return a.db.VerifySession(cookie.Value)
}

func (a *AuthHandler) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.IsAuthenticated(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
