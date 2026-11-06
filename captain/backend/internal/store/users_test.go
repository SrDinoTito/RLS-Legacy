package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestCreateFirstAdminAllowsOnlyOneConcurrentBootstrap(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "bootstrap.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	start := make(chan struct{})
	results := make(chan struct {
		id  int64
		err error
	}, 2)
	var wg sync.WaitGroup
	for _, username := range []string{"admin-alpha", "admin-beta"} {
		wg.Add(1)
		go func(username string) {
			defer wg.Done()
			<-start
			id, err := st.CreateFirstAdmin(context.Background(), username, "bcrypt-hash")
			results <- struct {
				id  int64
				err error
			}{id: id, err: err}
		}(username)
	}
	close(start)
	wg.Wait()
	close(results)

	var winnerID int64
	created, rejected := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			created++
			winnerID = result.id
		case errors.Is(result.err, ErrBootstrapComplete):
			rejected++
		default:
			t.Fatalf("unexpected bootstrap error: %v", result.err)
		}
	}
	if created != 1 || rejected != 1 {
		t.Fatalf("created=%d rejected=%d, want one of each", created, rejected)
	}

	count, err := st.CountUsers(context.Background())
	if err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Fatalf("users after concurrent bootstrap = %d, want 1", count)
	}
	admin, err := st.GetUserByID(context.Background(), winnerID)
	if err != nil {
		t.Fatalf("load winning admin: %v", err)
	}
	if admin.Role != "admin" {
		t.Fatalf("winning user role = %q, want admin", admin.Role)
	}
}
