// このファイルはWebSocketハンドラとHubクライアントを定義します。
package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/hub"
)

// wsClient 1つのWebSocketコネクションを表します。
// hub.Senderを実装し、HubからのpayloadをWS経由でクライアントへ送出します。
type wsClient struct {
	conn *websocket.Conn
}

// Send Hubから呼ばれる送信メソッドです。
func (c *wsClient) Send(ctx context.Context, payload []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageText, payload)
}

func wsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Hub == nil {
			http.Error(w, "hub not wired", http.StatusInternalServerError)
			return
		}
		channelIDs := parseChannelIDs(r.URL.Query().Get("channel_ids"))
		if len(channelIDs) == 0 {
			http.Error(w, "channel_ids required", http.StatusBadRequest)
			return
		}

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"},
		})
		if err != nil {
			deps.Logger.Warn("ws accept", "err", err)
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

		client := &wsClient{conn: conn}
		sub := deps.Hub.Subscribe(client, channelIDs)
		defer deps.Hub.Unsubscribe(sub)

		ctx := r.Context()
		for {
			_, _, err := conn.Read(ctx)
			if err != nil {
				if !isExpectedClose(err) {
					deps.Logger.Info("ws read end", "err", err)
				}
				return
			}
		}
	}
}

func parseChannelIDs(s string) []int64 {
	if s == "" {
		return nil
	}
	var out []int64
	for _, part := range strings.Split(s, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func isExpectedClose(err error) bool {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

type messageFragmentRenderer interface {
	Render(w io.Writer, name string, data any) error
}

// fragmentForMessage 単一メッセージをHubに流す際のHTMLフラグメントを構築します。
// hx-swap-oobで対象チャンネルのDOMにbeforeendで挿入されます。
// 本文に@が含まれる場合は同じ送信payloadにトースト通知フラグメントも結合し、
// 受信側の全クライアントに画面右下のポップアップを表示します。
func fragmentForMessage(renderer messageFragmentRenderer, msg domain.Message) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(`<div id="messages-`)
	buf.WriteString(strconv.FormatInt(msg.ChannelID, 10))
	buf.WriteString(`" hx-swap-oob="beforeend">`)
	if err := renderer.Render(&buf, "message", msg); err != nil {
		return nil, err
	}
	buf.WriteString(`</div>`)

	if strings.Contains(msg.Body, "@") {
		if err := renderer.Render(&buf, "toast", "新着メンション: "+truncate(msg.Body, 60)); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "..."
}

// 静的にhub.Senderを満たすことを保証します。
var _ hub.Sender = (*wsClient)(nil)
