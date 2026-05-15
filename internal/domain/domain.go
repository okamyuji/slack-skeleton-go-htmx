// Package domain 本記事で扱う最小ドメインモデルを定義します。
// Workspace、User、Channel、Membership、Messageの5型のみで、
// 認証や認可といった本記事のスコープ外の責務は持ちません。
package domain

import "time"

// Workspace 1つの組織や個人の作業空間を表します。
type Workspace struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

// User Workspaceに属する利用者を表します。
type User struct {
	ID          int64
	WorkspaceID int64
	DisplayName string
	CreatedAt   time.Time
}

// Channel Workspace内の話題ごとの部屋を表します。
type Channel struct {
	ID          int64
	WorkspaceID int64
	Name        string
	CreatedAt   time.Time
}

// Membership UserがChannelに参加していることを表します。
type Membership struct {
	UserID    int64
	ChannelID int64
	JoinedAt  time.Time
}

// Message 1件の発言を表します。
// ClientMsgIDはクライアントが送信時に生成する冪等性キーで、
// channel_idとの複合UNIQUE制約により重複INSERTを弾きます。
type Message struct {
	ID          int64
	ChannelID   int64
	UserID      int64
	Body        string
	ClientMsgID string
	CreatedAt   time.Time
}
