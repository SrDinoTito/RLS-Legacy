// notify_settings.go: admin CRUD of the notification channels configured
// from the dashboard (REQ-050). Design (CON contract):
//
//	GET /api/v1/notify/settings → masked view (last 8 / last 4 chars; NEVER
//	  plaintext webhook/token over the wire), effective values = DB row field
//	  first, RLS_NOTIF_* env fallback, plus per-channel enabled flags.
//	PUT /api/v1/notify/settings → optional fields; absent = untouched,
//	  null/"" = disable that canal; strict validation → 400; returns the
//	  updated masked view. An empty value only disables when no env fallback
//	  backs the channel (documented behavior of the DB-first resolution).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/rls-legacy/captain/backend/internal/store"
)

// minimal prefixes for a (supposedly valid) Discord webhook URL.
var discordWebhookPrefixes = []string{
	"https://discord.com/api/webhooks/",
	"https://discordapp.com/api/webhooks/",
}

// telegramBotTokenRE: "<numeric id>:<base64url-ish secret>" per the official format.
var telegramBotTokenRE = regexp.MustCompile(`^\d+:[A-Za-z0-9_-]+$`)

// telegramChatIDRE: numeric chat id (allow the leading '-' of group chats).
var telegramChatIDRE = regexp.MustCompile(`^-?\d+$`)

// notifySettingsView is the GET/PUT response: masked, never plaintext.
type notifySettingsView struct {
	// last 8 chars of the webhook URL, or null (not configured / disabled).
	DiscordWebhookURL *string `json:"discord_webhook_url"`
	// last 4 chars of the bot token, or null.
	TelegramBotToken *string `json:"telegram_bot_token"`
	// full chat id (a numeric id, not a secret), or null.
	TelegramChatID  *string `json:"telegram_chat_id"`
	DiscordEnabled  bool    `json:"discord_enabled"`
	TelegramEnabled bool    `json:"telegram_enabled"`
}

// tail returns the last n characters of s (for masking views).
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// apiSettings is the internal effective snapshot (DB-first, env fallback).
type apiSettings struct{ discordWebhook, telegramToken, telegramChatID string }

func (s apiSettings) view() notifySettingsView {
	discordURL, telegramToken, telegramChatID :=
		(*string)(nil), (*string)(nil), (*string)(nil)
	if s.discordWebhook != "" {
		v := tail(s.discordWebhook, 8)
		discordURL = &v
	}
	if s.telegramToken != "" {
		v := tail(s.telegramToken, 4)
		telegramToken = &v
	}
	if s.telegramChatID != "" {
		v := s.telegramChatID
		telegramChatID = &v
	}
	return notifySettingsView{
		DiscordWebhookURL: discordURL,
		TelegramBotToken:  telegramToken,
		TelegramChatID:    telegramChatID,
		DiscordEnabled:    s.discordWebhook != "",
		TelegramEnabled:   s.telegramToken != "" && s.telegramChatID != "",
	}
}

// effectiveNotifySettings merges the persisted row (DB wins per field) over
// the RLS_NOTIF_* env fallback. The DB row always exists (idempotent seed).
func (s *Server) effectiveNotifySettings(ctx context.Context) (apiSettings, error) {
	row, err := s.store.GetNotifySettings(ctx)
	if err != nil {
		return apiSettings{}, err
	}
	out := apiSettings{
		discordWebhook: row.DiscordWebhookURL,
		telegramToken:  row.TelegramBotToken,
		telegramChatID: row.TelegramChatID,
	}
	if out.discordWebhook == "" {
		out.discordWebhook = s.notifEnv.DiscordWebhookURL
	}
	if out.telegramToken == "" {
		out.telegramToken = s.notifEnv.TelegramBotToken
	}
	if out.telegramChatID == "" {
		out.telegramChatID = s.notifEnv.TelegramChatID
	}
	return out, nil
}

// GET /api/v1/notify/settings — admin-only masked view.
func (s *Server) handleGetNotifySettings(w http.ResponseWriter, r *http.Request) {
	eff, err := s.effectiveNotifySettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, eff.view())
}

// rawOptionalString decodes one JSON raw value of the PUT body:
// absent key → nil (untouched); null → ""; "texto" → texto; other type →
// invalid marker (nil, !ok) so the handler can answer 400.
func rawOptionalString(raw json.RawMessage) (*string, bool) {
	if len(raw) == 0 {
		return nil, true // key absent → untouched
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		v := ""
		return &v, true // explicit null → disable
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	return &v, true
}

// PUT /api/v1/notify/settings — admin-only partial validation + upsert.
func (s *Server) handlePutNotifySettings(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<15))
	var req struct {
		DiscordWebhookURL json.RawMessage `json:"discord_webhook_url"`
		TelegramBotToken  json.RawMessage `json:"telegram_bot_token"`
		TelegramChatID    json.RawMessage `json:"telegram_chat_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	patch := store.NotifyPatch{}
	if v, ok := rawOptionalString(req.DiscordWebhookURL); !ok {
		writeError(w, http.StatusBadRequest, "discord_webhook_url debe ser un string o null")
		return
	} else if v != nil {
		val := strings.TrimSpace(*v)
		if val != "" && !validDiscordWebhook(val) {
			writeError(w, http.StatusBadRequest,
				`discord_webhook_url inválido: debe empezar con https://discord.com/api/webhooks/ o https://discordapp.com/api/webhooks/`)
			return
		}
		patch.DiscordWebhookURL = &val
	}
	if v, ok := rawOptionalString(req.TelegramBotToken); !ok {
		writeError(w, http.StatusBadRequest, "telegram_bot_token debe ser un string o null")
		return
	} else if v != nil {
		val := strings.TrimSpace(*v)
		if val != "" && !telegramBotTokenRE.MatchString(val) {
			writeError(w, http.StatusBadRequest,
				"telegram_bot_token inválido: formato esperado \"<numeric_id>:<secret>\"")
			return
		}
		patch.TelegramBotToken = &val
	}
	if v, ok := rawOptionalString(req.TelegramChatID); !ok {
		writeError(w, http.StatusBadRequest, "telegram_chat_id debe ser un string o null")
		return
	} else if v != nil {
		val := strings.TrimSpace(*v)
		if val != "" && !telegramChatIDRE.MatchString(val) {
			writeError(w, http.StatusBadRequest,
				"telegram_chat_id inválido: debe ser numérico (admite '-' inicial para grupos)")
			return
		}
		patch.TelegramChatID = &val
	}

	if err := s.store.PatchNotifySettings(r.Context(), patch); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	eff, err := s.effectiveNotifySettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, eff.view())
}

func validDiscordWebhook(url string) bool {
	for _, p := range discordWebhookPrefixes {
		if strings.HasPrefix(url, p) {
			return true
		}
	}
	return false
}
