// Package domain 本記事で扱う最小ドメインモデルを定義します。
// Workspace、User、Channel、Message、Webhook、WebhookWithSecret、
// WebhookSettingの7型のみで、認証や認可といった本記事のスコープ外の
// 責務は持ちません。
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
	IsBot       bool
	CreatedAt   time.Time
}

// Channel Workspace内の話題ごとの部屋を表します。
type Channel struct {
	ID          int64
	WorkspaceID int64
	Name        string
	CreatedAt   time.Time
}

// memberships テーブルに対応するGoの型は置きません。
// 参加関係は「参加しているか」の真偽と、一覧を絞り込むJOINでしか使わず、
// 1行ぶんを値として持ち回る場面がないためです。
// 判定は store.IsMember、絞り込みは各クエリのJOINが担います。

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

// Webhook 外部サービスから投稿を受け付けるIncoming Webhookを表します。
// Secretは通常の参照で漏らさないため、この型には含めません。
type Webhook struct {
	ID        int64
	ChannelID int64
	Token     string
	Label     string
	BotUserID int64
	CreatedAt time.Time
}

// WebhookWithSecret 署名検証が必要なサービス層へだけSecretを渡す型です。
type WebhookWithSecret struct {
	Webhook
	Secret string
}

// WebhookSetting 管理画面に表示するWebhook設定です。
type WebhookSetting struct {
	Webhook
	ChannelName string
	HasSecret   bool
}
