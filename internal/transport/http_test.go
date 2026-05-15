package transport_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/render"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/snapshot"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/transport"
)

type stubReader struct {
	channels []domain.Channel
	users    []domain.User
	recent   map[int64][]domain.Message
}

func (s *stubReader) ListChannelsByWorkspace(_ context.Context, _ int64) ([]domain.Channel, error) {
	return s.channels, nil
}
func (s *stubReader) ListUsersByWorkspace(_ context.Context, _ int64) ([]domain.User, error) {
	return s.users, nil
}
func (s *stubReader) RecentMessages(_ context.Context, channelID int64, _ int) ([]domain.Message, error) {
	return s.recent[channelID], nil
}

func newDeps(t *testing.T) transport.Deps {
	t.Helper()
	r, err := render.New()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	reader := &stubReader{
		channels: []domain.Channel{{ID: 10, Name: "general"}},
		users:    []domain.User{{ID: 1, DisplayName: "alice"}},
		recent: map[int64][]domain.Message{
			10: {{ID: 1, ChannelID: 10, Body: "hi", CreatedAt: time.Now().UTC()}},
		},
	}
	return transport.Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Snapshot: snapshot.New(reader, 20),
		Renderer: r,
	}
}

func TestHealthzReturnsOK(t *testing.T) {
	t.Parallel()

	mux := transport.NewMux(newDeps(t))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestSnapshotHandlerReturnsHTML(t *testing.T) {
	t.Parallel()

	mux := transport.NewMux(newDeps(t))
	req := httptest.NewRequest(http.MethodGet, "/workspaces/1/snapshot", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type=%q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"alice", "#general", "msg-1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestSnapshotHandlerRejectsInvalidID(t *testing.T) {
	t.Parallel()

	mux := transport.NewMux(newDeps(t))
	req := httptest.NewRequest(http.MethodGet, "/workspaces/abc/snapshot", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}
