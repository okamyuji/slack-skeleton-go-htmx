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

// Deps ハンドラ層が必要とする依存をまとめます。
type Deps struct {
	Logger       *slog.Logger
	Snapshot     *snapshot.Service
	Renderer     *render.Renderer
	Messages     *message.Service
	Webhooks     *webhook.Service
	WebhookAdmin webhookAdmin
	Hub          *hub.Hub
}

type webhookAdmin interface {
	FindWebhookBotUserIDByChannel(ctx context.Context, channelID int64) (int64, error)
	CreateWebhook(ctx context.Context, in store.CreateWebhookInput) (domain.Webhook, error)
	DeleteWebhook(ctx context.Context, id int64) error
}

// NewMux Chapter 5時点でのルーティングを返します。
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
		if deps.WebhookAdmin == nil {
			http.Error(w, "webhook admin not wired", http.StatusInternalServerError)
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

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
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
		if deps.WebhookAdmin == nil {
			http.Error(w, "webhook admin not wired", http.StatusInternalServerError)
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid webhook id", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
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
		if deps.Webhooks == nil {
			http.Error(w, "webhooks not wired", http.StatusInternalServerError)
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

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
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
	if deps.Snapshot == nil || deps.Renderer == nil {
		http.Error(w, "snapshot service not wired", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
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
		if deps.Messages == nil {
			http.Error(w, "messages not wired", http.StatusInternalServerError)
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
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
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
		default:
			deps.Logger.Error("send message", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}
}

func historyHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Messages == nil || deps.Renderer == nil {
			http.Error(w, "messages not wired", http.StatusInternalServerError)
			return
		}
		channelID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || channelID <= 0 {
			http.Error(w, "invalid channel id", http.StatusBadRequest)
			return
		}
		beforeID, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		msgs, err := deps.Messages.History(ctx, channelID, beforeID, limit)
		if err != nil {
			deps.Logger.Error("history", "err", err)
			http.Error(w, "history error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// id降順で返ってきている前提で、テンプレ側は変えずに1件ずつレンダします。
		for _, m := range msgs {
			if err := deps.Renderer.Render(w, "message", m); err != nil {
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
