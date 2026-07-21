//go:build integration

package message_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/message"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/migrate"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcmysql.Run(ctx, "mysql:8.4",
		tcmysql.WithDatabase("slack_skeleton"),
		tcmysql.WithUsername("appuser"),
		tcmysql.WithPassword("apppass"),
	)
	if err != nil {
		t.Fatalf("testcontainers: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	dsn, err := container.ConnectionString(ctx, "parseTime=true", "loc=UTC")
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}

	db, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := migrate.Up(ctx, db, "../../migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
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

func TestSendInsertsAndIsIdempotent_Integration(t *testing.T) {
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

func TestSendRejectsNonMember_Integration(t *testing.T) {
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

func TestSendConflictOnDifferentBody_Integration(t *testing.T) {
	db := openDB(t)
	channelID, userID := seed(t, db)
	svc := message.New(store.New(db))

	if _, _, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: channelID, UserID: userID, Body: "原本", ClientMsgID: "conflict-int-key",
	}); err != nil {
		t.Fatalf("first send: %v", err)
	}
	_, _, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: channelID, UserID: userID, Body: "編集後", ClientMsgID: "conflict-int-key",
	})
	if !errors.Is(err, message.ErrIdempotencyConflict) {
		t.Fatalf("want ErrIdempotencyConflict, got %v", err)
	}
}

func TestSendDuplicateAfterManyNewerMessages_Integration(t *testing.T) {
	db := openDB(t)
	channelID, userID := seed(t, db)
	svc := message.New(store.New(db))

	first, _, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: channelID, UserID: userID, Body: "最初の投稿", ClientMsgID: "delayed-retry-key",
	})
	if err != nil {
		t.Fatalf("first send: %v", err)
	}

	// 冪等キーの解決が「直近N件の線形検索」だと、N件を超える新規投稿の後の
	// リトライで原本を見失います。ユニークキー直接SELECTならこの回帰は起きません。
	for i := 0; i < 120; i++ {
		if _, err := db.Exec(
			"INSERT INTO messages (channel_id, user_id, body, client_msg_id) VALUES (?, ?, ?, ?)",
			channelID, userID, "埋め草", fmt.Sprintf("filler-%03d", i),
		); err != nil {
			t.Fatalf("filler %d: %v", i, err)
		}
	}

	retried, dup, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: channelID, UserID: userID, Body: "最初の投稿", ClientMsgID: "delayed-retry-key",
	})
	if err != nil {
		t.Fatalf("delayed retry: %v", err)
	}
	if !dup || retried.ID != first.ID {
		t.Fatalf("dup=%v retried.ID=%d want %d", dup, retried.ID, first.ID)
	}
}

func TestHistoryReturnsLatestFirst_Integration(t *testing.T) {
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
	first, err := svc.History(context.Background(), userID, channelID, 0, 3)
	if err != nil || len(first) != 3 {
		t.Fatalf("first: err=%v len=%d", err, len(first))
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].ID < first[i].ID {
			t.Fatal("id降順ではありません")
		}
	}
	second, err := svc.History(context.Background(), userID, channelID, first[len(first)-1].ID, 3)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(second) == 0 || second[0].ID >= first[len(first)-1].ID {
		t.Fatalf("カーソル境界が崩れています: %+v", second)
	}
}
