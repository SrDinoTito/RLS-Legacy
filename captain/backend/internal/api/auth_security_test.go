package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/rls-legacy/captain/backend/internal/auth"
	"github.com/rls-legacy/captain/backend/internal/store"
)

func TestDeletedUserSessionIsRejected(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "auth-security.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	userID, err := st.CreateUser(ctx, "retired-admin", "not-used-by-this-test", "admin")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	secret := []byte("auth-security-test-secret-012345")
	token, err := auth.MintToken(secret, userID, "admin", 24*time.Hour, time.Now())
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	if err := st.DeleteUser(ctx, userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	res := httptest.NewRecorder()
	New(st, secret).Handler().ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user's session status = %d, want %d; body=%s", res.Code, http.StatusUnauthorized, res.Body.String())
	}
}
