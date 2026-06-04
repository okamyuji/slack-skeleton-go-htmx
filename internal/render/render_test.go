package render_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/render"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/snapshot"
)

func newRenderer(t *testing.T) *render.Renderer {
	t.Helper()
	r, err := render.New()
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return r
}

func TestRenderPageContainsBody(t *testing.T) {
	t.Parallel()

	r := newRenderer(t)
	view := snapshot.View{
		Workspace: domain.Workspace{ID: 1, Name: "Test"},
		BaseURL:   "http://localhost:8080",
		Me:        domain.User{ID: 100, DisplayName: "alice"},
		Channels: []snapshot.ChannelView{
			{
				Channel: domain.Channel{ID: 10, Name: "general"},
				Messages: []domain.Message{
					{ID: 1, ChannelID: 10, Body: "hello", CreatedAt: time.Now().UTC()},
				},
			},
		},
		Webhooks: []domain.WebhookSetting{
			{
				Webhook: domain.Webhook{
					ID:        7,
					ChannelID: 10,
					Token:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					Label:     "GitHub main",
				},
				ChannelName: "general",
				HasSecret:   true,
			},
		},
	}
	var buf bytes.Buffer
	if err := r.Render(&buf, "page", view); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"alice",
		"#general",
		"hello",
		"/ws?channel_ids=10",
		"GitHub連携管理",
		"GitHub main",
		"Payload URL",
		"http://localhost:8080/api/webhooks/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"Content type",
		"application/json",
		"Secret configured",
		"新規Webhookを作成",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %s", want, out)
		}
	}
}

func TestRenderMessagePartial(t *testing.T) {
	t.Parallel()

	r := newRenderer(t)
	msg := domain.Message{ID: 42, ChannelID: 7, Body: "テスト本文", CreatedAt: time.Date(2026, 5, 16, 12, 30, 0, 0, time.UTC)}
	var buf bytes.Buffer
	if err := r.Render(&buf, "message", msg); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "msg-42") || !strings.Contains(buf.String(), "テスト本文") {
		t.Fatalf("unexpected: %s", buf.String())
	}
}
