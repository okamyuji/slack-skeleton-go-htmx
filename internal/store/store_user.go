// usersテーブルへのアクセスをまとめます。
package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
)

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
