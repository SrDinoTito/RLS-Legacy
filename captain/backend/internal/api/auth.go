package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/rls-legacy/captain/backend/internal/auth"
	"github.com/rls-legacy/captain/backend/internal/store"
)

type ctxKey int

const (
	ctxKeyUserID ctxKey = iota
	ctxKeyRole
	ctxKeyUsername
)

// sessionCookie builds the login/bootstrap session cookie: httpOnly,
// SameSite=Lax, scoped to /api/v1, Secure only behind HTTPS (detected via
// X-Forwarded-Proto set by the reverse proxy).
func (s *Server) sessionCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/api/v1",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge:   maxAge,
	}
}

// POST /api/v1/auth/login — verifies bcrypt credentials and issues an HS256
// session JWT cookie.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	c, err := decodeCreds(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	user, err := s.store.GetUserByUsername(r.Context(), c.Username)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if auth.ComparePassword(user.PasswordHash, c.Password) != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, err := auth.MintToken(s.jwtSecret, user.ID, user.Role, sessionTTL, s.now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue session")
		return
	}
	http.SetCookie(w, s.sessionCookie(r, token, int(sessionTTL/time.Second)))
	writeJSON(w, http.StatusOK, meResponse{Username: user.Username, Role: user.Role})
}

// POST /api/v1/auth/bootstrap — open only while there are zero users; creates
// the first admin.
func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if n > 0 {
		writeError(w, http.StatusForbidden, "bootstrap already done: users exist")
		return
	}
	c, err := decodeCreds(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if c.Role == "" {
		c.Role = "admin" // first user is always admin
	}
	if msg := validateCreds(c.Username, c.Password, c.Role); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	hash, err := auth.HashPassword(c.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}
	id, err := s.store.CreateFirstAdmin(r.Context(), c.Username, hash)
	if errors.Is(err, store.ErrBootstrapComplete) {
		// The check-and-insert is atomic in the store; a concurrent request can
		// only win once, even when its username is different.
		writeError(w, http.StatusConflict, "bootstrap already done: users exist")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	// Log the user in immediately for a smooth first-run experience.
	token, err := auth.MintToken(s.jwtSecret, id, "admin", sessionTTL, s.now())
	if err == nil {
		http.SetCookie(w, s.sessionCookie(r, token, int(sessionTTL/time.Second)))
	}
	writeJSON(w, http.StatusCreated, map[string]string{"username": c.Username, "role": "admin"})
}

// POST /api/v1/auth/logout — expires the session cookie. Always 204.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// MaxAge -1 + past Expires removes the cookie client-side.
	http.SetCookie(w, s.sessionCookie(r, "", -1))
	w.WriteHeader(http.StatusNoContent)
}

// meResponse is the public representation of the authenticated user.
type meResponse struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

// GET /api/v1/auth/me — requires a valid session JWT.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(ctxKeyUserID).(int64)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	user, err := s.store.GetUserByID(r.Context(), userID)
	if err != nil {
		// Token valid but user deleted meanwhile → treat as unauthorized.
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, meResponse{Username: user.Username, Role: user.Role})
}

// requireAuth validates the session cookie and stores the identity in the
// request context.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookie)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		uid, tokenRole, err := auth.ParseToken(s.jwtSecret, cookie.Value, s.now())
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// JWTs are intentionally stateless, but account removal and role
		// changes must take effect immediately. Revalidate both against SQLite
		// on every request instead of trusting a stale 24-hour claim.
		user, err := s.store.GetUserByID(r.Context(), uid)
		if errors.Is(err, store.ErrNotFound) || (err == nil && user.Role != tokenRole) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authentication store unavailable")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyUserID, uid)
		ctx = context.WithValue(ctx, ctxKeyRole, user.Role)
		ctx = context.WithValue(ctx, ctxKeyUsername, user.Username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAdmin rejects non-admin identities with 403.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, _ := r.Context().Value(ctxKeyRole).(string)
		if role != "admin" {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// userIDFromCtx extracts the authenticated user id.
func userIDFromCtx(ctx context.Context) (int64, bool) {
	uid, ok := ctx.Value(ctxKeyUserID).(int64)
	return uid, ok
}
