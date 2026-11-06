package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Token is a bearer credential issued to operators. Since DEC-011 (TASK-011)
// BOTH the SHA-256 hash and the plaintext are persisted: token_plain lets the
// UI show/copy the credential at any time (the "shown exactly once" model is
// gone). agent_id links the 1:1 agent row, once auto-registered by its first
// beat. Tags is a JSON array TEXT (e.g. '["lab","infra"]'); NULL/empty means
// "no system tags". Tags are shared with the linked agent (ecosystem tags).
type Token struct {
	ID         int64
	TokenHash  string
	TokenPlain sql.NullString
	Name       string
	Tags       sql.NullString
	AgentID    sql.NullInt64
	CreatedAt  int64
	RevokedAt  sql.NullInt64
	// VPSID is pinned by the first Stowaway heartbeat to the Docker Engine ID.
	// A token may be used by many containers, but only one VPS.
	VPSID     sql.NullString
	VPSScoped bool // true only for credentials created under the per-VPS contract.
	// Enrollment is the historical multi-agent capability used by Stowaway.
	// Newly issued enrollment tokens are also VPSScoped and pinned to one
	// Docker Engine; unscoped rows are grandfathered legacy credentials.
	Enrollment bool
}

// tokenColumns is the SELECT projection shared by every token lookup.
const tokenColumns = `id, token_hash, token_plain, name, tags, agent_id, created_at, revoked_at, enrollment, vps_id, vps_scoped`

func scanToken(row scannableRow) (*Token, error) {
	var t Token
	var enrollment, vpsScoped int64
	err := row.Scan(&t.ID, &t.TokenHash, &t.TokenPlain, &t.Name, &t.Tags,
		&t.AgentID, &t.CreatedAt, &t.RevokedAt, &enrollment, &t.VPSID, &vpsScoped)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan token: %w", err)
	}
	t.Enrollment = enrollment != 0
	t.VPSScoped = vpsScoped != 0
	return &t, nil
}

var (
	ErrTokenBoundToOtherVPS = errors.New("token is bound to another VPS")
	ErrVPSAlreadyHasToken   = errors.New("VPS already has an active token")
)

// BindTokenVPS pins an enrollment token to the first stable Docker Engine ID
// it reports. Repeated beats from the same engine are idempotent; a token
// cannot migrate to another engine, and a VPS cannot have two active tokens.
func (s *Store) BindTokenVPS(ctx context.Context, tokenID int64, vpsID string) error {
	vpsID = strings.TrimSpace(vpsID)
	if vpsID == "" {
		return nil
	}

	var current sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT vps_id FROM tokens WHERE id = ? AND revoked_at IS NULL`, tokenID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("read VPS binding for token %d: %w", tokenID, err)
	}
	if current.Valid {
		if current.String != vpsID {
			return ErrTokenBoundToOtherVPS
		}
		return nil
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE tokens SET vps_id = ? WHERE id = ? AND vps_id IS NULL AND revoked_at IS NULL`,
		vpsID, tokenID); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") {
			return ErrVPSAlreadyHasToken
		}
		return fmt.Errorf("bind token %d to VPS: %w", tokenID, err)
	}

	// A concurrent first beat may have bound this token to a different VPS.
	if err := s.db.QueryRowContext(ctx,
		`SELECT vps_id FROM tokens WHERE id = ?`, tokenID).Scan(&current); err != nil {
		return fmt.Errorf("verify VPS binding for token %d: %w", tokenID, err)
	}
	if current.Valid && current.String != vpsID {
		return ErrTokenBoundToOtherVPS
	}
	return nil
}

// TokenActiveByHash resolves a SHA-256 token hash to its token row only
// while it is not revoked. ErrNotFound otherwise (the caller maps this to a
// 401; no information is leaked about revoked vs unknown tokens).
func (s *Store) TokenActiveByHash(ctx context.Context, tokenHash string) (*Token, error) {
	return scanToken(s.db.QueryRowContext(ctx,
		`SELECT `+tokenColumns+`
		 FROM tokens WHERE token_hash = ? AND revoked_at IS NULL`, tokenHash))
}

// ParseTags parses the stored JSON-array TEXT of tags ([]string; stored
// NULL/empty → nil). Corrupt content degrades to nil instead of failing the
// caller (tokens and agents share the same tags column format).
func ParseTags(ns sql.NullString) []string {
	if !ns.Valid || strings.TrimSpace(ns.String) == "" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(ns.String), &arr); err != nil {
		return nil
	}
	return arr
}

// CreateToken inserts a token row with BOTH its SHA-256 hash and its
// plaintext (DEC-011: the UI shows/copies the token forever). tagsJSON is
// the marshaled []string ("" or "null" → NULL = no tags). ErrDuplicate on a
// hash collision. This creates an unscoped legacy/fixture token; new API
// credentials use CreateTokenFull with enrollment=true and vpsScoped=true.
func (s *Store) CreateToken(ctx context.Context, tokenHash, tokenPlain, name, tagsJSON string) (int64, error) {
	return s.CreateTokenFull(ctx, tokenHash, tokenPlain, name, tagsJSON, false, false)
}

// CreateTokenFull supports historical token modes and per-VPS credentials.
// New production tokens use enrollment=true and vpsScoped=true: one token is
// pinned to one Docker Engine while its containers link through token_id.
func (s *Store) CreateTokenFull(ctx context.Context, tokenHash, tokenPlain, name, tagsJSON string, enrollment, vpsScoped bool) (int64, error) {
	if tagsJSON == "null" {
		tagsJSON = ""
	}
	enrInt := 0
	if enrollment {
		enrInt = 1
	}
	vpsScopedInt := 0
	if vpsScoped {
		vpsScopedInt = 1
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO tokens (token_hash, token_plain, name, tags, enrollment, vps_scoped, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tokenHash, nullableText(tokenPlain), name, nullableText(tagsJSON), enrInt, vpsScopedInt, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, ErrDuplicate
		}
		return 0, fmt.Errorf("insert token: %w", err)
	}
	return res.LastInsertId()
}

// nullableText maps the empty string to SQL NULL (TEXT column with real
// "absent" semantics instead of an empty blob).
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// RevokeToken marks a token revoked (revoked_at = now). Shared by the
// future DELETE /api/v1/tokens/{id} endpoint; used by e2e tests to prove
// revoked tokens stop heartbeating.
func (s *Store) RevokeToken(ctx context.Context, tokenID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		time.Now().Unix(), tokenID)
	if err != nil {
		return fmt.Errorf("revoke token %d: %w", tokenID, err)
	}
	return nil
}

// RevokeTokenAt marks a token revoked at an explicit timestamp (injectable
// clock for tests) and reports rows affected: 0 means already revoked or
// nonexistent, so the caller reads existence separately if it must tell
// those two cases apart.
func (s *Store) RevokeTokenAt(ctx context.Context, tokenID int64, nowSec int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		nowSec, tokenID)
	if err != nil {
		return 0, fmt.Errorf("revoke token %d: %w", tokenID, err)
	}
	n, err := res.RowsAffected()
	return n, err
}

// BindTokenAgent records the auto-registered agent id on the token row,
// completing the 1:1 mapping (tokens.agent_id).
func (s *Store) BindTokenAgent(ctx context.Context, tokenID, agentID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET agent_id = ? WHERE id = ?`, agentID, tokenID)
	if err != nil {
		return fmt.Errorf("bind agent %d to token %d: %w", agentID, tokenID, err)
	}
	return nil
}

// ---- TASK-004: tokens API support ----------------------------------------

// ListTokens returns every token row (newest first). The plaintext (when
// known — rows seeded hash-only carry NULL and the API maps that to
// token=null) and tags ride along since DEC-011 (TASK-011).
func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+tokenColumns+`
		 FROM tokens ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("list tokens: %w", err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// TokenByID fetches one token row (hash AND plaintext included; the API
// projects both, DEC-011). ErrNotFound when absent.
func (s *Store) TokenByID(ctx context.Context, id int64) (*Token, error) {
	return scanToken(s.db.QueryRowContext(ctx,
		`SELECT `+tokenColumns+`
		 FROM tokens WHERE id = ?`, id))
}

// ---- TASK-011: tags -------------------------------------------------------

// UpdateTokenTags overwrites the tags JSON of a token. Labels of a linked
// explorer are untouched (agents.labels is a different column). ErrNotFound
// when the token does not exist.
func (s *Store) UpdateTokenTags(ctx context.Context, tokenID int64, tagsJSON string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE tokens SET tags = ? WHERE id = ?`, nullableText(tagsJSON), tokenID)
	if err != nil {
		return fmt.Errorf("update token %d tags: %w", tokenID, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return fmt.Errorf("update token %d tags: %w", tokenID, err)
		}
		return ErrNotFound
	}
	return nil
}

// SetAgentTags overwrites the system tags of an agent (agents.tags). Used
// on the first token bind (token tags copied over) and by the token-tags
// endpoint (live sync). Explorer labels (agents.labels) are NOT touched —
// they belong to the explorer's own reported labels.
func (s *Store) SetAgentTags(ctx context.Context, agentID int64, tagsJSON string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET tags = ? WHERE id = ?`, nullableText(tagsJSON), agentID)
	if err != nil {
		return fmt.Errorf("set agent %d tags: %w", agentID, err)
	}
	return nil
}

// ---- Legacy enrollment / adopted-container counts ------------------------

// AdoptedCounts maps token_id to linked container records. All statuses count
// so retired rows preserve their historical inventory.
func (s *Store) AdoptedCounts(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT token_id, COUNT(*) FROM agents WHERE token_id IS NOT NULL GROUP BY token_id`)
	if err != nil {
		return nil, fmt.Errorf("adopted counts: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]int64)
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("adopted counts: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}
