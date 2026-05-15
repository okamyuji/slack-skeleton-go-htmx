// Package snapshot 初期画面ロード用にWorkspace全体の状態を1回のリクエストで取得します。
// Slackで言うSnapshot Serviceの役割を、単一プロセスの薄いサービスとして表現します。
package snapshot

import (
	"context"
	"fmt"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
)

// Reader Snapshotサービスが必要とする読み出しメソッドだけを抽象化します。
type Reader interface {
	ListChannelsByWorkspace(ctx context.Context, workspaceID int64) ([]domain.Channel, error)
	ListUsersByWorkspace(ctx context.Context, workspaceID int64) ([]domain.User, error)
	RecentMessages(ctx context.Context, channelID int64, limit int) ([]domain.Message, error)
}

// 静的にstore.Storeが必要なメソッドを満たすことを確認します。
var _ Reader = (*store.Store)(nil)

// Service Snapshotの組み立てを担います。
type Service struct {
	reader        Reader
	recentPerChan int
}

// New ServiceをReaderと最大件数で構築します。
func New(reader Reader, recentPerChan int) *Service {
	if recentPerChan <= 0 {
		recentPerChan = 20
	}
	return &Service{reader: reader, recentPerChan: recentPerChan}
}

// View ハンドラやテンプレートに渡すための統合データ型です。
type View struct {
	Workspace domain.Workspace
	Users     []domain.User
	Channels  []ChannelView
	Me        domain.User
}

// ChannelView 1チャンネル分の表示用データを束ねます。
type ChannelView struct {
	Channel  domain.Channel
	Messages []domain.Message
}

// Load Workspace全体のスナップショットを返します。
// 全チャンネルの直近メッセージを取得するため、本来はキャッシュ層を介す前提です。
func (s *Service) Load(ctx context.Context, workspaceID, meUserID int64) (View, error) {
	channels, err := s.reader.ListChannelsByWorkspace(ctx, workspaceID)
	if err != nil {
		return View{}, fmt.Errorf("snapshot: channels: %w", err)
	}
	users, err := s.reader.ListUsersByWorkspace(ctx, workspaceID)
	if err != nil {
		return View{}, fmt.Errorf("snapshot: users: %w", err)
	}

	var me domain.User
	for _, u := range users {
		if u.ID == meUserID {
			me = u
			break
		}
	}

	cvs := make([]ChannelView, 0, len(channels))
	for _, c := range channels {
		msgs, err := s.reader.RecentMessages(ctx, c.ID, s.recentPerChan)
		if err != nil {
			return View{}, fmt.Errorf("snapshot: recent %d: %w", c.ID, err)
		}
		// id降順で取得しているため、表示の都合で昇順に並べ替えます。
		for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
			msgs[i], msgs[j] = msgs[j], msgs[i]
		}
		cvs = append(cvs, ChannelView{Channel: c, Messages: msgs})
	}

	return View{
		Workspace: domain.Workspace{ID: workspaceID},
		Users:     users,
		Channels:  cvs,
		Me:        me,
	}, nil
}
