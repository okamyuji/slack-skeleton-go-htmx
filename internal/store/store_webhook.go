// webhooksテーブルへのアクセスをまとめます。
// Secretを含む取得は署名検証を行うサービス層だけが使います。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
)

// FindWebhookWithSecret tokenに対応するWebhookをSecret込みで取得します。
func (s *Store) FindWebhookWithSecret(ctx context.Context, token string) (domain.WebhookWithSecret, error) {
	const q = `SELECT id, channel_id, token, label, secret, bot_user_id, created_at
	             FROM webhooks
	            WHERE token = ?`
	var wh domain.WebhookWithSecret
	err := s.db.QueryRowContext(ctx, q, token).Scan(
		&wh.ID,
		&wh.ChannelID,
		&wh.Token,
		&wh.Label,
		&wh.Secret,
		&wh.BotUserID,
		&wh.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.WebhookWithSecret{}, ErrNotFound
		}
		return domain.WebhookWithSecret{}, fmt.Errorf("find webhook: %w", err)
	}
	return wh, nil
}

// ListWebhookSettingsForUser workspace配下のうち、userが参加しているチャンネルの
// Webhook管理表示用一覧だけを返します。Payload URLのtokenを含むため、
// Snapshotと同じMembership境界で読み取りを絞ります。
func (s *Store) ListWebhookSettingsForUser(ctx context.Context, workspaceID, userID int64) ([]domain.WebhookSetting, error) {
	const q = `SELECT w.id, w.channel_id, w.token, w.label, w.bot_user_id, w.created_at,
	                  c.name,
	                  CASE WHEN w.secret <> '' THEN TRUE ELSE FALSE END
	             FROM webhooks w
	             JOIN channels c ON c.id = w.channel_id
	             JOIN memberships m ON m.channel_id = w.channel_id AND m.user_id = ?
	            WHERE c.workspace_id = ?
	            ORDER BY w.created_at DESC, w.id DESC`
	rows, err := s.db.QueryContext(ctx, q, userID, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list webhook settings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanRows(rows, func(rows *sql.Rows) (domain.WebhookSetting, error) {
		var wh domain.WebhookSetting
		if err := rows.Scan(
			&wh.ID,
			&wh.ChannelID,
			&wh.Token,
			&wh.Label,
			&wh.BotUserID,
			&wh.CreatedAt,
			&wh.ChannelName,
			&wh.HasSecret,
		); err != nil {
			return domain.WebhookSetting{}, err
		}
		return wh, nil
	})
}

// FindWebhookBotUserIDByChannel channelと同じworkspaceにいるWebhook用botを返します。
func (s *Store) FindWebhookBotUserIDByChannel(ctx context.Context, channelID int64) (int64, error) {
	const q = `SELECT u.id
	             FROM users u
	             JOIN channels c ON c.workspace_id = u.workspace_id
	             JOIN memberships m ON m.user_id = u.id AND m.channel_id = c.id
	            WHERE c.id = ?
	              AND u.display_name = ?
	              AND u.is_bot = TRUE
	            LIMIT 1`
	var id int64
	err := s.db.QueryRowContext(ctx, q, channelID, webhookBotDisplayName).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("find webhook bot: %w", err)
	}
	return id, nil
}

// CreateWebhook Webhookを作成します。
func (s *Store) CreateWebhook(ctx context.Context, in CreateWebhookInput) (domain.Webhook, error) {
	if err := s.validateWebhookBot(ctx, in.BotUserID, in.ChannelID); err != nil {
		return domain.Webhook{}, err
	}
	// created_atをDEFAULT任せにしない理由はInsertMessageと同じです。
	const q = `INSERT INTO webhooks (channel_id, token, label, secret, bot_user_id, created_at)
	           VALUES (?, ?, ?, ?, ?, ?)`
	createdAt := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, q, in.ChannelID, in.Token, in.Label, in.Secret, in.BotUserID, createdAt)
	if err != nil {
		var mErr *mysql.MySQLError
		if errors.As(err, &mErr) && mErr.Number == 1062 {
			return domain.Webhook{}, ErrDuplicate
		}
		return domain.Webhook{}, fmt.Errorf("create webhook: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.Webhook{}, fmt.Errorf("last id: %w", err)
	}
	return domain.Webhook{
		ID:        id,
		ChannelID: in.ChannelID,
		Token:     in.Token,
		Label:     in.Label,
		BotUserID: in.BotUserID,
		CreatedAt: createdAt,
	}, nil
}

func (s *Store) validateWebhookBot(ctx context.Context, botUserID, channelID int64) error {
	const q = `SELECT 1
	             FROM users u
	             JOIN memberships m ON m.user_id = u.id
	            WHERE u.id = ?
	              AND u.is_bot = TRUE
	              AND m.channel_id = ?
	            LIMIT 1`
	var v int
	err := s.db.QueryRowContext(ctx, q, botUserID, channelID).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("validate webhook bot: %w", err)
	}
	return nil
}

// FindWebhookChannelID Webhookが紐づくchannel IDを返します。
// 削除前のMembership検査に使います。
func (s *Store) FindWebhookChannelID(ctx context.Context, id int64) (int64, error) {
	var channelID int64
	err := s.db.QueryRowContext(ctx, `SELECT channel_id FROM webhooks WHERE id = ?`, id).Scan(&channelID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("find webhook channel: %w", err)
	}
	return channelID, nil
}

// DeleteWebhook Webhookを削除します。
func (s *Store) DeleteWebhook(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete webhook rows: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}
