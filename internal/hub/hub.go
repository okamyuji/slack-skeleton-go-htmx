// Package hub channelごとの購読者集合を保持して、メッセージをファンアウトします。
// Slack内部のGateway Serverに相当する役割を、単一プロセスのin-memory実装で表現します。
package hub

import (
	"context"
	"errors"
	"sync"
)

// Sender 1つのWebSocketコネクションに対するHTMLフラグメント送信の抽象です。
// 本記事のWSハンドラはこのインターフェイスを実装します。
type Sender interface {
	Send(ctx context.Context, payload []byte) error
}

// Subscription 1つのクライアントの購読を表します。
type Subscription struct {
	id         uint64
	channelIDs map[int64]struct{}
	sender     Sender
	hub        *Hub
}

// Hub channelごとの購読者集合と、ブロードキャストのファンアウトを行います。
type Hub struct {
	mu          sync.RWMutex
	nextID      uint64
	subscribers map[int64]map[uint64]*Subscription
}

// New 空のHubを返します。
func New() *Hub {
	return &Hub{subscribers: make(map[int64]map[uint64]*Subscription)}
}

// Subscribe Senderをcs(channel ID集合)に登録します。
func (h *Hub) Subscribe(sender Sender, channelIDs []int64) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nextID++
	sub := &Subscription{
		id:         h.nextID,
		channelIDs: make(map[int64]struct{}, len(channelIDs)),
		sender:     sender,
		hub:        h,
	}
	for _, id := range channelIDs {
		if id <= 0 {
			continue
		}
		sub.channelIDs[id] = struct{}{}
		if _, ok := h.subscribers[id]; !ok {
			h.subscribers[id] = make(map[uint64]*Subscription)
		}
		h.subscribers[id][sub.id] = sub
	}
	return sub
}

// Unsubscribe 購読を解除します。複数回呼び出しても安全です。
func (h *Hub) Unsubscribe(sub *Subscription) {
	if sub == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id := range sub.channelIDs {
		if subs, ok := h.subscribers[id]; ok {
			delete(subs, sub.id)
			if len(subs) == 0 {
				delete(h.subscribers, id)
			}
		}
	}
	sub.channelIDs = nil
}

// Publish channelIDを購読中の全コネクションへpayloadを配信します。
// 送信に失敗したコネクションは自動的にUnsubscribeします(slow consumer disconnect)。
func (h *Hub) Publish(ctx context.Context, channelID int64, payload []byte) {
	h.mu.RLock()
	subs := h.subscribers[channelID]
	targets := make([]*Subscription, 0, len(subs))
	for _, s := range subs {
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, sub := range targets {
		if err := sub.sender.Send(ctx, payload); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			h.Unsubscribe(sub)
		}
	}
}

// SubscriberCount 指定チャンネルの購読者数を返します。テスト用です。
func (h *Hub) SubscriberCount(channelID int64) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers[channelID])
}
