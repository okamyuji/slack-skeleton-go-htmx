// workspacesテーブルへのアクセスをまとめます。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

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
