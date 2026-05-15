// Package transport HTTPと(後続章で)WebSocketの配線を担います。
package transport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/render"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/snapshot"
)

// Deps ハンドラ層が必要とする依存をまとめます。
// 後続章ではここにHubやMessageServiceが加わります。
type Deps struct {
	Logger   *slog.Logger
	Snapshot *snapshot.Service
	Renderer *render.Renderer
}

// NewMux Chapter 3時点でのルーティングを返します。
// healthz、ルートのスナップショット、固定Welcomeを提供します。
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

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if deps.Snapshot == nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("slack-skeleton-go-htmx: snapshot未配線"))
			return
		}
		// ルートアクセスは固定でworkspace 1 / user 1 を見せます(認証はスコープ外)。
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
	ctx, cancel := context.WithTimeout(r.Context(), 5*1000*1000*1000)
	defer cancel()

	view, err := deps.Snapshot.Load(ctx, workspaceID, userID)
	if err != nil {
		deps.Logger.Error("snapshot load", "err", err)
		http.Error(w, "snapshot load error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := deps.Renderer.Render(w, "page", view); err != nil {
		var netErr interface{ Timeout() bool }
		if errors.As(err, &netErr) {
			deps.Logger.Warn("render timeout", "err", err)
			return
		}
		deps.Logger.Error("render", "err", err)
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
