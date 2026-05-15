// Package transport HTTPと(後続章で)WebSocketの配線を担います。
package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/message"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/render"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/snapshot"
)

// Deps ハンドラ層が必要とする依存をまとめます。
type Deps struct {
	Logger   *slog.Logger
	Snapshot *snapshot.Service
	Renderer *render.Renderer
	Messages *message.Service
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := deps.Renderer.Render(w, "page", view); err != nil {
		deps.Logger.Error("render", "err", err)
	}
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

		_, duplicate, err := deps.Messages.Send(ctx, message.SendInput{
			ChannelID:   channelID,
			UserID:      currentUserID(r),
			Body:        body,
			ClientMsgID: clientMsgID,
		})
		switch {
		case err == nil:
			// 受信は別経路(WebSocket)で行うため、ここでは204を返します。
			if duplicate {
				w.Header().Set("X-Duplicate", "1")
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
