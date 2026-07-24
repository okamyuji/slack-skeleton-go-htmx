// messagesテーブルへのアクセスをまとめます。
// 一覧はid降順で返し、時系列昇順への並べ替えは呼び出し側が行います。
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
//
// created_atはDBのDEFAULTに任せず、アプリで決めた値を明示的に渡します。
// DEFAULTに任せると、ここで返す時刻はアプリが別途採った現在時刻になり、
// 実際に保存された値と一致する保証がありません。送信直後にWebSocketで配る
// フラグメントと、あとで履歴を読み直したときの表示で時刻がずれます。
// 明示的に渡せば、再SELECTを足さずに両者を必ず一致させられます。
//
// 渡す前にマイクロ秒へ丸めます。time.Nowはナノ秒まで持ちますが、
// 列の型はDATETIME(6)でマイクロ秒までしか保持しないため、丸めずに渡すと
// 返した値と保存された値が末尾の桁でずれます。時刻の分解能はOSによって
// 違うので、丸めないと環境によって再現したりしなかったりします。
func (s *Store) InsertMessage(ctx context.Context, in domain.Message) (domain.Message, error) {
	const q = `INSERT INTO messages (channel_id, user_id, body, client_msg_id, created_at)
	           VALUES (?, ?, ?, ?, ?)`
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	res, err := s.db.ExecContext(ctx, q, in.ChannelID, in.UserID, in.Body, in.ClientMsgID, createdAt)
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
	out.CreatedAt = createdAt
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

// FindMessage 単一メッセージをIDで取得します。
// 本番の経路からは呼びません。統合テストがINSERTの結果を確かめるために使います。
// 該当がない場合は他のfinderと同じくErrNotFoundを返します。
func (s *Store) FindMessage(ctx context.Context, id int64) (domain.Message, error) {
	const q = `SELECT id, channel_id, user_id, body, client_msg_id, created_at
	             FROM messages WHERE id = ?`
	var m domain.Message
	if err := s.db.QueryRowContext(ctx, q, id).
		Scan(&m.ID, &m.ChannelID, &m.UserID, &m.Body, &m.ClientMsgID, &m.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Message{}, ErrNotFound
		}
		return domain.Message{}, fmt.Errorf("find message: %w", err)
	}
	return m, nil
}
