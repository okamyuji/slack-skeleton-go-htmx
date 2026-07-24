//go:build integration

package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
)

func seedBasicFixture(t *testing.T, db *sql.DB) (workspaceID, userID, channelID int64) {
	t.Helper()
	ctx := context.Background()
	res, err := db.ExecContext(ctx, "INSERT INTO workspaces (name) VALUES (?)", "test-ws")
	if err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	workspaceID, _ = res.LastInsertId()

	res, err = db.ExecContext(ctx,
		"INSERT INTO users (workspace_id, display_name) VALUES (?, ?)", workspaceID, "alice")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userID, _ = res.LastInsertId()

	res, err = db.ExecContext(ctx,
		"INSERT INTO channels (workspace_id, name) VALUES (?, ?)", workspaceID, "general")
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	channelID, _ = res.LastInsertId()

	if _, err := db.ExecContext(ctx,
		"INSERT INTO memberships (user_id, channel_id) VALUES (?, ?)", userID, channelID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	return
}

func TestInsertMessageAndRetrieve(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	_, userID, channelID := seedBasicFixture(t, db)
	s := store.New(db)

	in := domain.Message{
		ChannelID:   channelID,
		UserID:      userID,
		Body:        "こんにちは",
		ClientMsgID: "demo-001",
	}
	got, err := s.InsertMessage(context.Background(), in)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got.ID == 0 {
		t.Fatal("採番されたIDが0です")
	}

	found, err := s.FindMessage(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found.Body != in.Body || found.ClientMsgID != in.ClientMsgID {
		t.Fatalf("got %+v, want body=%q client=%q", found, in.Body, in.ClientMsgID)
	}
}

func TestDuplicateClientMsgIDIsRejected(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	_, userID, channelID := seedBasicFixture(t, db)
	s := store.New(db)

	in := domain.Message{
		ChannelID:   channelID,
		UserID:      userID,
		Body:        "重複送信",
		ClientMsgID: "dup-001",
	}
	if _, err := s.InsertMessage(context.Background(), in); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err := s.InsertMessage(context.Background(), in)
	if !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("二回目のINSERTで ErrDuplicate を期待しましたが %v", err)
	}
}

func TestMessagesBeforeCursor(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	_, userID, channelID := seedBasicFixture(t, db)
	s := store.New(db)

	for i := 0; i < 5; i++ {
		_, err := s.InsertMessage(context.Background(), domain.Message{
			ChannelID:   channelID,
			UserID:      userID,
			Body:        "message",
			ClientMsgID: "msg-" + time.Now().Format("150405.000000000"),
		})
		if err != nil {
			t.Fatalf("seed msg %d: %v", i, err)
		}
		time.Sleep(time.Millisecond)
	}

	first, err := s.MessagesBefore(context.Background(), channelID, 0, 3)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("first page size=%d, want 3", len(first))
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].ID < first[i].ID {
			t.Fatal("id降順になっていません")
		}
	}

	second, err := s.MessagesBefore(context.Background(), channelID, first[len(first)-1].ID, 3)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second) == 0 {
		t.Fatal("二ページ目が空です")
	}
	if second[0].ID >= first[len(first)-1].ID {
		t.Fatal("カーソルより新しいIDが返っています")
	}
}

func TestListUsersByWorkspace(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	workspaceID, _, _ := seedBasicFixture(t, db)
	if _, err := db.Exec("INSERT INTO users (workspace_id, display_name) VALUES (?, ?)", workspaceID, "bob"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	s := store.New(db)
	users, err := s.ListUsersByWorkspace(context.Background(), workspaceID)
	if err != nil || len(users) != 2 {
		t.Fatalf("users: err=%v len=%d", err, len(users))
	}
	if users[0].DisplayName != "alice" || users[1].DisplayName != "bob" {
		t.Fatalf("users unsorted: %+v", users)
	}
}

func TestListChannelsForUserFiltersByMembership(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	workspaceID, userID, _ := seedBasicFixture(t, db)
	// メンバーでないチャンネルを追加します
	if _, err := db.Exec("INSERT INTO channels (workspace_id, name) VALUES (?, ?)", workspaceID, "random"); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	s := store.New(db)
	channels, err := s.ListChannelsForUser(context.Background(), workspaceID, userID)
	if err != nil {
		t.Fatalf("list for user: %v", err)
	}
	if len(channels) != 1 || channels[0].Name != "general" {
		t.Fatalf("参加チャンネルだけを期待しましたが: %+v", channels)
	}

	// 非参加ユーザーには何も返しません
	channels, err = s.ListChannelsForUser(context.Background(), workspaceID, 9999)
	if err != nil || len(channels) != 0 {
		t.Fatalf("non-member: err=%v channels=%+v", err, channels)
	}
}

func TestRecentMessagesAndIsMember(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	_, userID, channelID := seedBasicFixture(t, db)
	s := store.New(db)

	ok, err := s.IsMember(context.Background(), userID, channelID)
	if err != nil || !ok {
		t.Fatalf("is member: err=%v ok=%v", err, ok)
	}
	ok, err = s.IsMember(context.Background(), 9999, channelID)
	if err != nil || ok {
		t.Fatalf("non-member: err=%v ok=%v", err, ok)
	}

	for i := 0; i < 3; i++ {
		if _, err := s.InsertMessage(context.Background(), domain.Message{
			ChannelID: channelID, UserID: userID, Body: "m", ClientMsgID: time.Now().Format("150405.000000000"),
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		time.Sleep(time.Millisecond)
	}

	recent, err := s.RecentMessages(context.Background(), channelID, 5)
	if err != nil || len(recent) != 3 {
		t.Fatalf("recent: err=%v len=%d", err, len(recent))
	}
	if recent[0].ID < recent[1].ID {
		t.Fatal("id降順ではありません")
	}
}

func TestWebhookSettingsCRUD(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	workspaceID, userID, channelID := seedBasicFixture(t, db)
	botUserID := seedWebhookBot(t, db, workspaceID, channelID)
	s := store.New(db)
	token := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	created, err := s.CreateWebhook(context.Background(), store.CreateWebhookInput{
		ChannelID: channelID,
		Token:     token,
		Label:     "GitHub main",
		Secret:    "top-secret",
		BotUserID: botUserID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("created id is zero")
	}

	settings, err := s.ListWebhookSettingsForUser(context.Background(), workspaceID, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(settings) != 1 {
		t.Fatalf("settings len=%d", len(settings))
	}
	if settings[0].Label != "GitHub main" || settings[0].Token != token || !settings[0].HasSecret {
		t.Fatalf("settings=%+v", settings[0])
	}

	// tokenを含む一覧は非メンバーには返しません
	nonMember, err := s.ListWebhookSettingsForUser(context.Background(), workspaceID, 9999)
	if err != nil || len(nonMember) != 0 {
		t.Fatalf("non-member list: err=%v settings=%+v", err, nonMember)
	}

	gotChannelID, err := s.FindWebhookChannelID(context.Background(), created.ID)
	if err != nil || gotChannelID != channelID {
		t.Fatalf("find webhook channel: err=%v got=%d want=%d", err, gotChannelID, channelID)
	}

	if err := s.DeleteWebhook(context.Background(), created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteWebhook(context.Background(), created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: want ErrNotFound, got %v", err)
	}
	if _, err := s.FindWebhookChannelID(context.Background(), created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("find after delete: want ErrNotFound, got %v", err)
	}
	settings, err = s.ListWebhookSettingsForUser(context.Background(), workspaceID, userID)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(settings) != 0 {
		t.Fatalf("settings after delete=%+v", settings)
	}
}

func seedWebhookBot(t *testing.T, db *sql.DB, workspaceID, channelID int64) int64 {
	t.Helper()
	res, err := db.ExecContext(context.Background(),
		"INSERT INTO users (workspace_id, display_name, is_bot) VALUES (?, ?, ?)",
		workspaceID,
		"webhook-bot",
		true,
	)
	if err != nil {
		t.Fatalf("seed bot: %v", err)
	}
	botUserID, _ := res.LastInsertId()
	if _, err := db.ExecContext(context.Background(),
		"INSERT INTO memberships (user_id, channel_id) VALUES (?, ?)",
		botUserID,
		channelID,
	); err != nil {
		t.Fatalf("seed bot membership: %v", err)
	}
	return botUserID
}
