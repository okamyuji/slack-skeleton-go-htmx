// Package message メッセージの送信と取得を取り扱うサービス層です。
// Slack内部のChat Serviceに相当します。
package message

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
)

// ErrInvalidInput 入力が不正であることを表します。
var ErrInvalidInput = errors.New("message: invalid input")

// ErrNotMember 投稿者がチャンネルに参加していないことを表します。
var ErrNotMember = errors.New("message: not a member")

// ErrIdempotencyConflict 同じclient_msg_idが異なる内容の送信に使われたことを表します。
// 同じキーは同一内容の再送だけに使えるという契約の違反です。
var ErrIdempotencyConflict = errors.New("message: client_msg_id conflict")

// Repository messageサービスが必要とするデータアクセスメソッドを抽象化します。
// 各層をinterfaceで疎結合に繋ぐことで、テスト時にfakeを差し込みやすくします。
type Repository interface {
	IsMember(ctx context.Context, userID, channelID int64) (bool, error)
	InsertMessage(ctx context.Context, in domain.Message) (domain.Message, error)
	FindMessageByClientMsgID(ctx context.Context, channelID int64, clientMsgID string) (domain.Message, error)
	MessagesBefore(ctx context.Context, channelID, beforeID int64, limit int) ([]domain.Message, error)
}

// 静的にstore.StoreがRepositoryを満たすことを保証します。
var _ Repository = (*store.Store)(nil)

// SendInput 送信時の入力を束ねます。
type SendInput struct {
	ChannelID   int64
	UserID      int64
	Body        string
	ClientMsgID string
}

// Service messageレイヤの薄いサービスです。
type Service struct {
	repo Repository
}

// New サービスを構築します。
func New(repo Repository) *Service { return &Service{repo: repo} }

// Send 1件のメッセージを保存します。
// 冪等性キーが既知の組み合わせなら既存メッセージを返します。
func (s *Service) Send(ctx context.Context, in SendInput) (domain.Message, bool, error) {
	body := strings.TrimSpace(in.Body)
	// 上限はバイトではなくルーン数で数えます。webhook側の切り詰めが
	// ルーン単位なので、ここをバイトで数えると多バイト本文の通知が
	// 切り詰め後もここで弾かれて失われます。
	if body == "" || utf8.RuneCountInString(body) > 4000 {
		return domain.Message{}, false, fmt.Errorf("%w: body length", ErrInvalidInput)
	}
	cid := strings.TrimSpace(in.ClientMsgID)
	if cid == "" || len(cid) > 64 {
		return domain.Message{}, false, fmt.Errorf("%w: client_msg_id", ErrInvalidInput)
	}
	if in.ChannelID <= 0 || in.UserID <= 0 {
		return domain.Message{}, false, fmt.Errorf("%w: channel/user id", ErrInvalidInput)
	}

	ok, err := s.repo.IsMember(ctx, in.UserID, in.ChannelID)
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("send: is member: %w", err)
	}
	if !ok {
		return domain.Message{}, false, ErrNotMember
	}

	saved, err := s.repo.InsertMessage(ctx, domain.Message{
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

	// 既存レコードをユニークキーで直接引いて返します。
	// 直近N件の線形検索だと、遅延リトライまでにN件を超える新規投稿が
	// 挟まった時点で見つけられなくなり、冪等な結果を返せません。
	existing, err := s.repo.FindMessageByClientMsgID(ctx, in.ChannelID, cid)
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("send: find existing: %w", err)
	}
	// 同じキーで内容が違う送信を成功扱いにすると、編集後の本文が保存も配信も
	// されないまま無言で消えます。原本と比較し、不一致は衝突として拒否します。
	if existing.UserID != in.UserID || existing.Body != body {
		return domain.Message{}, false, ErrIdempotencyConflict
	}
	return existing, true, nil
}

// History カーソルベースで過去メッセージを最新順に返します。
// 投稿側と同じMembership境界を読み取り側にも適用し、非参加者にはErrNotMemberを返します。
func (s *Service) History(ctx context.Context, userID, channelID, beforeID int64, limit int) ([]domain.Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	ok, err := s.repo.IsMember(ctx, userID, channelID)
	if err != nil {
		return nil, fmt.Errorf("history: is member: %w", err)
	}
	if !ok {
		return nil, ErrNotMember
	}
	msgs, err := s.repo.MessagesBefore(ctx, channelID, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	return msgs, nil
}
