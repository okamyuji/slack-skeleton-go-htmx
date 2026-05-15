// Package transport はHTTPと(後続章で)WebSocketの配線を担います。
package transport

import (
	"log/slog"
	"net/http"
)

// NewMux はChapter 1時点でのルーティングを返します。
// 現状はヘルスチェックと固定のWelcomeレスポンスのみを提供します。
func NewMux(logger *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			logger.Warn("write healthz", "err", err)
		}
	})

	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("slack-skeleton-go-htmx: chapter1 skeleton")); err != nil {
			logger.Warn("write root", "err", err)
		}
	})

	return mux
}
