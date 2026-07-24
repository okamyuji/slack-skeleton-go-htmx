// membershipsテーブルへのアクセスをまとめます。
// 参加関係は真偽の判定でしか使わないため、対応するdomainの型は置きません。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// IsMember userがchannelに参加しているかを判定します。
func (s *Store) IsMember(ctx context.Context, userID, channelID int64) (bool, error) {
	const q = `SELECT 1 FROM memberships WHERE user_id = ? AND channel_id = ? LIMIT 1`
	var v int
	if err := s.db.QueryRowContext(ctx, q, userID, channelID).Scan(&v); err != nil {
		// 参加していないことは異常ではないので、ErrNoRowsだけは偽として返します。
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("is member: %w", err)
	}
	return true, nil
}
