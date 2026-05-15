// Package message メッセージの送信と取得を取り扱うサービス層です。
// Slack内部のChat Serviceに相当します。
package message

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
)

// ErrInvalidInput 入力が不正であることを表します。
var ErrInvalidInput = errors.New("message: invalid input")

// ErrNotMember 投稿者がチャンネルに参加していないことを表します。
var ErrNotMember = errors.New("message: not a member")

// SendInput 送信時の入力を束ねます。
type SendInput struct {
	ChannelID   int64
	UserID      int64
	Body        string
	ClientMsgID string
}

// Service messageレイヤの薄いサービスです。
type Service struct {
	store *store.Store
}

// New サービスを構築します。
func New(s *store.Store) *Service { return &Service{store: s} }

// Send 1件のメッセージを保存します。
// 冪等性キーが既知の組み合わせなら既存メッセージを返します。
func (s *Service) Send(ctx context.Context, in SendInput) (domain.Message, bool, error) {
	body := strings.TrimSpace(in.Body)
	if body == "" || len(body) > 4000 {
		return domain.Message{}, false, fmt.Errorf("%w: body length", ErrInvalidInput)
	}
	cid := strings.TrimSpace(in.ClientMsgID)
	if cid == "" || len(cid) > 64 {
		return domain.Message{}, false, fmt.Errorf("%w: client_msg_id", ErrInvalidInput)
	}
	if in.ChannelID <= 0 || in.UserID <= 0 {
		return domain.Message{}, false, fmt.Errorf("%w: channel/user id", ErrInvalidInput)
	}

	ok, err := s.store.IsMember(ctx, in.UserID, in.ChannelID)
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("send: is member: %w", err)
	}
	if !ok {
		return domain.Message{}, false, ErrNotMember
	}

	saved, err := s.store.InsertMessage(ctx, domain.Message{
		ChannelID:   in.ChannelID,
		UserID:      in.UserID,
		Body:        body,
		ClientMsgID: cid,
	})
	if err == nil {
		return saved, false, nil
	}
	if !errors.Is(err, store.ErrDuplicate) {
		return domain.Message{}, false, fmt.Errorf("send: insert: %w", err)
	}

	// 既存レコードを探して返します(教材スコープなのでchannel単位の線形検索で十分です)。
	existing, err := s.findExistingByClientMsgID(ctx, in.ChannelID, cid)
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("send: find existing: %w", err)
	}
	return existing, true, nil
}

// History カーソルベースで過去メッセージを最新順に返します。
func (s *Service) History(ctx context.Context, channelID, beforeID int64, limit int) ([]domain.Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	msgs, err := s.store.MessagesBefore(ctx, channelID, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	return msgs, nil
}

func (s *Service) findExistingByClientMsgID(ctx context.Context, channelID int64, clientMsgID string) (domain.Message, error) {
	// 同一チャンネル内の最新側から軽くスキャンします。
	// 本番ではUNIQUEインデックス利用の単発SELECTに置き換える前提です。
	recent, err := s.store.RecentMessages(ctx, channelID, 100)
	if err != nil {
		return domain.Message{}, err
	}
	for _, m := range recent {
		if m.ClientMsgID == clientMsgID {
			return m, nil
		}
	}
	return domain.Message{}, fmt.Errorf("duplicate marker but record not found")
}
