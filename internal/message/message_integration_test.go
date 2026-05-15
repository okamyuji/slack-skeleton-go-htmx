//go:build integration

package message_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/message"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/migrate"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		t.Skip("DB_DSNが未設定のためスキップします")
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := migrate.Up(ctx, db, "../../migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, _ = db.Exec("DELETE FROM messages")
	_, _ = db.Exec("DELETE FROM memberships")
	_, _ = db.Exec("DELETE FROM channels")
	_, _ = db.Exec("DELETE FROM users")
	_, _ = db.Exec("DELETE FROM workspaces")
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seed(t *testing.T, db *sql.DB) (channelID, userID int64) {
	t.Helper()
	res, err := db.Exec("INSERT INTO workspaces (name) VALUES (?)", "ws")
	if err != nil {
		t.Fatalf("ws: %v", err)
	}
	wsID, _ := res.LastInsertId()
	res, err = db.Exec("INSERT INTO users (workspace_id, display_name) VALUES (?, ?)", wsID, "alice")
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	userID, _ = res.LastInsertId()
	res, err = db.Exec("INSERT INTO channels (workspace_id, name) VALUES (?, ?)", wsID, "general")
	if err != nil {
		t.Fatalf("ch: %v", err)
	}
	channelID, _ = res.LastInsertId()
	if _, err := db.Exec("INSERT INTO memberships (user_id, channel_id) VALUES (?, ?)", userID, channelID); err != nil {
		t.Fatalf("ms: %v", err)
	}
	return
}

func TestSendInsertsAndIsIdempotent(t *testing.T) {
	db := openDB(t)
	channelID, userID := seed(t, db)
	svc := message.New(store.New(db))

	first, dup, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: channelID, UserID: userID, Body: "こんにちは", ClientMsgID: "abc",
	})
	if err != nil || dup {
		t.Fatalf("first: err=%v dup=%v", err, dup)
	}
	if first.ID == 0 {
		t.Fatal("ID未採番")
	}

	second, dup2, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: channelID, UserID: userID, Body: "こんにちは", ClientMsgID: "abc",
	})
	if err != nil {
		t.Fatalf("second: err=%v", err)
	}
	if !dup2 {
		t.Fatal("二回目は duplicate=true を期待します")
	}
	if second.ID != first.ID {
		t.Fatalf("同一IDを期待 first=%d second=%d", first.ID, second.ID)
	}
}

func TestSendRejectsNonMember(t *testing.T) {
	db := openDB(t)
	channelID, _ := seed(t, db)
	svc := message.New(store.New(db))

	// 別ユーザーを作って参加させずに送信を試みます。
	res, err := db.Exec("INSERT INTO users (workspace_id, display_name) VALUES ((SELECT id FROM workspaces LIMIT 1), ?)", "bob")
	if err != nil {
		t.Fatalf("seed bob: %v", err)
	}
	bobID, _ := res.LastInsertId()

	_, _, err = svc.Send(context.Background(), message.SendInput{
		ChannelID: channelID, UserID: bobID, Body: "hi", ClientMsgID: "x",
	})
	if !errors.Is(err, message.ErrNotMember) {
		t.Fatalf("want ErrNotMember, got %v", err)
	}
}

func TestHistoryReturnsLatestFirst(t *testing.T) {
	db := openDB(t)
	channelID, userID := seed(t, db)
	svc := message.New(store.New(db))

	for i := 0; i < 5; i++ {
		_, _, err := svc.Send(context.Background(), message.SendInput{
			ChannelID: channelID, UserID: userID, Body: "msg", ClientMsgID: time.Now().Format("150405.000000000") + "-" + string(rune('a'+i)),
		})
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		time.Sleep(time.Millisecond)
	}
	first, err := svc.History(context.Background(), channelID, 0, 3)
	if err != nil || len(first) != 3 {
		t.Fatalf("first: err=%v len=%d", err, len(first))
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].ID < first[i].ID {
			t.Fatal("id降順ではありません")
		}
	}
	second, err := svc.History(context.Background(), channelID, first[len(first)-1].ID, 3)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(second) == 0 || second[0].ID >= first[len(first)-1].ID {
		t.Fatalf("カーソル境界が崩れています: %+v", second)
	}
}
