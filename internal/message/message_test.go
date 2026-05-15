package message_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/message"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
)

// fakeRepo はmessage.Repositoryをin-memoryで満たすテスト用ダブルです。
type fakeRepo struct {
	members    map[[2]int64]bool
	stored     []domain.Message
	idemKey    map[string]domain.Message
	nextID     int64
	insertErr  error
	memberErr  error
	historyErr error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		members: make(map[[2]int64]bool),
		idemKey: make(map[string]domain.Message),
	}
}

func (f *fakeRepo) IsMember(_ context.Context, userID, channelID int64) (bool, error) {
	if f.memberErr != nil {
		return false, f.memberErr
	}
	return f.members[[2]int64{userID, channelID}], nil
}

func (f *fakeRepo) InsertMessage(_ context.Context, in domain.Message) (domain.Message, error) {
	if f.insertErr != nil {
		return domain.Message{}, f.insertErr
	}
	key := in.ClientMsgID
	if _, dup := f.idemKey[key]; dup {
		return domain.Message{}, store.ErrDuplicate
	}
	f.nextID++
	saved := in
	saved.ID = f.nextID
	saved.CreatedAt = time.Now().UTC()
	f.stored = append(f.stored, saved)
	f.idemKey[key] = saved
	return saved, nil
}

func (f *fakeRepo) RecentMessages(_ context.Context, channelID int64, limit int) ([]domain.Message, error) {
	if f.historyErr != nil {
		return nil, f.historyErr
	}
	var out []domain.Message
	for i := len(f.stored) - 1; i >= 0 && len(out) < limit; i-- {
		if f.stored[i].ChannelID == channelID {
			out = append(out, f.stored[i])
		}
	}
	return out, nil
}

func (f *fakeRepo) MessagesBefore(_ context.Context, channelID, beforeID int64, limit int) ([]domain.Message, error) {
	if f.historyErr != nil {
		return nil, f.historyErr
	}
	var out []domain.Message
	for i := len(f.stored) - 1; i >= 0 && len(out) < limit; i-- {
		m := f.stored[i]
		if m.ChannelID != channelID {
			continue
		}
		if beforeID > 0 && m.ID >= beforeID {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func TestSendRejectsEmptyBody(t *testing.T) {
	t.Parallel()
	svc := message.New(newFakeRepo())
	_, _, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: 1, UserID: 1, Body: "  ", ClientMsgID: "k1",
	})
	if !errors.Is(err, message.ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
}

func TestSendRejectsLongBody(t *testing.T) {
	t.Parallel()
	svc := message.New(newFakeRepo())
	long := make([]byte, 4001)
	for i := range long {
		long[i] = 'a'
	}
	_, _, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: 1, UserID: 1, Body: string(long), ClientMsgID: "k1",
	})
	if !errors.Is(err, message.ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
}

func TestSendRejectsEmptyClientMsgID(t *testing.T) {
	t.Parallel()
	svc := message.New(newFakeRepo())
	_, _, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: 1, UserID: 1, Body: "hi", ClientMsgID: "",
	})
	if !errors.Is(err, message.ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
}

func TestSendRejectsNonPositiveIDs(t *testing.T) {
	t.Parallel()
	svc := message.New(newFakeRepo())
	_, _, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: 0, UserID: 1, Body: "hi", ClientMsgID: "k",
	})
	if !errors.Is(err, message.ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
}

func TestSendRejectsNonMember(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	// 参加なし
	svc := message.New(repo)
	_, _, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: 1, UserID: 1, Body: "hi", ClientMsgID: "k",
	})
	if !errors.Is(err, message.ErrNotMember) {
		t.Fatalf("got %v", err)
	}
}

func TestSendInsertsForMember(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.members[[2]int64{1, 10}] = true
	svc := message.New(repo)

	saved, dup, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: 10, UserID: 1, Body: "hi", ClientMsgID: "k",
	})
	if err != nil || dup {
		t.Fatalf("err=%v dup=%v", err, dup)
	}
	if saved.ID == 0 {
		t.Fatal("ID未採番")
	}
}

func TestSendReturnsExistingOnDuplicate(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.members[[2]int64{1, 10}] = true
	svc := message.New(repo)

	first, _, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: 10, UserID: 1, Body: "hi", ClientMsgID: "dup",
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, dup, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: 10, UserID: 1, Body: "hi", ClientMsgID: "dup",
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !dup {
		t.Fatal("duplicate=trueを期待")
	}
	if second.ID != first.ID {
		t.Fatalf("ID差分 first=%d second=%d", first.ID, second.ID)
	}
}

func TestSendPropagatesMemberCheckError(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.memberErr = errors.New("boom")
	svc := message.New(repo)
	_, _, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: 1, UserID: 1, Body: "x", ClientMsgID: "k",
	})
	if err == nil {
		t.Fatal("err: nilでなく boom 由来エラーを期待")
	}
}

func TestSendPropagatesInsertError(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.members[[2]int64{1, 1}] = true
	repo.insertErr = errors.New("write fail")
	svc := message.New(repo)
	_, _, err := svc.Send(context.Background(), message.SendInput{
		ChannelID: 1, UserID: 1, Body: "x", ClientMsgID: "k",
	})
	if err == nil {
		t.Fatal("insertエラーが伝播していません")
	}
}

func TestHistoryDefaultsLimit(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.members[[2]int64{1, 1}] = true
	svc := message.New(repo)

	for i := 0; i < 30; i++ {
		_, _, err := svc.Send(context.Background(), message.SendInput{
			ChannelID: 1, UserID: 1, Body: "m", ClientMsgID: "key-" + time.Now().Format("150405.000000000"),
		})
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		time.Sleep(time.Microsecond)
	}
	got, err := svc.History(context.Background(), 1, 0, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(got) != 20 {
		t.Fatalf("default limit=20 を期待しましたが %d", len(got))
	}
}

func TestHistoryUsesBeforeCursor(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.members[[2]int64{1, 1}] = true
	svc := message.New(repo)

	var ids []int64
	for i := 0; i < 5; i++ {
		saved, _, err := svc.Send(context.Background(), message.SendInput{
			ChannelID: 1, UserID: 1, Body: "m", ClientMsgID: "k" + time.Now().Format("150405.000000000"),
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		ids = append(ids, saved.ID)
		time.Sleep(time.Microsecond)
	}
	got, err := svc.History(context.Background(), 1, ids[2], 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	for _, m := range got {
		if m.ID >= ids[2] {
			t.Fatalf("cursor%d より新しいIDが返っています: %d", ids[2], m.ID)
		}
	}
}
