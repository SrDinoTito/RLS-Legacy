// notify.go: persisted notification-channel settings (admin UI, SQLite row
// id=1). Each field is NULL/empty when the channel was never configured or
// was explicitly disabled from the dashboard; the notifier resolves the
// effective credentials DB-first with the RLS_NOTIF_* env vars as fallback.
package store

import (
	"context"
	"fmt"
	"time"
)

// NotifySettings is the snapshot of the notify_settings row (id=1). Empty
// string = campo sin configurar / canal deshabilitado via UI.
type NotifySettings struct {
	DiscordWebhookURL string
	TelegramBotToken  string
	TelegramChatID    string
	UpdatedAt         int64
}

// NotifyPatch is a partial update: nil = leave the field untouched;
// non-nil = set it (empty string clears it → canal deshabilitado).
type NotifyPatch struct {
	DiscordWebhookURL *string
	TelegramBotToken  *string
	TelegramChatID    *string
}

// GetNotifySettings returns the settings row. Idempotent migration guarantees
// the id=1 row always exists (seeded with NULLs on Open).
func (s *Store) GetNotifySettings(ctx context.Context) (NotifySettings, error) {
	var n NotifySettings
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(discord_webhook_url, ''),
			COALESCE(telegram_bot_token, ''),
			COALESCE(telegram_chat_id, ''),
			COALESCE(updated_at, 0)
		FROM notify_settings WHERE id = 1`).
		Scan(&n.DiscordWebhookURL, &n.TelegramBotToken, &n.TelegramChatID, &n.UpdatedAt)
	if err != nil {
		return n, fmt.Errorf("get notify settings: %w", err)
	}
	return n, nil
}

// PatchNotifySettings merges the patch over the current row and upserts it
// (single-connection pool serializes the read-merge-write). Never blocks on
// absent fields: nil pointers keep the stored value.
func (s *Store) PatchNotifySettings(ctx context.Context, patch NotifyPatch) error {
	cur, err := s.GetNotifySettings(ctx)
	if err != nil {
		return err
	}
	if patch.DiscordWebhookURL != nil {
		cur.DiscordWebhookURL = *patch.DiscordWebhookURL
	}
	if patch.TelegramBotToken != nil {
		cur.TelegramBotToken = *patch.TelegramBotToken
	}
	if patch.TelegramChatID != nil {
		cur.TelegramChatID = *patch.TelegramChatID
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO notify_settings (id, discord_webhook_url, telegram_bot_token, telegram_chat_id, updated_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			discord_webhook_url = excluded.discord_webhook_url,
			telegram_bot_token = excluded.telegram_bot_token,
			telegram_chat_id = excluded.telegram_chat_id,
			updated_at = excluded.updated_at`,
		cur.DiscordWebhookURL, cur.TelegramBotToken, cur.TelegramChatID, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("patch notify settings: %w", err)
	}
	return nil
}
