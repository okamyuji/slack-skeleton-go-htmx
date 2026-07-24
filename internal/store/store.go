// Package store MySQLへのデータアクセスを薄くラップします。
// 教材として骨格を見せるためORMは使わず、database/sqlだけで構成します。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
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
