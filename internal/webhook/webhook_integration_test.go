//go:build integration

package webhook_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/message"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/migrate"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/webhook"
)

func TestWebhookIntegrationGenericPayload(t *testing.T) {
	db, cleanup := openWebhookTestDB(t)
	defer cleanup()

	st := store.New(db)
	token := seedWebhookFixture(t, db)
	svc := webhook.New(st, message.New(st))

	got, duplicate, err := svc.HandlePayload(
		context.Background(),
		token,
		http.Header{},
		[]byte(`{"text":"Hello from webhook!"}`),
	)
	if err != nil {
		t.Fatalf("HandlePayload: %v", err)
	}
	if duplicate {
		t.Fatal("first delivery marked duplicate")
	}
	if got.Body != "Hello from webhook!" {
		t.Fatalf("body=%q", got.Body)
	}
	if got.ChannelID != 12 || got.UserID != 3 {
		t.Fatalf("channel=%d user=%d", got.ChannelID, got.UserID)
	}
}

func TestWebhookIntegrationGitHubPush(t *testing.T) {
	db, cleanup := openWebhookTestDB(t)
	defer cleanup()

	st := store.New(db)
	token := seedWebhookFixture(t, db)
	svc := webhook.New(st, message.New(st))

	got, _, err := svc.HandlePayload(
		context.Background(),
		token,
		http.Header{"X-Github-Event": []string{"push"}},
		[]byte(`{
			"ref":"refs/heads/main",
			"repository":{"name":"my-repo"},
			"pusher":{"name":"alice"},
			"commits":[{"id":"abc1234567","message":"Fix bug"}]
		}`),
	)
	if err != nil {
		t.Fatalf("HandlePayload: %v", err)
	}
	if !strings.Contains(got.Body, "[my-repo] alice pushed 1 commit(s) to main") {
		t.Fatalf("body missing summary: %q", got.Body)
	}
	if !strings.Contains(got.Body, "abc1234 Fix bug") {
		t.Fatalf("body missing commit: %q", got.Body)
	}
}

func TestWebhookIntegrationTokenNotFound(t *testing.T) {
	db, cleanup := openWebhookTestDB(t)
	defer cleanup()

	st := store.New(db)
	seedWebhookFixture(t, db)
	svc := webhook.New(st, message.New(st))

	_, _, err := svc.HandlePayload(context.Background(), "missing", http.Header{}, []byte(`{"text":"hello"}`))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err=%v, want store.ErrNotFound", err)
	}
}

func TestWebhookIntegrationIdempotency(t *testing.T) {
	db, cleanup := openWebhookTestDB(t)
	defer cleanup()

	st := store.New(db)
	token := seedWebhookFixture(t, db)
	svc := webhook.New(st, message.New(st))
	body := []byte(`{"ref":"refs/heads/main","repository":{"name":"demo"},"pusher":{"name":"alice"},"commits":[]}`)
	githubHeaders := func(delivery string) http.Header {
		h := http.Header{}
		h.Set("X-GitHub-Event", "push")
		h.Set("X-GitHub-Delivery", delivery)
		return h
	}

	// 同一配信のredeliveryだけが重複扱いになります。
	first, duplicate, err := svc.HandlePayload(context.Background(), token, githubHeaders("delivery-1"), body)
	if err != nil || duplicate {
		t.Fatalf("first: duplicate=%v err=%v", duplicate, err)
	}
	second, duplicate, err := svc.HandlePayload(context.Background(), token, githubHeaders("delivery-1"), body)
	if err != nil || !duplicate {
		t.Fatalf("redelivery: duplicate=%v err=%v", duplicate, err)
	}
	if first.ID != second.ID {
		t.Fatalf("ids differ: first=%d second=%d", first.ID, second.ID)
	}

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM messages WHERE channel_id = ? AND client_msg_id = ?",
		first.ChannelID,
		first.ClientMsgID,
	).Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 1 {
		t.Fatalf("message count=%d, want 1", count)
	}

	// 別配信IDなら本文が同一でも別メッセージとして保存されます。
	third, duplicate, err := svc.HandlePayload(context.Background(), token, githubHeaders("delivery-2"), body)
	if err != nil || duplicate {
		t.Fatalf("other delivery: duplicate=%v err=%v", duplicate, err)
	}
	if third.ID == first.ID {
		t.Fatal("別配信が重複として消えています")
	}
}

func TestWebhookIntegrationGenericSameBodySavesTwo(t *testing.T) {
	db, cleanup := openWebhookTestDB(t)
	defer cleanup()

	st := store.New(db)
	token := seedWebhookFixture(t, db)
	svc := webhook.New(st, message.New(st))
	body := []byte(`{"text":"定期通知: バックアップ完了"}`)

	// キー未指定の汎用形式は重複排除しません。同一本文の別イベントは2件とも残ります。
	first, dup1, err := svc.HandlePayload(context.Background(), token, http.Header{}, body)
	if err != nil || dup1 {
		t.Fatalf("first: dup=%v err=%v", dup1, err)
	}
	second, dup2, err := svc.HandlePayload(context.Background(), token, http.Header{}, body)
	if err != nil || dup2 {
		t.Fatalf("second: dup=%v err=%v", dup2, err)
	}
	if first.ID == second.ID {
		t.Fatal("同一本文の別イベントが1件に潰されています")
	}
}

func TestWebhookIntegrationGenericExplicitKeyDeduplicates(t *testing.T) {
	db, cleanup := openWebhookTestDB(t)
	defer cleanup()

	st := store.New(db)
	token := seedWebhookFixture(t, db)
	svc := webhook.New(st, message.New(st))
	body := []byte(`{"text":"リトライされる通知","client_msg_id":"caller-key-1"}`)

	first, dup1, err := svc.HandlePayload(context.Background(), token, http.Header{}, body)
	if err != nil || dup1 {
		t.Fatalf("first: dup=%v err=%v", dup1, err)
	}
	second, dup2, err := svc.HandlePayload(context.Background(), token, http.Header{}, body)
	if err != nil || !dup2 {
		t.Fatalf("second: dup=%v err=%v", dup2, err)
	}
	if first.ID != second.ID {
		t.Fatalf("明示キーの再送が同一メッセージになっていません: %d != %d", first.ID, second.ID)
	}
}

func openWebhookTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcmysql.Run(ctx,
		"mysql:8.4",
		tcmysql.WithDatabase("slack_skeleton"),
		tcmysql.WithUsername("appuser"),
		tcmysql.WithPassword("apppass"),
	)
	if err != nil {
		t.Fatalf("testcontainers: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate: %v", err)
		}
	})
	dsn, err := container.ConnectionString(ctx, "parseTime=true", "loc=UTC")
	if err != nil {
		t.Fatalf("connection string: %v", err)
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
	resetWebhookFixtures(db)
	return db, func() {
		resetWebhookFixtures(db)
		_ = db.Close()
	}
}

func seedWebhookFixture(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "INSERT INTO workspaces (id, name) VALUES (?, ?)", 1, "test-ws"); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO users (id, workspace_id, display_name, is_bot) VALUES (?, ?, ?, ?)",
		3,
		1,
		"webhook-bot",
		true,
	); err != nil {
		t.Fatalf("seed bot: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO channels (id, workspace_id, name) VALUES (?, ?, ?)",
		12,
		1,
		"dev",
	); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO memberships (user_id, channel_id) VALUES (?, ?)",
		3,
		12,
	); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	token := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := db.ExecContext(ctx,
		"INSERT INTO webhooks (channel_id, token, label, secret, bot_user_id) VALUES (?, ?, ?, ?, ?)",
		12,
		token,
		"test webhook",
		"",
		3,
	); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	return token
}

func resetWebhookFixtures(db *sql.DB) {
	_, _ = db.Exec("DELETE FROM messages")
	_, _ = db.Exec("DELETE FROM webhooks")
	_, _ = db.Exec("DELETE FROM memberships")
	_, _ = db.Exec("DELETE FROM channels")
	_, _ = db.Exec("DELETE FROM users")
	_, _ = db.Exec("DELETE FROM workspaces")
}
