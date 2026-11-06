package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/rls-legacy/captain/backend/internal/auth"
	"github.com/rls-legacy/captain/backend/internal/store"
)

// userDTO is the JSON projection of a user (never exposes password_hash).
type userDTO struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt int64  `json:"created_at"`
}

func toDTO(u store.User) userDTO {
	return userDTO{ID: u.ID, Username: u.Username, Role: u.Role, CreatedAt: u.CreatedAt}
}

// GET /api/v1/users (admin-only) — lists users without password hashes.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	out := make([]userDTO, 0, len(users))
	for _, u := range users {
		out = append(out, toDTO(u))
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/v1/users (admin-only) — creates a user. 409 on duplicate username.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	c, err := decodeCreds(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
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
	_, err = s.store.CreateUser(r.Context(), c.Username, hash, c.Role)
	switch {
	case errors.Is(err, store.ErrDuplicate):
		writeError(w, http.StatusConflict, "username already exists")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"username": c.Username, "role": c.Role})
}

// DELETE /api/v1/users/{id} (admin-only) — 409 if deleting yourself or the
// last admin (a captain cannot be removed by their own hand unless another
// admin exists, and the fleet always keeps at least one).
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if uid, ok := userIDFromCtx(r.Context()); ok && uid == id {
		writeError(w, http.StatusConflict, "cannot delete yourself")
		return
	}
	user, err := s.store.GetUserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if user.Role == "admin" {
		admins, err := s.store.CountAdmins(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		if admins <= 1 {
			writeError(w, http.StatusConflict, "cannot delete the last admin")
			return
		}
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PUT /api/v1/users/{id}/password {"password": "..."} — an admin may change
// anyone's password; a plain user may only change their own (self). The route
// lives in the authenticated (not admin-only) group precisely to allow the
// self case; the admin-or-self check is done in the handler.
func (s *Server) handleChangeUserPassword(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(body.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	role, _ := r.Context().Value(ctxKeyRole).(string)
	uid, ok := userIDFromCtx(r.Context())
	if !ok || (role != "admin" && uid != id) {
		writeError(w, http.StatusForbidden, "admin role required to change another user's password")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}
	if err := s.store.UpdateUserPassword(r.Context(), id, hash); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
