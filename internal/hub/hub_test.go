package hub_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// deadlineSender 常にcontext.DeadlineExceededを返す購読者です。
// wsClientの送信タイムアウト切れを模擬します。
type deadlineSender struct{ atomic.Int32 }

func (d *deadlineSender) Send(_ context.Context, _ []byte) error {
	d.Add(1)
	return context.DeadlineExceeded
}

func TestPublishContinuesPastTimedOutSubscriber(t *testing.T) {
	t.Parallel()

	h := hub.New()
	bad := &deadlineSender{}
	goods := make([]*recordingSender, 5)
	h.Subscribe(bad, []int64{1})
	for i := range goods {
		goods[i] = &recordingSender{}
		h.Subscribe(goods[i], []int64{1})
	}

	h.Publish(context.Background(), 1, []byte("x"))

	// タイムアウトした購読者は必ず解除され、他の全購読者には届きます。
	// mapの反復順に依存しないよう、失敗側が先頭でも後続でも同じ結果を要求します。
	if h.SubscriberCount(1) != len(goods) {
		t.Fatalf("タイムアウト側が解除されていません: count=%d", h.SubscriberCount(1))
	}
	for i, g := range goods {
		if g.count() != 1 {
			t.Fatalf("購読者%dに届いていません: count=%d", i, g.count())
		}
	}
}

// gateSender releaseが閉じるまでSendをブロックし続ける購読者です。
// 同時に何本のSendが走っているかをinflightで観測します。
type gateSender struct {
	inflight *atomic.Int32
	release  chan struct{}
}

func (g *gateSender) Send(ctx context.Context, _ []byte) error {
	g.inflight.Add(1)
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestPublishFansOutConcurrently(t *testing.T) {
	t.Parallel()

	h := hub.New()
	var inflight atomic.Int32
	release := make(chan struct{})
	const n = 8
	for i := 0; i < n; i++ {
		h.Subscribe(&gateSender{inflight: &inflight, release: release}, []int64{1})
	}

	done := make(chan struct{})
	go func() {
		h.Publish(context.Background(), 1, []byte("x"))
		close(done)
	}()

	// 直列実装なら同時実行数は1で頭打ちになり、この待ちが失敗します。
	// 並行実装なら全購読者のSendがブロック中でも同時に開始され、
	// Publish全体の所要時間は購読者数に比例しません。
	deadline := time.Now().Add(3 * time.Second)
	for inflight.Load() < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := inflight.Load(); got < n {
		t.Fatalf("並行送信になっていません: inflight=%d want=%d", got, n)
	}

	close(release)
	<-done
	if h.SubscriberCount(1) != n {
		t.Fatalf("成功した購読者が解除されています: count=%d", h.SubscriberCount(1))
	}
}

func TestPublishDeliversEvenWhenCallerContextCanceled(t *testing.T) {
	t.Parallel()

	h := hub.New()
	good := &recordingSender{}
	h.Subscribe(good, []int64{1})

	// 保存済みメッセージの配信はHTTPリクエストの取り消しに道連れにしません。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.Publish(ctx, 1, []byte("x"))

	if good.count() != 1 {
		t.Fatalf("呼び出し元ctx取り消し時に配信されていません: count=%d", good.count())
	}
	if h.SubscriberCount(1) != 1 {
		t.Fatalf("正常な購読者が解除されています: count=%d", h.SubscriberCount(1))
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
