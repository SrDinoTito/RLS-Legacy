package api_test

// End-to-end test of the auth + users flow over a real HTTP server backed by
// a SQLite database file inside t.TempDir():
//
// healthz → bootstrap → bootstrap-twice 403 → login (ok + invalid 401) →
// me → no-cookie 401 → list users → create user (ok/dup/invalid) →
// viewer 403 → delete last admin 409 → delete ok/404 → logout 204.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rls-legacy/captain/backend/internal/api"
	"github.com/rls-legacy/captain/backend/internal/store"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	secret := []byte("e2e-test-secret-0123456789abcdef")

	srv := httptest.NewServer(api.New(st, secret).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// clientFor returns an HTTP client with its own cookie jar so each client is
// an independent identity (sessions do not overwrite each other).
func clientFor() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

type response struct {
	status int
	body   string
	header http.Header
}

// do sends a JSON request. A nil client means "anonymous": a fresh empty jar
// is created per request.
func do(t *testing.T, client *http.Client, method, url, body string) response {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatalf("new %s %s: %v", method, url, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if client == nil {
		client = clientFor()
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, url, err)
	}
	return response{status: resp.StatusCode, body: string(data), header: resp.Header}
}

// login POSTs credentials on behalf of req (nil → anonymous) and returns the response.
func login(t *testing.T, client *http.Client, srv *httptest.Server, creds map[string]string) response {
	t.Helper()
	b, err := json.Marshal(creds)
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	return do(t, client, http.MethodPost, srv.URL+"/api/v1/auth/login", string(b))
}

// clientLoggedIn returns a fresh client authenticated as the given user.
func clientLoggedIn(t *testing.T, srv *httptest.Server, username, password string) *http.Client {
	t.Helper()
	cl := clientFor()
	r := login(t, cl, srv, map[string]string{"username": username, "password": password})
	if r.status != http.StatusOK {
		t.Fatalf("login as %s: got %d: %s", username, r.status, r.body)
	}
	return cl
}

func adminID(t *testing.T, cl *http.Client, srv *httptest.Server) int64 {
	t.Helper()
	r := do(t, cl, http.MethodGet, srv.URL+"/api/v1/users", "")
	if r.status != http.StatusOK {
		t.Fatalf("list users: got %d: %s", r.status, r.body)
	}
	var users []map[string]any
	if err := json.Unmarshal([]byte(r.body), &users); err != nil {
		t.Fatalf("unmarshal users: %v (%s)", err, r.body)
	}
	for _, u := range users {
		if u["username"] == "admin" {
			return int64(u["id"].(float64))
		}
	}
	t.Fatalf("admin user not found in %v", users)
	return 0
}

func userIDByUsername(t *testing.T, cl *http.Client, srv *httptest.Server, username string) int64 {
	t.Helper()
	r := do(t, cl, http.MethodGet, srv.URL+"/api/v1/users", "")
	if r.status != http.StatusOK {
		t.Fatalf("list users: got %d: %s", r.status, r.body)
	}
	var users []map[string]any
	if err := json.Unmarshal([]byte(r.body), &users); err != nil {
		t.Fatalf("unmarshal users: %v (%s)", err, r.body)
	}
	for _, u := range users {
		if u["username"] == username {
			return int64(u["id"].(float64))
		}
	}
	t.Fatalf("user %q not found in %v", username, users)
	return 0
}

func setSessionCookieIn(h http.Header) bool {
	for _, c := range (&http.Response{Header: h}).Cookies() {
		if c.Name == api.SessionCookie {
			return true
		}
	}
	return false
}

func TestE2E_AuthAndUsersFlow(t *testing.T) {
	srv := newTestServer(t)
	base := srv.URL

	// 1. /healthz public, {"status":"ok"}.
	r := do(t, nil, http.MethodGet, base+"/healthz", "")
	if r.status != 200 || !strings.Contains(r.body, `"status":"ok"`) {
		t.Fatalf("healthz: got %d %s", r.status, r.body)
	}

	// 2. Bootstrap: empty DB → creates first admin (201) + session cookie.
	jar := clientFor()
	r = do(t, jar, http.MethodPost, base+"/api/v1/auth/bootstrap",
		`{"username":"admin","password":"admin-password-1"}`)
	if r.status != 201 {
		t.Fatalf("bootstrap: got %d %s", r.status, r.body)
	}
	if !strings.Contains(r.body, `"username":"admin"`) || !strings.Contains(r.body, `"role":"admin"`) {
		t.Fatalf("bootstrap body: %s", r.body)
	}
	if !setSessionCookieIn(r.header) {
		t.Fatalf("bootstrap did not set session cookie: %v", r.header)
	}

	// 3. Second bootstrap is forbidden now that users exist.
	r = do(t, nil, http.MethodPost, base+"/api/v1/auth/bootstrap",
		`{"username":"other","password":"other-password-9"}`)
	if r.status != 403 {
		t.Fatalf("second bootstrap: got %d %s", r.status, r.body)
	}

	// 4. Login with wrong password → 401.
	r = login(t, clientFor(), srv, map[string]string{"username": "admin", "password": "wrong-password"})
	if r.status != 401 {
		t.Fatalf("wrong password login: got %d %s", r.status, r.body)
	}

	// 5. Login with unknown user → 401.
	r = login(t, clientFor(), srv, map[string]string{"username": "ghost", "password": "whatever123"})
	if r.status != 401 {
		t.Fatalf("unknown user login: got %d %s", r.status, r.body)
	}

	// 6. Classic login works and sets the session cookie.
	admin := clientFor()
	r = login(t, admin, srv, map[string]string{"username": "admin", "password": "admin-password-1"})
	if r.status != 200 || !strings.Contains(r.body, `"username":"admin"`) {
		t.Fatalf("login: got %d %s", r.status, r.body)
	}
	if !setSessionCookieIn(r.header) {
		t.Fatalf("login did not set session cookie: %v", r.header)
	}

	// 7. /auth/me returns the session user.
	r = do(t, admin, http.MethodGet, base+"/api/v1/auth/me", "")
	if r.status != 200 || !strings.Contains(r.body, `"role":"admin"`) {
		t.Fatalf("me: got %d %s", r.status, r.body)
	}

	// 8. /users without any cookie → 401.
	r = do(t, nil, http.MethodGet, base+"/api/v1/users", "")
	if r.status != 401 {
		t.Fatalf("no-cookie /users: got %d %s", r.status, r.body)
	}

	// 9. List users (admin): visible, never leaks password_hash.
	aID := adminID(t, admin, srv)
	r = do(t, admin, http.MethodGet, base+"/api/v1/users", "")
	if strings.Contains(strings.ToLower(r.body), "password_hash") {
		t.Fatalf("list users leaks password_hash: %s", r.body)
	}

	// 10. Create viewer (201); duplicate username (409); invalid creds (400).
	create := func(body string) response {
		return do(t, admin, http.MethodPost, base+"/api/v1/users", body)
	}
	r = create(`{"username":"viewer1","password":"viewer-password-1","role":"viewer"}`)
	if r.status != 201 {
		t.Fatalf("create viewer: got %d %s", r.status, r.body)
	}
	r = create(`{"username":"viewer1","password":"viewer-password-1","role":"viewer"}`)
	if r.status != 409 {
		t.Fatalf("duplicate username: got %d %s", r.status, r.body)
	}
	r = create(`{"username":"ab","password":"short","role":"viewer"}`)
	if r.status != 400 {
		t.Fatalf("invalid creds: got %d %s", r.status, r.body)
	}
	r = create(`{"username":"viewer2","password":"viewer-password-1","role":"root"}`)
	if r.status != 400 {
		t.Fatalf("invalid role: got %d %s", r.status, r.body)
	}

	// 11. Viewer login and permission checks → 403 on admin endpoints.
	viewer := clientLoggedIn(t, srv, "viewer1", "viewer-password-1")
	if vID := userIDByUsername(t, admin, srv, "viewer1"); vID != aID+1 {
		t.Fatalf("expected sequential viewer id %d, got %d", aID+1, vID)
	}
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

	// 12. Delete the viewer (204), repeat → 404, then last admin → 409.
	vID := userIDByUsername(t, admin, srv, "viewer1")
	r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/users/%d", base, vID), "")
	if r.status != 204 {
		t.Fatalf("delete viewer: got %d %s", r.status, r.body)
	}
	r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/users/%d", base, vID), "")
	if r.status != 404 {
		t.Fatalf("delete gone viewer: got %d", r.status)
	}
	r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/users/999", base), "")
	if r.status != 404 {
		t.Fatalf("delete nonexistent: got %d", r.status)
	}
	r = do(t, admin, http.MethodDelete, base+"/api/v1/users/abc", "")
	if r.status != 400 {
		t.Fatalf("delete invalid id: got %d", r.status)
	}
	r = do(t, admin, http.MethodDelete, fmt.Sprintf("%s/api/v1/users/%d", base, aID), "")
	if r.status != 409 {
		t.Fatalf("delete last admin: got %d %s", r.status, r.body)
	}

	// 13. Logout expires the cookie; /me afterwards → 401.
	r = do(t, admin, http.MethodPost, base+"/api/v1/auth/logout", "")
	if r.status != 204 {
		t.Fatalf("logout: got %d %s", r.status, r.body)
	}
	r = do(t, admin, http.MethodGet, base+"/api/v1/auth/me", "")
	if r.status != 401 {
		t.Fatalf("me after logout: got %d", r.status)
	}

	// 14. Ensure viewer was actually removed (fresh admin session; client
	// "admin" logged out at step 13, so its session cookie is gone).
	admin2 := clientLoggedIn(t, srv, "admin", "admin-password-1")
	r = do(t, admin2, http.MethodGet, base+"/api/v1/users", "")
	if r.status != 200 || strings.Contains(r.body, "viewer1") {
		t.Fatalf("users after deletes: %d %s", r.status, r.body)
	}
}
