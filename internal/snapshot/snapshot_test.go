package snapshot_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/snapshot"
)

type fakeReader struct {
	channels   []domain.Channel
	users      []domain.User
	recent     map[int64][]domain.Message
	webhooks   []domain.WebhookSetting
	err        error
	webhookErr error
}

func (f *fakeReader) ListChannelsByWorkspace(_ context.Context, _ int64) ([]domain.Channel, error) {
	return f.channels, f.err
}
func (f *fakeReader) ListUsersByWorkspace(_ context.Context, _ int64) ([]domain.User, error) {
	return f.users, f.err
}
func (f *fakeReader) RecentMessages(_ context.Context, channelID int64, _ int) ([]domain.Message, error) {
	return f.recent[channelID], f.err
}
func (f *fakeReader) ListWebhookSettingsByWorkspace(_ context.Context, _ int64) ([]domain.WebhookSetting, error) {
	if f.webhookErr != nil {
		return nil, f.webhookErr
	}
	return f.webhooks, f.err
}

func TestLoadReturnsSortedMessagesAscending(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	r := &fakeReader{
		channels: []domain.Channel{{ID: 10, WorkspaceID: 1, Name: "general", CreatedAt: now}},
		users: []domain.User{
			{ID: 100, WorkspaceID: 1, DisplayName: "alice", CreatedAt: now},
			{ID: 101, WorkspaceID: 1, DisplayName: "bob", CreatedAt: now},
		},
		recent: map[int64][]domain.Message{
			10: {
				{ID: 3, ChannelID: 10, UserID: 100, Body: "third"},
				{ID: 2, ChannelID: 10, UserID: 100, Body: "second"},
				{ID: 1, ChannelID: 10, UserID: 100, Body: "first"},
			},
		},
		webhooks: []domain.WebhookSetting{
			{
				Webhook:     domain.Webhook{ID: 20, ChannelID: 10, Token: "tok", Label: "GitHub main"},
				ChannelName: "general",
				HasSecret:   true,
			},
		},
	}
	svc := snapshot.New(r, 20)

	view, err := svc.Load(context.Background(), 1, 100)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(view.Channels) != 1 {
		t.Fatalf("channel count: got %d", len(view.Channels))
	}
	msgs := view.Channels[0].Messages
	if len(msgs) != 3 || msgs[0].ID != 1 || msgs[2].ID != 3 {
		t.Fatalf("messages not ascending: %+v", msgs)
	}
	if view.Me.ID != 100 {
		t.Fatalf("me.ID: got %d, want 100", view.Me.ID)
	}
	if len(view.Webhooks) != 1 || view.Webhooks[0].Label != "GitHub main" {
		t.Fatalf("webhooks: %+v", view.Webhooks)
	}
}

func TestLoadPropagatesReaderError(t *testing.T) {
	t.Parallel()

	r := &fakeReader{err: errors.New("boom")}
	svc := snapshot.New(r, 20)
	if _, err := svc.Load(context.Background(), 1, 1); err == nil {
		t.Fatal("err: got nil, want non-nil")
	}
}

func TestLoadWrapsWebhookReaderError(t *testing.T) {
	t.Parallel()

	r := &fakeReader{webhookErr: errors.New("boom")}
	svc := snapshot.New(r, 20)
	_, err := svc.Load(context.Background(), 1, 1)
	if err == nil {
		t.Fatal("err: got nil, want non-nil")
	}
	if !strings.HasPrefix(err.Error(), "snapshot: webhooks:") {
		t.Fatalf("err=%v, want snapshot: webhooks prefix", err)
	}
}
