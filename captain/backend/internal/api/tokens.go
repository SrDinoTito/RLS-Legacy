// tokens.go: agent-token lifecycle API (TASK-004 + TASK-011/DEC-011). The
// plaintext token is generated with crypto/rand and PERSISTED alongside its
// SHA-256 hash — DEC-011 removed the "shown exactly once" model so the UI
// can always display/copy the credential. Every token row also carries
// ecosystem `tags` (JSON array of system tags, shared 1:1 with the linked
// agent row once it exists).
package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/rls-legacy/captain/backend/internal/store"
)

// tokenDTO is the public projection of a token row: it exposes the
// PLAINTEXT when known (DEC-011; rows seeded hash-only or legacy databases
// render token=null) but never the stored hash. Pointer fields render NULL
// (agent_id unset, not revoked yet) as literal JSON null.
type tokenDTO struct {
	ID        int64    `json:"id"`
	Token     *string  `json:"token"` // plaintext credential (null if unknown)
	Name      string   `json:"name"`
	Tags      []string `json:"tags"` // system tags; null = none set
	AgentID   *int64   `json:"agent_id"`
	CreatedAt int64    `json:"created_at"`
	RevokedAt *int64   `json:"revoked_at"`
	VPSID     *string  `json:"vps_id"`
	VPSScoped bool     `json:"vps_scoped"`
	Status    string   `json:"status"` // active | revoked
	// TASK-013: enrollment flag + the count of agents linked via
	// agents.token_id (1:1 tokens ⇒ 0/1 after retro-fill; enrollment
	// fleets count every adopted member, dismissed included).
	Enrollment   bool  `json:"enrollment"`
	AdoptedCount int64 `json:"adopted_count"`
}

func tokenToDTO(t store.Token, adopted int64) tokenDTO {
	var plain *string
	if t.TokenPlain.Valid {
		p := t.TokenPlain.String
		plain = &p
	}
	dto := tokenDTO{
		ID:        t.ID,
		Token:     plain,
		Name:      t.Name,
		Tags:      tagsFromJSON(t.Tags),
		CreatedAt: t.CreatedAt,
		Status:    "active",
	}
	if t.AgentID.Valid {
		dto.AgentID = &t.AgentID.Int64
	}
	if t.RevokedAt.Valid {
		dto.RevokedAt = &t.RevokedAt.Int64
		dto.Status = "revoked"
	}
	if t.VPSID.Valid {
		vpsID := t.VPSID.String
		dto.VPSID = &vpsID
	}
	dto.Enrollment = t.Enrollment
	dto.VPSScoped = t.VPSScoped
	dto.AdoptedCount = adopted
	return dto
}

// tokenListDTOs projects a token list with adopted counts (TASK-013); a
// counts fetch failure degrades to zeros instead of failing the list.
func (s *Server) tokenListDTOs(w http.ResponseWriter, r *http.Request) (int, any) {
	tokens, err := s.store.ListTokens(r.Context())
	if err != nil {
		return http.StatusInternalServerError, nil
	}
	counts, err := s.store.AdoptedCounts(r.Context())
	if err != nil {
		slog.Warn("adopted counts lookup failed", "error", err)
		counts = map[int64]int64{}
	}
	out := make([]tokenDTO, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, tokenToDTO(t, counts[t.ID]))
	}
	return http.StatusOK, out
}

// tagsFromJSON parses the stored JSON-array TEXT of tags ([]string; stored
// NULL/empty → nil → JSON null on the wire). Corrupt content degrades to
// null instead of failing the whole list.
func tagsFromJSON(ns sql.NullString) []string {
	return store.ParseTags(ns)
}

// tagsToJSON marshals a normalized tag list for storage. Empty list → ""
// (stores as SQL NULL = "no tags").
func tagsToJSON(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "" // unreachable for []string
	}
	return string(b)
}

// tag limits (TASK-011): 1..5 tags per token/agent, each 1..24 chars of
// lowercase a-z / 0-9 / '-' / '_' after trimming.
const (
	maxTags     = 5
	maxTagLen   = 24
	minTagsOnUp = 1
)

var tagRE = regexp.MustCompile(`^[a-z0-9_-]+$`)

// normalizeTags validates a raw tags array: trims each value, dedupes
// (first occurrence wins, order preserved) and enforces lowercase
// a-z0-9-_ up to 24 chars. Returns the normalized list; an error means a
// 400 for the caller. An EMPTY input list is caller-defined (create
// rejects it — min 1; PUT treats it as "clear tags").
func normalizeTags(raw []string) ([]string, error) {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		t := strings.TrimSpace(r)
		if t == "" {
			continue
		}
		if len(t) > maxTagLen {
			return nil, fmt.Errorf("tag %q exceeds %d chars", t, maxTagLen)
		}
		if !tagRE.MatchString(t) {
			return nil, fmt.Errorf("tag %q: must be lowercase a-z, 0-9, '-' or '_'", t)
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > maxTags {
		return nil, fmt.Errorf("too many tags (max %d)", maxTags)
	}
	return out, nil
}

// GET /api/v1/tokens (admin) — lists every token INCLUDING the plaintext
// (DEC-011: the UI shows/copies it at any moment) plus tags, agent_id, the
// enrollment flag + adopted_count (TASK-013) and status.
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	status, body := s.tokenListDTOs(w, r)
	if status != http.StatusOK {
		writeError(w, status, "database error")
		return
	}
	writeJSON(w, status, body)
}

// POST /api/v1/tokens (admin) — generates `rlst_` + 32 hex chars from
// crypto/rand and persists BOTH the plaintext (DEC-011) and its hash.
// Body: {"name": "...", "tags": ["lab","infra"]?}. Every newly issued
// credential is enrollment + bound to exactly one Docker Engine: one Stowaway
// per VPS reports all its containers (1:N via agents.token_id). Previously
// issued shared/1:1 credentials remain accepted for a safe migration window.
// Returns 201 {"id","name","token","tags","enrollment":true,"vps_scoped":true,"vps_id":null}.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	if err != nil || len(bodyBytes) == 0 {
		writeError(w, http.StatusBadRequest, "request body required")
		return
	}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "token name required")
		return
	}
	// Tags: optional ("min 1" applies ONLY when the field is present) — a
	// token can still be created without system tags.
	var tags []string
	if body.Tags != nil {
		tags, err = normalizeTags(body.Tags)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(tags) < minTagsOnUp {
			writeError(w, http.StatusBadRequest, "at least one non-empty tag required when tags is set")
			return
		}
	}

	raw, hash, err := generateAgentToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	const enrollment, vpsScoped = true, true
	id, err := s.store.CreateTokenFull(r.Context(), hash, raw, name, tagsToJSON(tags), enrollment, vpsScoped)
	switch {
	case errors.Is(err, store.ErrDuplicate):
		// SHA-256 collision (astronomically unlikely); regenerate client-side.
		writeError(w, http.StatusConflict, "token collision, retry")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if tags == nil {
		tags = []string{}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         id,
		"name":       name,
		"token":      raw,
		"tags":       tags,
		"enrollment": enrollment,
		"vps_scoped": vpsScoped,
		"vps_id":     nil,
	})
}

// generateAgentToken builds the plaintext credential and its storage hash:
// 16 random bytes → 32 hex chars, prefixed rlst_ (same shape as the
// explorer README expects). The hash equals HashAgentToken(raw). Since
// DEC-011 BOTH forms are persisted (tokens.token_plain / tokens.token_hash).
func generateAgentToken() (plaintext, hash string, err error) {
	buf := make([]byte, 16)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	plaintext = "rlst_" + hex.EncodeToString(buf)
	return plaintext, HashAgentToken(plaintext), nil
}

// PUT /api/v1/tokens/{id}/tags (admin, TASK-011) — body {"tags": [...]}
// updates the token's system tags AND, when the token already has its 1:1
// agent bound (tokens.agent_id), syncs the agent row live: agents.tags are
// OVERWRITTEN by the token's tags (system tags are token-owned; the
// explorer labels in agents.labels are untouched). An empty array clears
// the tags of both. Responses: 200 with the UPDATED token list (same
// projection as GET /tokens); 400 invalid tags; 404 unknown token.
func (s *Server) handlePutTokenTags(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid token id")
		return
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	tags, err := normalizeTags(body.Tags)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tagsJSON := tagsToJSON(tags)
	tok, err := s.store.TokenByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if err := s.store.UpdateTokenTags(r.Context(), id, tagsJSON); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	// LIVE SYNC only reaches the 1:1 bound agent (tokens.agent_id). For
	// ENROLLMENT tokens (TASK-013) the token stays the tag TEMPLATE: every
	// adopted agent fixed its tags AT ADOPTION; editing the enrollment
	// token's tags does NOT re-write the fleet (adopted members keep the
	// ecosystem tags they were born with — re-classify by editing each
	// agent or adopting a fresh fleet token).
	if tok.AgentID.Valid {
		// Merge rule (DEC-011): the token's system tags OVERWRITE the
		// agent's system tags; explorer labels stay as reported.
		if err := s.store.SetAgentTags(r.Context(), tok.AgentID.Int64, tagsJSON); err != nil {
			slog.Warn("sync agent tags failed",
				"agent_id", tok.AgentID.Int64, "token_id", id, "error", err)
			// Non-fatal: the token itself was already updated; the next
			// successful sync heals the drift.
		}
	}
	listStatus, list := s.tokenListDTOs(w, r)
	if listStatus != http.StatusOK {
		writeError(w, listStatus, "database error")
		return
	}
	writeJSON(w, listStatus, list)
}

// DELETE /api/v1/tokens/{id} (admin) — soft-revokes (revoked_at = now).
// TASK-011/DEC-011: revoking a bound token AUTO-DISMISSES its linked agent
// (soft scorecard: dismissed_at stamped, open alerts resolved with " —
// agent dado de baja", exactly like DELETE /agents/{id}). Idempotent: no
// agent / already dismissed → no-op, still 204. A dismissal this endpoint
// performs is broadcast ONLY on SSE (agent_dismissed) — deliberately no
// outbound notifier push, consistent with TASK-009 (operator-driven
// lifecycle action, not a resource alert).
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid token id")
		return
	}
	tok, err := s.store.TokenByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if _, err := s.store.RevokeTokenAt(r.Context(), id, s.now().Unix()); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	// Auto-dismiss linked agents (if any) (TASK-011 → TASK-013): the 1:1
	// backlink (tokens.agent_id) UNION every agent adopted by this token
	// via agents.token_id (enrollment fleets → the whole fleet goes).
	// Best-effort: the revocation itself already succeeded; a
	// dismissed/missing agent is not an error.
	ids := make([]int64, 0, 2)
	if tok.AgentID.Valid {
		ids = append(ids, tok.AgentID.Int64)
	}
	if linked, err := s.store.AgentIDsByTokenID(r.Context(), tok.ID); err != nil {
		slog.Warn("enrollment agents lookup for token auto-dismiss failed",
			"token_id", id, "error", err)
	} else {
		for _, aid := range linked {
			known := false
			for _, k := range ids {
				if k == aid {
					known = true
					break
				}
			}
			if !known {
				ids = append(ids, aid)
			}
		}
	}
	for _, agentID := range ids {
		a, err := s.store.AgentByID(r.Context(), agentID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// Agent row gone (or never materialized): nothing to dismiss.
		case err != nil:
			slog.Warn("agent lookup for token auto-dismiss failed",
				"token_id", id, "agent_id", agentID, "error", err)
		case a.DismissedAt.Valid:
			// Already dismissed (e.g. repeat revoke): idempotent no-op.
		default:
			now := s.now().Unix()
			_, resolved, err := s.store.DismissAgent(r.Context(), agentID, now)
			if err != nil {
				slog.Warn("token auto-dismiss failed",
					"token_id", id, "agent_id", agentID, "error", err)
			} else {
				slog.Info("agent auto-dismissed by token revocation",
					"token_id", id, "agent_id", agentID,
					"name", a.Name, "alerts_resolved", resolved)
				s.broker.Broadcast("agent_dismissed", map[string]any{
					"agent_id": agentID, "name": a.Name,
					"status": "dismissed", "alerts_resolved": resolved,
				})
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
