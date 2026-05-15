package hub_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/hub"
)

type recordingSender struct {
	mu       sync.Mutex
	messages [][]byte
}

func (r *recordingSender) Send(_ context.Context, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, append([]byte(nil), payload...))
	return nil
}

func (r *recordingSender) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.messages)
}

func TestPublishFansOutToSubscribers(t *testing.T) {
	t.Parallel()

	h := hub.New()
	a := &recordingSender{}
	b := &recordingSender{}
	h.Subscribe(a, []int64{1, 2})
	h.Subscribe(b, []int64{2, 3})

	h.Publish(context.Background(), 2, []byte("payload"))
	if a.count() != 1 || b.count() != 1 {
		t.Fatalf("a=%d b=%d", a.count(), b.count())
	}
	h.Publish(context.Background(), 3, []byte("only-b"))
	if a.count() != 1 || b.count() != 2 {
		t.Fatalf("after pub3 a=%d b=%d", a.count(), b.count())
	}
}

func TestUnsubscribeRemovesFromAllChannels(t *testing.T) {
	t.Parallel()

	h := hub.New()
	s := &recordingSender{}
	sub := h.Subscribe(s, []int64{1, 2})
	if h.SubscriberCount(1) != 1 || h.SubscriberCount(2) != 1 {
		t.Fatal("subscribe失敗")
	}
	h.Unsubscribe(sub)
	if h.SubscriberCount(1) != 0 || h.SubscriberCount(2) != 0 {
		t.Fatal("unsubscribeが残っています")
	}
	h.Publish(context.Background(), 1, []byte("x"))
	if s.count() != 0 {
		t.Fatal("unsubscribe後に配信されています")
	}
}

type erroringSender struct{ atomic.Int32 }

func (e *erroringSender) Send(_ context.Context, _ []byte) error {
	e.Add(1)
	return errors.New("slow consumer")
}

func TestSlowConsumerIsDisconnected(t *testing.T) {
	t.Parallel()

	h := hub.New()
	bad := &erroringSender{}
	good := &recordingSender{}
	h.Subscribe(bad, []int64{1})
	h.Subscribe(good, []int64{1})

	h.Publish(context.Background(), 1, []byte("x"))

	if h.SubscriberCount(1) != 1 {
		t.Fatalf("失敗側が外れていません: count=%d", h.SubscriberCount(1))
	}
	if good.count() != 1 {
		t.Fatal("成功側に届いていません")
	}
}

func TestConcurrentPublishIsRaceFree(t *testing.T) {
	t.Parallel()

	h := hub.New()
	for i := 0; i < 50; i++ {
		h.Subscribe(&recordingSender{}, []int64{1})
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.Publish(context.Background(), 1, []byte("p"))
			}
		}()
	}
	wg.Wait()
}
