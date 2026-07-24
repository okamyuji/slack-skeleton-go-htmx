// Package transport HTTPと(後続章で)WebSocketの配線を担います。
package transport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/hub"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/message"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/render"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/snapshot"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/webhook"
)

// handlerTimeout ハンドラ1回の処理に与える期限です。
// 層をまたいで二重管理しないよう、transport配下のハンドラとWebSocket送信は
// すべてこの1つを参照します。Hubの購読者ごとの送信期限は別ポリシーなので、
// hub.sendTimeoutとは独立して定義します。
const handlerTimeout = 5 * time.Second

// Deps ハンドラ層が必要とする依存をまとめます。
type Deps struct {
	Logger       *slog.Logger
	Snapshot     *snapshot.Service
	Renderer     *render.Renderer
	Messages     *message.Service
	Webhooks     *webhook.Service
	WebhookAdmin webhookAdmin
	Hub          *hub.Hub
	Members      membershipChecker
}

// membershipChecker WebSocket購読前のMembership検査に使う読み取り境界の抽象です。
type membershipChecker interface {
	IsMember(ctx context.Context, userID, channelID int64) (bool, error)
}

type webhookAdmin interface {
	FindWebhookBotUserIDByChannel(ctx context.Context, channelID int64) (int64, error)
	FindWebhookChannelID(ctx context.Context, id int64) (int64, error)
	CreateWebhook(ctx context.Context, in store.CreateWebhookInput) (domain.Webhook, error)
	DeleteWebhook(ctx context.Context, id int64) error
}

// requireWired 必要な依存が未配線の場合に既存の500応答を書いてfalseを返します。
func requireWired(w http.ResponseWriter, name string, wired bool) bool {
	if wired {
		return true
	}
	http.Error(w, name+" not wired", http.StatusInternalServerError)
	return false
}

// requireMember 対象チャンネルのMembershipを検査し、通らない場合はレスポンスを書いてfalseを返します。
// Webhook管理は投稿権限と同じ境界で守ります。本格的な管理者ロールはスコープ外です。
func requireMember(deps Deps, w http.ResponseWriter, r *http.Request, channelID int64) bool {
	if !requireWired(w, "membership", deps.Members != nil) {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
	defer cancel()
	ok, err := deps.Members.IsMember(ctx, currentUserID(r), channelID)
	if err != nil {
		deps.Logger.Error("membership check", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// NewMux Snapshot、Message、WebSocket、Incoming Webhookのルーティングを返します。
func NewMux(deps Deps) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			deps.Logger.Warn("write healthz", "err", err)
		}
	})

	mux.HandleFunc("GET /workspaces/{id}/snapshot", snapshotHandler(deps))
	mux.HandleFunc("POST /channels/{id}/messages", postMessageHandler(deps))
	mux.HandleFunc("GET /channels/{id}/messages", historyHandler(deps))
	mux.HandleFunc("POST /api/webhooks/{token}", webhookHandler(deps))
	mux.HandleFunc("POST /admin/webhooks", createWebhookAdminHandler(deps))
	mux.HandleFunc("POST /admin/webhooks/{id}/delete", deleteWebhookAdminHandler(deps))
	mux.HandleFunc("GET /ws", wsHandler(deps))

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if deps.Snapshot == nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("slack-skeleton-go-htmx: snapshot未配線"))
			return
		}
		serveSnapshot(deps, w, r, 1, currentUserID(r))
	})

	return mux
}

func createWebhookAdminHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireWired(w, "webhook admin", deps.WebhookAdmin != nil) {
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		channelID, err := strconv.ParseInt(r.FormValue("channel_id"), 10, 64)
		if err != nil || channelID <= 0 {
			http.Error(w, "invalid channel id", http.StatusBadRequest)
			return
		}
		if !requireMember(deps, w, r, channelID) {
			return
		}
		label := strings.TrimSpace(r.FormValue("label"))
		if label == "" {
			label = "GitHub push"
		}
		token, err := webhook.GenerateToken()
		if err != nil {
			deps.Logger.Error("webhook token", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()
		botUserID, err := deps.WebhookAdmin.FindWebhookBotUserIDByChannel(ctx, channelID)
		if err != nil {
			deps.Logger.Error("find webhook bot", "err", err)
			http.Error(w, "webhook bot not configured", http.StatusInternalServerError)
			return
		}
		_, err = deps.WebhookAdmin.CreateWebhook(ctx, store.CreateWebhookInput{
			ChannelID: channelID,
			Token:     token,
			Label:     label,
			Secret:    strings.TrimSpace(r.FormValue("secret")),
			BotUserID: botUserID,
		})
		if err != nil {
			if errors.Is(err, store.ErrDuplicate) {
				http.Error(w, "webhook already exists", http.StatusConflict)
				return
			}
			deps.Logger.Error("create webhook", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func deleteWebhookAdminHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireWired(w, "webhook admin", deps.WebhookAdmin != nil) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid webhook id", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()
		channelID, err := deps.WebhookAdmin.FindWebhookChannelID(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "webhook not found", http.StatusNotFound)
				return
			}
			deps.Logger.Error("find webhook channel", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !requireMember(deps, w, r, channelID) {
			return
		}
		err = deps.WebhookAdmin.DeleteWebhook(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "webhook not found", http.StatusNotFound)
				return
			}
			deps.Logger.Error("delete webhook", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func webhookHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireWired(w, "webhooks", deps.Webhooks != nil) {
			return
		}
		token := r.PathValue("token")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()

		saved, duplicate, err := deps.Webhooks.HandlePayload(ctx, token, r.Header, body)
		switch {
		case err == nil:
			if !duplicate && deps.Hub != nil && deps.Renderer != nil {
				frag, ferr := fragmentForMessage(deps.Renderer, saved)
				if ferr != nil {
					deps.Logger.Warn("fragment render", "err", ferr)
				} else {
					deps.Hub.Publish(ctx, saved.ChannelID, frag)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, store.ErrNotFound):
			http.Error(w, "webhook not found", http.StatusNotFound)
		case errors.Is(err, webhook.ErrUnauthorized):
			http.Error(w, "signature verification failed", http.StatusUnauthorized)
		case errors.Is(err, webhook.ErrBadPayload):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, message.ErrInvalidInput):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, message.ErrIdempotencyConflict):
			http.Error(w, "client_msg_id conflict", http.StatusConflict)
		default:
			deps.Logger.Error("webhook", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}
}

func snapshotHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		workspaceID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || workspaceID <= 0 {
			http.Error(w, "invalid workspace id", http.StatusBadRequest)
			return
		}
		serveSnapshot(deps, w, r, workspaceID, currentUserID(r))
	}
}

func serveSnapshot(deps Deps, w http.ResponseWriter, r *http.Request, workspaceID, userID int64) {
	if !requireWired(w, "snapshot service", deps.Snapshot != nil && deps.Renderer != nil) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
	defer cancel()

	view, err := deps.Snapshot.Load(ctx, workspaceID, userID)
	if err != nil {
		deps.Logger.Error("snapshot load", "err", err)
		http.Error(w, "snapshot load error", http.StatusInternalServerError)
		return
	}
	view.BaseURL = requestBaseURL(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := deps.Renderer.Render(w, "page", view); err != nil {
		deps.Logger.Error("render", "err", err)
	}
}

func requestBaseURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return strings.TrimRight(scheme+"://"+host, "/")
}

func postMessageHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireWired(w, "messages", deps.Messages != nil) {
			return
		}
		channelID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || channelID <= 0 {
			http.Error(w, "invalid channel id", http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		body := r.FormValue("body")
		clientMsgID := r.FormValue("client_msg_id")
		if clientMsgID == "" {
			http.Error(w, "client_msg_id required", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()

		saved, duplicate, err := deps.Messages.Send(ctx, message.SendInput{
			ChannelID:   channelID,
			UserID:      currentUserID(r),
			Body:        body,
			ClientMsgID: clientMsgID,
		})
		switch {
		case err == nil:
			// 重複でない新規送信のみHubへ配信します。
			// 受信は別経路(WebSocket)で行うため、ここでは204を返します。
			if duplicate {
				w.Header().Set("X-Duplicate", "1")
			} else if deps.Hub != nil && deps.Renderer != nil {
				frag, ferr := fragmentForMessage(deps.Renderer, saved)
				if ferr != nil {
					deps.Logger.Warn("fragment render", "err", ferr)
				} else {
					deps.Hub.Publish(ctx, channelID, frag)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, message.ErrInvalidInput):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, message.ErrNotMember):
			http.Error(w, "forbidden", http.StatusForbidden)
		case errors.Is(err, message.ErrIdempotencyConflict):
			http.Error(w, "client_msg_id conflict", http.StatusConflict)
		default:
			deps.Logger.Error("send message", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}
}

func historyHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireWired(w, "messages", deps.Messages != nil && deps.Renderer != nil) {
			return
		}
		channelID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || channelID <= 0 {
			http.Error(w, "invalid channel id", http.StatusBadRequest)
			return
		}
		beforeID, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()

		msgs, err := deps.Messages.History(ctx, currentUserID(r), channelID, beforeID, limit)
		if err != nil {
			if errors.Is(err, message.ErrNotMember) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			deps.Logger.Error("history", "err", err)
			http.Error(w, "history error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// サービス層からはid降順で返ってきます。読み込みUIがhx-swap="afterbegin"で
		// メッセージ一覧の先頭にひとかたまりで挿入するため、ここでは時系列昇順に
		// 並べ替えてからレンダリングします。
		for i := len(msgs) - 1; i >= 0; i-- {
			if err := deps.Renderer.Render(w, "message", msgs[i]); err != nil {
				deps.Logger.Error("render message", "err", err)
				return
			}
		}
	}
}

// currentUserID X-User-Idヘッダから現在ユーザーを取得します。
// 本記事では認証認可はスコープ外で、ヘッダ未指定時は1にフォールバックします。
func currentUserID(r *http.Request) int64 {
	if v := r.Header.Get("X-User-Id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			return id
		}
	}
	return 1
}
