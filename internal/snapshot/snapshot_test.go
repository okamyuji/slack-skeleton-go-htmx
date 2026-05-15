package snapshot_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/snapshot"
)

type fakeReader struct {
	channels []domain.Channel
	users    []domain.User
	recent   map[int64][]domain.Message
	err      error
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
}

func TestLoadPropagatesReaderError(t *testing.T) {
	t.Parallel()

	r := &fakeReader{err: errors.New("boom")}
	svc := snapshot.New(r, 20)
	if _, err := svc.Load(context.Background(), 1, 1); err == nil {
		t.Fatal("err: got nil, want non-nil")
	}
}
