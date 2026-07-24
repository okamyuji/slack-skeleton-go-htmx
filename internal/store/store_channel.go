// channelsテーブルへのアクセスをまとめます。
// 読み取りはmembershipsとの結合で参加チャンネルだけに絞ります。
package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
)

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
