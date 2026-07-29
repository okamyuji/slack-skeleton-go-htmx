package transport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/hub"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/render"
)

func TestFragmentForPlainMessage(t *testing.T) {
	t.Parallel()

	r, err := render.New()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	frag, err := fragmentForMessage(r, domain.Message{ID: 1, ChannelID: 10, Body: "hello"})
	if err != nil {
		t.Fatalf("fragment: %v", err)
	}
	s := string(frag)
	if !strings.Contains(s, `id="messages-10"`) {
		t.Fatalf("oob id missing: %s", s)
	}
	if !strings.Contains(s, `hx-swap-oob="beforeend"`) {
		t.Fatalf("oob attr missing: %s", s)
	}
	if strings.Contains(s, "toast") {
		t.Fatalf("toast出現 unexpected: %s", s)
	}
}

func TestFragmentIncludesToastWhenMentionPresent(t *testing.T) {
	t.Parallel()

	r, err := render.New()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	frag, err := fragmentForMessage(r, domain.Message{ID: 2, ChannelID: 11, Body: "@alice こんにちは"})
	if err != nil {
		t.Fatalf("fragment: %v", err)
	}
	s := string(frag)
	if !strings.Contains(s, `id="toast-area"`) {
		t.Fatalf("toast outerが見当たりません: %s", s)
	}
	if !strings.Contains(s, "新着メンション") {
		t.Fatalf("toast本文が見当たりません: %s", s)
	}
}

func TestParseChannelIDs(t *testing.T) {
	t.Parallel()

	got := parseChannelIDs("1,2, 3 ,abc, ")
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("got=%v", got)
	}
}

func TestTruncateUnicodeSafe(t *testing.T) {
	t.Parallel()

	got := truncate("あいうえおかきくけこ", 5)
	if got != "あいうえお..." {
		t.Fatalf("got=%q", got)
	}
}

func TestIsExpectedCloseRecognizesCloseError(t *testing.T) {
	t.Parallel()

	if isExpectedClose(nil) {
		t.Fatal("nilを期待しない値で受け入れています")
	}
	ce := websocket.CloseError{Code: websocket.StatusNormalClosure}
	if !isExpectedClose(ce) {
		t.Fatal("CloseError")
	}
	if !isExpectedClose(context.Canceled) {
		t.Fatal("context.Canceled")
	}
	if !isExpectedClose(context.DeadlineExceeded) {
		t.Fatal("DeadlineExceeded")
	}
	if isExpectedClose(errors.New("other")) {
		t.Fatal("無関係なエラーをexpectedとしてしまっています")
	}
}

// allowAllMembers 全員をメンバー扱いするMembership検査のテストダブルです。
type allowAllMembers struct{}

func (allowAllMembers) IsMember(_ context.Context, _, _ int64) (bool, error) { return true, nil }

func TestWSHandlerEndToEnd(t *testing.T) {
	t.Parallel()

	r, err := render.New()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	h := hub.New()
	deps := Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Renderer: r,
		Hub:      h,
		Members:  allowAllMembers{},
	}

	server := httptest.NewServer(wsHandler(deps))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/?channel_ids=42"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// 購読が成立するまで小さく待ちます
	deadline := time.Now().Add(2 * time.Second)
	for h.SubscriberCount(42) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.SubscriberCount(42) != 1 {
		t.Fatalf("subscriber not registered (count=%d)", h.SubscriberCount(42))
	}

	// HubからPublishしてWS受信を確認します
	go h.Publish(context.Background(), 42, []byte(`<div id="messages-42">payload</div>`))

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "messages-42") {
		t.Fatalf("payload=%s", string(data))
	}
}

func TestWSHandlerRejectsMissingChannelIDs(t *testing.T) {
	t.Parallel()

	deps := Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Hub:    hub.New(),
	}
	server := httptest.NewServer(wsHandler(deps))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatal("err: nil (BadRequestを期待)")
	}
	if resp == nil || resp.StatusCode != 400 {
		t.Fatalf("status=%v", resp)
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func TestFragmentForMessageEmbedsBody(t *testing.T) {
	t.Parallel()

	r, err := render.New()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got, err := fragmentForMessage(r, domain.Message{ID: 5, ChannelID: 7, Body: "あい"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(string(got), "あい") {
		t.Fatalf("body missing: %s", got)
	}
}

// TestWSHandlerSurvivesServerTimeouts 本番のhttp.Serverと同様に
// ReadTimeout/WriteTimeoutを設定したサーバーで、そのタイムアウト時刻を
// 越えた後もWebSocket配信が届くことを確認します。Hijack後に残る
// deadlineを解除しないと、このテストは読み取りエラーで失敗します。
func TestWSHandlerSurvivesServerTimeouts(t *testing.T) {
	t.Parallel()

	r, err := render.New()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	h := hub.New()
	deps := Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Renderer: r,
		Hub:      h,
		Members:  allowAllMembers{},
	}

	server := httptest.NewUnstartedServer(wsHandler(deps))
	server.Config.ReadTimeout = 400 * time.Millisecond
	server.Config.WriteTimeout = 400 * time.Millisecond
	server.Start()
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/?channel_ids=7"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	deadline := time.Now().Add(2 * time.Second)
	for h.SubscriberCount(7) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.SubscriberCount(7) != 1 {
		t.Fatalf("subscriber not registered (count=%d)", h.SubscriberCount(7))
	}

	// Read/WriteTimeoutの期限を確実に越えてから配信します。
	time.Sleep(time.Second)

	go h.Publish(context.Background(), 7, []byte(`<div id="messages-7">after timeout</div>`))

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read after server timeout: %v", err)
	}
	if !strings.Contains(string(data), "after timeout") {
		t.Fatalf("payload=%s", string(data))
	}
}
