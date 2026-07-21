// このファイルはブラウザ相当の経路を通す結合テストを置きます。
// レンダリングした実フォームの配線を検証し、フォームPOSTからWebSocket受信までを往復させます。
package transport_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/hub"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/transport"
)

// postForm X-User-Idヘッダ付きでフォームPOSTを行うヘルパーです。
func postForm(t *testing.T, serverURL, path string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, serverURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Id", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return resp
}

// dialWS WebSocketを購読し、Hubへの登録完了まで待つヘルパーです。
func dialWS(ctx context.Context, t *testing.T, serverURL string, h *hub.Hub, channelID int64) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + fmt.Sprintf("/ws?channel_ids=%d", channelID)
	before := h.SubscriberCount(channelID)
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for h.SubscriberCount(channelID) <= before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.SubscriberCount(channelID) <= before {
		t.Fatalf("subscriber not registered (count=%d)", h.SubscriberCount(channelID))
	}
	return conn
}

func TestBrowserFormPostToWebSocketRoundTrip(t *testing.T) {
	t.Parallel()

	deps, repo, h := newFullDeps(t)
	repo.members[[2]int64{1, 10}] = true

	server := httptest.NewServer(transport.NewMux(deps))
	defer server.Close()

	// 1. ブラウザが最初に受け取るページを実際にレンダリングし、
	//    フォームにclient_msg_idの自動生成が配線されていることを確認します。
	pageResp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	pageBytes, err := io.ReadAll(pageResp.Body)
	_ = pageResp.Body.Close()
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	page := string(pageBytes)
	for _, want := range []string{
		`hx-post="/channels/10/messages"`,
		`name="client_msg_id"`,
		`crypto.randomUUID`,
		`hx-on::config-request`,
		// form.reset()はhidden入力を戻さないため、成功時の明示クリアが必須です
		`this.elements.client_msg_id.value = ''`,
		// 本文編集時はキーを破棄し、次の送信を新規メッセージとして扱います
		`oninput="this.form.elements.client_msg_id.value = ''"`,
		// 履歴読み込みUIの配線も実ページに存在することを固定します
		`過去のメッセージを読み込む`,
		`hx-swap="afterbegin"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("rendered form missing %q", want)
		}
	}

	// 2. WebSocketで購読します。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialWS(ctx, t, server.URL, h, 10)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	// 3. レンダリングされたフォームと同じエンドポイントへPOSTし、WS側で受信します。
	form := url.Values{"body": {"こんにちはE2E"}, "client_msg_id": {"e2e-round-trip-1"}}
	resp := postForm(t, server.URL, "/channels/10/messages", form)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("post status=%d", resp.StatusCode)
	}

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "messages-10") || !strings.Contains(got, "こんにちはE2E") {
		t.Fatalf("ws payload=%s", got)
	}
}

func TestWSRejectsNonMemberSubscription(t *testing.T) {
	t.Parallel()

	deps, _, _ := newFullDeps(t) // Membershipは空のまま
	server := httptest.NewServer(transport.NewMux(deps))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?channel_ids=10"
	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	if err == nil {
		t.Fatal("err: nil (Forbiddenを期待)")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%v", resp)
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func TestHistoryRejectsNonMember(t *testing.T) {
	t.Parallel()

	deps, _, _ := newFullDeps(t) // Membershipは空のまま
	mux := transport.NewMux(deps)
	req := httptest.NewRequest(http.MethodGet, "/channels/10/messages", nil)
	req.Header.Set("X-User-Id", "1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d", rec.Code)
	}
}

// TestReconnectDoesNotReplayMissedMessages 切断中に保存されたメッセージは
// WebSocket再接続だけでは再送されず、Snapshot(ページ再読込)で見えることを固定します。
// この挙動が本実装の到達保証の境界です。
func TestReconnectDoesNotReplayMissedMessages(t *testing.T) {
	t.Parallel()

	deps, repo, h := newFullDeps(t)
	repo.members[[2]int64{1, 10}] = true

	server := httptest.NewServer(transport.NewMux(deps))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. 購読してから切断します。
	conn1 := dialWS(ctx, t, server.URL, h, 10)
	_ = conn1.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(2 * time.Second)
	for h.SubscriberCount(10) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.SubscriberCount(10) != 0 {
		t.Fatalf("unsubscribe未完了: count=%d", h.SubscriberCount(10))
	}

	// 2. 切断中にメッセージAが保存されます(配信先は不在)。
	respA := postForm(t, server.URL, "/channels/10/messages",
		url.Values{"body": {"切断中メッセージA"}, "client_msg_id": {"e2e-miss-a"}})
	_ = respA.Body.Close()
	if respA.StatusCode != http.StatusNoContent {
		t.Fatalf("post A status=%d", respA.StatusCode)
	}

	// 3. 再接続してメッセージBを送ると、届くのはBだけです。
	conn2 := dialWS(ctx, t, server.URL, h, 10)
	defer func() { _ = conn2.Close(websocket.StatusNormalClosure, "") }()

	respB := postForm(t, server.URL, "/channels/10/messages",
		url.Values{"body": {"再接続後メッセージB"}, "client_msg_id": {"e2e-miss-b"}})
	_ = respB.Body.Close()
	if respB.StatusCode != http.StatusNoContent {
		t.Fatalf("post B status=%d", respB.StatusCode)
	}

	_, data, err := conn2.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "再接続後メッセージB") {
		t.Fatalf("Bが届いていません: %s", got)
	}
	if strings.Contains(got, "切断中メッセージA") {
		t.Fatalf("再接続だけでAが再送されています(実装と主張の不一致): %s", got)
	}

	// 4. ページ再読込に相当するSnapshotでは、AもBも見えます。
	pageResp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	pageBytes, err := io.ReadAll(pageResp.Body)
	_ = pageResp.Body.Close()
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	page := string(pageBytes)
	if !strings.Contains(page, "切断中メッセージA") || !strings.Contains(page, "再接続後メッセージB") {
		t.Fatalf("snapshotに保存済みメッセージが出ていません")
	}
}
