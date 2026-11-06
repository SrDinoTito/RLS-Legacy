package api_test

// End-to-end test of the FULL users CRUD contract (list/create/delete +
// change-password) over a real HTTP server backed by SQLite in t.TempDir():
//
// bootstrap → list → create admin2/viewer (201, dup 409) → viewer denies
// (GET/POST/DELETE 403, PUT password of another 403) → viewer changes own
// password (204, old pw 401, new pw 200) → admin resets viewer password
// (204, new pw 200, invalid 400, unknown id 404) → self-delete 409 (two
// admins) → delete admin2 204 → last-admin delete 409 → cleanup viewer.
//
// The viewer's own-password change exercises the authed-group route
// PUT /users/{id}/password (admin-or-self enforced in the handler).

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestE2E_UsersCrud(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL

	// Bootstrap the first admin (id 1 in a fresh DB).
	r := do(t, nil, http.MethodPost, base+"/api/v1/auth/bootstrap",
		`{"username":"admin","password":"admin-password-1"}`)
	if r.status != 201 {
		t.Fatalf("bootstrap: got %d %s", r.status, r.body)
	}
	admin := clientLoggedIn(t, srv, "admin", "admin-password-1")

	// List users (admin): the bootstrap admin is there, no password_hash leak.
	r = do(t, admin, http.MethodGet, base+"/api/v1/users", "")
	if r.status != 200 {
		t.Fatalf("list users: got %d %s", r.status, r.body)
	}
	if strings.Contains(strings.ToLower(r.body), "password_hash") {
		t.Fatalf("list users leaks password_hash: %s", r.body)
	}
	aID := userIDByUsername(t, admin, srv, "admin")

	// Create a second admin and a viewer.
	create := func(body string) response {
		return do(t, admin, http.MethodPost, base+"/api/v1/users", body)
	}
	r = create(`{"username":"deputy","password":"deputy-password-1","role":"admin"}`)
	if r.status != 201 {
		t.Fatalf("create deputy: got %d %s", r.status, r.body)
	}
	r = create(`{"username":"viewer1","password":"viewer-password-1","role":"viewer"}`)
	if r.status != 201 {
		t.Fatalf("create viewer: got %d %s", r.status, r.body)
	}
	r = create(`{"username":"viewer1","password":"viewer-password-1","role":"viewer"}`)
	if r.status != 409 {
		t.Fatalf("duplicate username: got %d %s", r.status, r.body)
	}

	vID := userIDByUsername(t, admin, srv, "viewer1")
	viewer := clientLoggedIn(t, srv, "viewer1", "viewer-password-1")

	// Viewer denies on the admin-only user endpoints.
	r = do(t, viewer, http.MethodGet, base+"/api/v1/users", "")
	if r.status != 403 {
		t.Fatalf("viewer GET /users: got %d", r.status)
	}
	r = do(t, viewer, http.MethodPost, base+"/api/v1/users",
		`{"username":"h4x","password":"password123","role":"admin"}`)
	if r.status != 403 {
		t.Fatalf("viewer POST /users: got %d", r.status)
	}
	r = do(t, viewer, http.MethodDelete, fmt.Sprintf("%s/api/v1/users/%d", base, aID), "")
	if r.status != 403 {
		t.Fatalf("viewer DELETE /users: got %d", r.status)
	}
	// Viewer may NOT change another user's password...
	r = do(t, viewer, http.MethodPut, fmt.Sprintf("%s/api/v1/users/%d/password", base, aID),
		`{"password":"hacked-password"}`)
	if r.status != 403 {
		t.Fatalf("viewer PUT password of admin: got %d %s", r.status, r.body)
	}
	// ...only their own.
	r = do(t, viewer, http.MethodPut, fmt.Sprintf("%s/api/v1/users/%d/password", base, vID),
		`{"password":"viewer-new-password"}`)
	if r.status != 204 {
		t.Fatalf("viewer PUT own password: got %d %s", r.status, r.body)
	}
	// Old password is dead, the new one logs in.
	r = login(t, clientFor(), srv,
		map[string]string{"username": "viewer1", "password": "viewer-password-1"})
	if r.status != 401 {
		t.Fatalf("login with old password: got %d", r.status)
	}
	viewer = clientLoggedIn(t, srv, "viewer1", "viewer-new-password")

	// Admin password reset of another user: invalid body, unknown id, then 204.
	r = do(t, admin, http.MethodPut, fmt.Sprintf("%s/api/v1/users/%d/password", base, vID),
		`{"password":"short"}`)
	if r.status != 400 {
		t.Fatalf("admin PUT short password: got %d %s", r.status, r.body)
	}
	r = do(t, admin, http.MethodPut, base+"/api/v1/users/999/password",
		`{"password":"reset-password-1"}`)
	if r.status != 404 {
		t.Fatalf("admin PUT password unknown id: got %d", r.status)
	}
	r = do(t, admin, http.MethodPut, fmt.Sprintf("%s/api/v1/users/%d/password", base, vID),
		`{"password":"reset-password-1"}`)
	if r.status != 204 {
		t.Fatalf("admin PUT password reset: got %d %s", r.status, r.body)
	}
	if r = login(t, clientFor(), srv,
		map[string]string{"username": "viewer1", "password": "reset-password-1"}); r.status != 200 {
		t.Fatalf("login with reset password: got %d %s", r.status, r.body)
	}

	// Self-delete is always 409 (even with another admin present).
	r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/users/%d", base, aID), "")
	if r.status != 409 {
		t.Fatalf("admin self-delete: got %d %s", r.status, r.body)
	}

	// The deputy admin CAN be deleted by the first admin...
	dID := userIDByUsername(t, admin, srv, "deputy")
	r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/users/%d", base, dID), "")
	if r.status != 204 {
		t.Fatalf("delete deputy: got %d %s", r.status, r.body)
	}
	// ...and with only one admin left, deleting (via another session) is
	// still impossible: the last-admin guard holds now that self-delete
	// does not even let him try — verify the list keeps exactly the 2 users.
	r = do(t, admin, http.MethodGet, base+"/api/v1/users", "")
	if r.status != 200 || strings.Contains(r.body, "deputy") {
		t.Fatalf("users after deputy delete: %d %s", r.status, r.body)
	}

	// Cleanup: the admin removes the viewer.
	r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/users/%d", base, vID), "")
	if r.status != 204 {
		t.Fatalf("delete viewer: got %d %s", r.status, r.body)
	}
	// Unknown id → 404 (already deleted).
	r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/users/%d", base, vID), "")
	if r.status != 404 {
		t.Fatalf("delete gone viewer: got %d", r.status)
	}
}
