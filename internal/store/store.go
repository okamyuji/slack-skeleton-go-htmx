// Package store MySQLへのデータアクセスを薄くラップします。
// 教材として骨格を見せるためORMは使わず、database/sqlだけで構成します。
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

// ErrDuplicate 冪等性キーの一意制約に違反したことを表します。
var ErrDuplicate = errors.New("store: duplicate")

// ErrNotFound 指定された条件に一致するレコードが存在しないことを表します。
var ErrNotFound = errors.New("store: not found")

// webhookBotDisplayName は migrations/0003_webhooks.up.sql が作成するbotの表示名と対応します。
const webhookBotDisplayName = "webhook-bot"

// Open DSNからMySQLへの接続を開きます。
// 設定は最小限で、本記事の範囲では本番チューニングを行いません。
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(20)
	db.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}

// Store クエリの集合を1つに束ねた薄いリポジトリです。
type Store struct {
	db *sql.DB
}

// scanRows rowsの走査、値の詰め替え、rows.Err()の確認をまとめます。
// 各クエリメソッドは1行ぶんのScanだけをscanに渡せばよく、走査の定型を
// クエリの数だけ書き写さずに済みます。呼び出し側はrows.Close()の責任を持ちます。
func scanRows[T any](rows *sql.Rows, scan func(*sql.Rows) (T, error)) ([]T, error) {
	var out []T
	for rows.Next() {
		value, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

// CreateWebhookInput Webhook作成に必要な値です。
type CreateWebhookInput struct {
	ChannelID int64
	Token     string
	Label     string
	Secret    string
	BotUserID int64
}

// New DBハンドルを保持したStoreを返します。
func New(db *sql.DB) *Store { return &Store{db: db} }

// DB 外部からのテスト用に内部ハンドルを返します。
func (s *Store) DB() *sql.DB { return s.db }

// ListChannelsByWorkspace 対象Workspaceのチャンネルを名前順で返します。
func (s *Store) ListChannelsByWorkspace(ctx context.Context, workspaceID int64) ([]domain.Channel, error) {
	const q = `SELECT id, workspace_id, name, created_at
	             FROM channels
	            WHERE workspace_id = ?
	            ORDER BY name ASC`
	rows, err := s.db.QueryContext(ctx, q, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRows(rows, func(rows *sql.Rows) (domain.Channel, error) {
		var c domain.Channel
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.CreatedAt); err != nil {
			return domain.Channel{}, err
		}
		return c, nil
	})
}

// FindWorkspaceName Workspaceの表示名を返します。
func (s *Store) FindWorkspaceName(ctx context.Context, workspaceID int64) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM workspaces WHERE id = ?`, workspaceID).Scan(&name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("find workspace: %w", err)
	}
	return name, nil
}

// ListChannelsForUser 対象Workspaceのうち、userが参加しているチャンネルだけを名前順で返します。
// Snapshotや購読の読み取り境界はこのMembership結合で導出します。
func (s *Store) ListChannelsForUser(ctx context.Context, workspaceID, userID int64) ([]domain.Channel, error) {
	const q = `SELECT c.id, c.workspace_id, c.name, c.created_at
	             FROM channels c
	             JOIN memberships m ON m.channel_id = c.id
	            WHERE c.workspace_id = ?
	              AND m.user_id = ?
	            ORDER BY c.name ASC`
	rows, err := s.db.QueryContext(ctx, q, workspaceID, userID)
	if err != nil {
		return nil, fmt.Errorf("list channels for user: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRows(rows, func(rows *sql.Rows) (domain.Channel, error) {
		var c domain.Channel
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.CreatedAt); err != nil {
			return domain.Channel{}, err
		}
		return c, nil
	})
}

// ListUsersByWorkspace 対象Workspaceのユーザーを表示名順で返します。
func (s *Store) ListUsersByWorkspace(ctx context.Context, workspaceID int64) ([]domain.User, error) {
	const q = `SELECT id, workspace_id, display_name, is_bot, created_at
	             FROM users
	            WHERE workspace_id = ?
	            ORDER BY display_name ASC`
	rows, err := s.db.QueryContext(ctx, q, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRows(rows, func(rows *sql.Rows) (domain.User, error) {
		var u domain.User
		if err := rows.Scan(&u.ID, &u.WorkspaceID, &u.DisplayName, &u.IsBot, &u.CreatedAt); err != nil {
			return domain.User{}, err
		}
		return u, nil
	})
}

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
	const q = `INSERT INTO webhooks (channel_id, token, label, secret, bot_user_id)
	           VALUES (?, ?, ?, ?, ?)`
	res, err := s.db.ExecContext(ctx, q, in.ChannelID, in.Token, in.Label, in.Secret, in.BotUserID)
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
		CreatedAt: time.Now().UTC(),
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

// RecentMessages channelIDの直近メッセージをid降順で最大limit件返します。
// 表示は時系列昇順で行うため、呼び出し側で並べ替える前提です。
func (s *Store) RecentMessages(ctx context.Context, channelID int64, limit int) ([]domain.Message, error) {
	return s.MessagesBefore(ctx, channelID, 0, limit)
}

// MessagesBefore カーソル(beforeID)より古いメッセージをid降順で返します。
// beforeIDが0のときは最新分から取得します。
func (s *Store) MessagesBefore(ctx context.Context, channelID, beforeID int64, limit int) ([]domain.Message, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if beforeID > 0 {
		const q = `SELECT id, channel_id, user_id, body, client_msg_id, created_at
		             FROM messages
		            WHERE channel_id = ? AND id < ?
		            ORDER BY id DESC
		            LIMIT ?`
		rows, err = s.db.QueryContext(ctx, q, channelID, beforeID, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, channel_id, user_id, body, client_msg_id, created_at
			   FROM messages
			  WHERE channel_id = ?
			  ORDER BY id DESC
			  LIMIT ?`, channelID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("messages before: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRows(rows, func(rows *sql.Rows) (domain.Message, error) {
		var m domain.Message
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.UserID, &m.Body, &m.ClientMsgID, &m.CreatedAt); err != nil {
			return domain.Message{}, err
		}
		return m, nil
	})
}

// InsertMessage 新規メッセージを保存し、採番されたIDと作成時刻を返します。
// 同じ(channelID, clientMsgID)が既に存在する場合は ErrDuplicate を返します。
func (s *Store) InsertMessage(ctx context.Context, in domain.Message) (domain.Message, error) {
	const q = `INSERT INTO messages (channel_id, user_id, body, client_msg_id)
	           VALUES (?, ?, ?, ?)`
	res, err := s.db.ExecContext(ctx, q, in.ChannelID, in.UserID, in.Body, in.ClientMsgID)
	if err != nil {
		var mErr *mysql.MySQLError
		if errors.As(err, &mErr) && mErr.Number == 1062 {
			return domain.Message{}, ErrDuplicate
		}
		return domain.Message{}, fmt.Errorf("insert message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.Message{}, fmt.Errorf("last id: %w", err)
	}
	out := in
	out.ID = id
	out.CreatedAt = time.Now().UTC()
	return out, nil
}

// FindMessageByClientMsgID 冪等性キーの組み合わせでメッセージを1件取得します。
// (channel_id, client_msg_id)のユニークインデックスがそのまま効きます。
func (s *Store) FindMessageByClientMsgID(ctx context.Context, channelID int64, clientMsgID string) (domain.Message, error) {
	const q = `SELECT id, channel_id, user_id, body, client_msg_id, created_at
	             FROM messages
	            WHERE channel_id = ? AND client_msg_id = ?`
	var m domain.Message
	err := s.db.QueryRowContext(ctx, q, channelID, clientMsgID).
		Scan(&m.ID, &m.ChannelID, &m.UserID, &m.Body, &m.ClientMsgID, &m.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Message{}, ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("find by client_msg_id: %w", err)
	}
	return m, nil
}

// IsMember userがchannelに参加しているかを判定します。
func (s *Store) IsMember(ctx context.Context, userID, channelID int64) (bool, error) {
	const q = `SELECT 1 FROM memberships WHERE user_id = ? AND channel_id = ? LIMIT 1`
	var v int
	if err := s.db.QueryRowContext(ctx, q, userID, channelID).Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// FindMessage 単一メッセージを取得します。テスト時の検証目的でも利用します。
func (s *Store) FindMessage(ctx context.Context, id int64) (domain.Message, error) {
	const q = `SELECT id, channel_id, user_id, body, client_msg_id, created_at
	             FROM messages WHERE id = ?`
	var m domain.Message
	if err := s.db.QueryRowContext(ctx, q, id).
		Scan(&m.ID, &m.ChannelID, &m.UserID, &m.Body, &m.ClientMsgID, &m.CreatedAt); err != nil {
		return domain.Message{}, err
	}
	return m, nil
}
