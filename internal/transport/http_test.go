package transport_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/hub"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/message"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/render"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/snapshot"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/transport"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/webhook"
)

type stubReader struct {
	channels []domain.Channel
	users    []domain.User
	recent   map[int64][]domain.Message
	repo     *fakeMessageRepo // 非nilのときはRecentMessagesを保存済みデータへ委譲します
	webhooks []domain.WebhookSetting
}

func (s *stubReader) FindWorkspaceName(_ context.Context, _ int64) (string, error) {
	return "Demo Workspace", nil
}
func (s *stubReader) ListChannelsForUser(_ context.Context, _, _ int64) ([]domain.Channel, error) {
	return s.channels, nil
}
func (s *stubReader) ListUsersByWorkspace(_ context.Context, _ int64) ([]domain.User, error) {
	return s.users, nil
}
func (s *stubReader) RecentMessages(ctx context.Context, channelID int64, limit int) ([]domain.Message, error) {
	if s.repo != nil {
		return s.repo.RecentMessages(ctx, channelID, limit)
	}
	return s.recent[channelID], nil
}
func (s *stubReader) ListWebhookSettingsForUser(_ context.Context, _, _ int64) ([]domain.WebhookSetting, error) {
	return s.webhooks, nil
}

// fakeMessageRepo はmessage.Repositoryを満たすin-memoryダブルです。
type fakeMessageRepo struct {
	mu      sync.Mutex
	members map[[2]int64]bool
	stored  []domain.Message
	idem    map[string]domain.Message
	nextID  int64
}

func newFakeMessageRepo() *fakeMessageRepo {
	return &fakeMessageRepo{
		members: make(map[[2]int64]bool),
		idem:    make(map[string]domain.Message),
	}
}

func (f *fakeMessageRepo) IsMember(_ context.Context, userID, channelID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.members[[2]int64{userID, channelID}], nil
}

func (f *fakeMessageRepo) InsertMessage(_ context.Context, in domain.Message) (domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := in.ClientMsgID
	if _, dup := f.idem[key]; dup {
		return domain.Message{}, store.ErrDuplicate
	}
	f.nextID++
	in.ID = f.nextID
	in.CreatedAt = time.Now().UTC()
	f.stored = append(f.stored, in)
	f.idem[key] = in
	return in, nil
}

func (f *fakeMessageRepo) FindMessageByClientMsgID(_ context.Context, channelID int64, clientMsgID string) (domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.idem[clientMsgID]; ok && m.ChannelID == channelID {
		return m, nil
	}
	return domain.Message{}, store.ErrNotFound
}

func (f *fakeMessageRepo) RecentMessages(_ context.Context, channelID int64, limit int) ([]domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Message
	for i := len(f.stored) - 1; i >= 0 && len(out) < limit; i-- {
		if f.stored[i].ChannelID == channelID {
			out = append(out, f.stored[i])
		}
	}
	return out, nil
}

func (f *fakeMessageRepo) MessagesBefore(_ context.Context, channelID, beforeID int64, limit int) ([]domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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

func newFullDeps(t *testing.T) (transport.Deps, *fakeMessageRepo, *hub.Hub) {
	t.Helper()
	r, err := render.New()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	repo := newFakeMessageRepo()
	h := hub.New()
	reader := &stubReader{
		channels: []domain.Channel{{ID: 10, Name: "general"}},
		users:    []domain.User{{ID: 1, DisplayName: "alice"}},
		recent:   map[int64][]domain.Message{},
		repo:     repo,
	}
	deps := transport.Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Snapshot: snapshot.New(reader, 20),
		Renderer: r,
		Messages: message.New(repo),
		Hub:      h,
		Members:  repo,
	}
	return deps, repo, h
}

type fakeWebhookAdmin struct {
	created   store.CreateWebhookInput
	deleted   int64
	botUserID int64
	channelID int64
}

func (f *fakeWebhookAdmin) FindWebhookBotUserIDByChannel(_ context.Context, _ int64) (int64, error) {
	if f.botUserID != 0 {
		return f.botUserID, nil
	}
	return 3, nil
}

func (f *fakeWebhookAdmin) FindWebhookChannelID(_ context.Context, _ int64) (int64, error) {
	if f.channelID != 0 {
		return f.channelID, nil
	}
	return 12, nil
}

func (f *fakeWebhookAdmin) CreateWebhook(_ context.Context, in store.CreateWebhookInput) (domain.Webhook, error) {
	f.created = in
	return domain.Webhook{ID: 1, ChannelID: in.ChannelID, Token: in.Token, Label: in.Label, BotUserID: in.BotUserID}, nil
}

func (f *fakeWebhookAdmin) DeleteWebhook(_ context.Context, id int64) error {
	f.deleted = id
	return nil
}

func TestUnwiredDependenciesPreserveResponses(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name        string
		method      string
		target      string
		body        string
		contentType string
		deps        func(*testing.T) transport.Deps
		wantStatus  int
		wantBody    string
	}{
		{
			name:       "root without snapshot",
			method:     http.MethodGet,
			target:     "/",
			deps:       func(*testing.T) transport.Deps { return transport.Deps{Logger: logger} },
			wantStatus: http.StatusOK,
			wantBody:   "slack-skeleton-go-htmx: snapshot未配線",
		},
		{
			name:       "workspace snapshot without service",
			method:     http.MethodGet,
			target:     "/workspaces/1/snapshot",
			deps:       func(*testing.T) transport.Deps { return transport.Deps{Logger: logger} },
			wantStatus: http.StatusInternalServerError,
			wantBody:   "snapshot service not wired\n",
		},
		{
			name:        "post message without service",
			method:      http.MethodPost,
			target:      "/channels/1/messages",
			body:        "body=hello&client_msg_id=unwired",
			contentType: "application/x-www-form-urlencoded",
			deps:        func(*testing.T) transport.Deps { return transport.Deps{Logger: logger} },
			wantStatus:  http.StatusInternalServerError,
			wantBody:    "messages not wired\n",
		},
		{
			name:       "message history without service",
			method:     http.MethodGet,
			target:     "/channels/1/messages",
			deps:       func(*testing.T) transport.Deps { return transport.Deps{Logger: logger} },
			wantStatus: http.StatusInternalServerError,
			wantBody:   "messages not wired\n",
		},
		{
			name:       "incoming webhook without service",
			method:     http.MethodPost,
			target:     "/api/webhooks/x",
			deps:       func(*testing.T) transport.Deps { return transport.Deps{Logger: logger} },
			wantStatus: http.StatusInternalServerError,
			wantBody:   "webhooks not wired\n",
		},
		{
			name:       "create webhook without admin",
			method:     http.MethodPost,
			target:     "/admin/webhooks",
			deps:       func(*testing.T) transport.Deps { return transport.Deps{Logger: logger} },
			wantStatus: http.StatusInternalServerError,
			wantBody:   "webhook admin not wired\n",
		},
		{
			name:       "delete webhook without admin",
			method:     http.MethodPost,
			target:     "/admin/webhooks/1/delete",
			deps:       func(*testing.T) transport.Deps { return transport.Deps{Logger: logger} },
			wantStatus: http.StatusInternalServerError,
			wantBody:   "webhook admin not wired\n",
		},
		{
			name:       "websocket without hub",
			method:     http.MethodGet,
			target:     "/ws?channel_ids=1",
			deps:       func(*testing.T) transport.Deps { return transport.Deps{Logger: logger} },
			wantStatus: http.StatusInternalServerError,
			wantBody:   "hub not wired\n",
		},
		{
			name:        "create webhook without membership checker",
			method:      http.MethodPost,
			target:      "/admin/webhooks",
			body:        "channel_id=12",
			contentType: "application/x-www-form-urlencoded",
			deps: func(t *testing.T) transport.Deps {
				deps, _, _ := newFullDeps(t)
				deps.WebhookAdmin = &fakeWebhookAdmin{}
				deps.Members = nil
				return deps
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "membership not wired\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := httptest.NewRecorder()
			transport.NewMux(tt.deps(t)).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status=%d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body=%q, want %q", got, tt.wantBody)
			}
		})
	}
}

func TestPostMessageRequiresClientMsgID(t *testing.T) {
	t.Parallel()

	deps, _, _ := newFullDeps(t)
	mux := transport.NewMux(deps)
	form := url.Values{"body": {"hi"}}
	req := httptest.NewRequest(http.MethodPost, "/channels/10/messages",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestPostMessageRejectsInvalidChannelID(t *testing.T) {
	t.Parallel()

	deps, _, _ := newFullDeps(t)
	mux := transport.NewMux(deps)
	form := url.Values{"body": {"hi"}, "client_msg_id": {"k1"}}
	req := httptest.NewRequest(http.MethodPost, "/channels/0/messages",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestPostMessageRejectsNonMember(t *testing.T) {
	t.Parallel()

	deps, _, _ := newFullDeps(t)
	mux := transport.NewMux(deps)
	form := url.Values{"body": {"hi"}, "client_msg_id": {"k1"}}
	req := httptest.NewRequest(http.MethodPost, "/channels/10/messages",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Id", "1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestPostMessageSucceedsForMemberAndPublishes(t *testing.T) {
	t.Parallel()

	deps, repo, h := newFullDeps(t)
	repo.members[[2]int64{1, 10}] = true

	mux := transport.NewMux(deps)
	form := url.Values{"body": {"hello"}, "client_msg_id": {"k1"}}
	req := httptest.NewRequest(http.MethodPost, "/channels/10/messages",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Id", "1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if h.SubscriberCount(10) != 0 {
		// Publishは購読者ゼロでも安全に動くことを期待します
		t.Fatalf("unexpected subscribers")
	}
	if len(repo.stored) != 1 {
		t.Fatalf("stored=%d", len(repo.stored))
	}
}

func TestPostMessageMarksDuplicateOnSecondSend(t *testing.T) {
	t.Parallel()

	deps, repo, _ := newFullDeps(t)
	repo.members[[2]int64{1, 10}] = true
	mux := transport.NewMux(deps)
	send := func() *httptest.ResponseRecorder {
		form := url.Values{"body": {"hi"}, "client_msg_id": {"dup1"}}
		req := httptest.NewRequest(http.MethodPost, "/channels/10/messages",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-User-Id", "1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	first := send()
	if first.Code != http.StatusNoContent {
		t.Fatalf("first status=%d", first.Code)
	}
	second := send()
	if second.Code != http.StatusNoContent {
		t.Fatalf("second status=%d", second.Code)
	}
	if second.Header().Get("X-Duplicate") != "1" {
		t.Fatalf("X-Duplicateが付いていません")
	}
}

func TestPostMessageConflictOnEditedRetry(t *testing.T) {
	t.Parallel()

	deps, repo, _ := newFullDeps(t)
	repo.members[[2]int64{1, 10}] = true
	mux := transport.NewMux(deps)
	send := func(body string) *httptest.ResponseRecorder {
		form := url.Values{"body": {body}, "client_msg_id": {"edited-retry-key"}}
		req := httptest.NewRequest(http.MethodPost, "/channels/10/messages",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-User-Id", "1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := send("最初の本文"); rec.Code != http.StatusNoContent {
		t.Fatalf("first status=%d", rec.Code)
	}
	// 同じキーで本文を変えた再送は無言の204ではなく409で拒否します
	if rec := send("編集した本文"); rec.Code != http.StatusConflict {
		t.Fatalf("edited retry status=%d, want 409", rec.Code)
	}
}

func TestWebhookHandlerSuccess204(t *testing.T) {
	t.Parallel()

	deps, repo, _ := newFullDeps(t)
	repo.members[[2]int64{3, 12}] = true
	deps.Webhooks = webhook.New(&fakeWebhookLookup{}, deps.Messages)

	mux := transport.NewMux(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/token", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(repo.stored) != 1 || repo.stored[0].Body != "hello" {
		t.Fatalf("stored=%+v", repo.stored)
	}
}

func TestWebhookHandlerNotFound404(t *testing.T) {
	t.Parallel()

	deps, _, _ := newFullDeps(t)
	deps.Webhooks = webhook.New(&fakeWebhookLookup{err: store.ErrNotFound}, deps.Messages)

	mux := transport.NewMux(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/missing", strings.NewReader(`{"text":"hello"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebhookHandlerBadBody400(t *testing.T) {
	t.Parallel()

	deps, repo, _ := newFullDeps(t)
	repo.members[[2]int64{3, 12}] = true
	deps.Webhooks = webhook.New(&fakeWebhookLookup{}, deps.Messages)

	mux := transport.NewMux(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/token", strings.NewReader(`{`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebhookHandlerUnauthorized401(t *testing.T) {
	t.Parallel()

	deps, repo, _ := newFullDeps(t)
	repo.members[[2]int64{3, 12}] = true
	deps.Webhooks = webhook.New(&fakeWebhookLookup{secret: "top-secret"}, deps.Messages)

	mux := transport.NewMux(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/token", strings.NewReader(`{"text":"hello"}`))
	req.Header.Set("X-Hub-Signature-256", "sha256=bad")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebhookHandlerAcceptsValidSignature(t *testing.T) {
	t.Parallel()

	body := `{"text":"hello"}`
	secret := "top-secret"
	deps, repo, _ := newFullDeps(t)
	repo.members[[2]int64{3, 12}] = true
	deps.Webhooks = webhook.New(&fakeWebhookLookup{secret: secret}, deps.Messages)

	mux := transport.NewMux(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/token", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signWebhookBody(secret, []byte(body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebhookHandlerBodyTooLarge(t *testing.T) {
	t.Parallel()

	deps, repo, _ := newFullDeps(t)
	repo.members[[2]int64{3, 12}] = true
	deps.Webhooks = webhook.New(&fakeWebhookLookup{}, deps.Messages)

	mux := transport.NewMux(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/token", strings.NewReader(strings.Repeat("x", 1<<20+1)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebhookHandlerTruncatesLongBodyAndPosts(t *testing.T) {
	t.Parallel()

	deps, repo, _ := newFullDeps(t)
	repo.members[[2]int64{3, 12}] = true
	deps.Webhooks = webhook.New(&fakeWebhookLookup{}, deps.Messages)

	// 上限4000文字を超える本文は400で落とさず、切り詰めて投稿します。
	// GitHubは失敗した配信を自動再送しないため、400はその通知の喪失を意味します。
	tooLong := `{"text":"` + strings.Repeat("x", 4001) + `"}`
	mux := transport.NewMux(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/token", strings.NewReader(tooLong))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.stored) != 1 {
		t.Fatalf("stored=%d", len(repo.stored))
	}
	body := repo.stored[0].Body
	if got := utf8.RuneCountInString(body); got > 4000 {
		t.Fatalf("body runes=%d", got)
	}
	if !strings.HasSuffix(body, "…") {
		t.Fatalf("body should end with ellipsis, got tail=%q", body[len(body)-8:])
	}
}

func TestCreateWebhookAdminCreatesAndRedirects(t *testing.T) {
	t.Parallel()

	deps, repo, _ := newFullDeps(t)
	repo.members[[2]int64{1, 12}] = true
	admin := &fakeWebhookAdmin{botUserID: 30}
	deps.WebhookAdmin = admin

	mux := transport.NewMux(deps)
	form := url.Values{
		"channel_id": {"12"},
		"label":      {"GitHub main"},
		"secret":     {"top-secret"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/webhooks", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "/" {
		t.Fatalf("Location=%q", rec.Header().Get("Location"))
	}
	if admin.created.ChannelID != 12 || admin.created.Label != "GitHub main" || admin.created.Secret != "top-secret" {
		t.Fatalf("created=%+v", admin.created)
	}
	if admin.created.BotUserID != 30 {
		t.Fatalf("bot user=%d", admin.created.BotUserID)
	}
	if len(admin.created.Token) != 64 {
		t.Fatalf("token len=%d token=%q", len(admin.created.Token), admin.created.Token)
	}
}

func TestCreateWebhookAdminRejectsInvalidChannel(t *testing.T) {
	t.Parallel()

	deps, _, _ := newFullDeps(t)
	deps.WebhookAdmin = &fakeWebhookAdmin{}

	mux := transport.NewMux(deps)
	form := url.Values{"channel_id": {"0"}, "label": {"GitHub main"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/webhooks", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestDeleteWebhookAdminDeletesAndRedirects(t *testing.T) {
	t.Parallel()

	deps, repo, _ := newFullDeps(t)
	repo.members[[2]int64{1, 12}] = true
	admin := &fakeWebhookAdmin{}
	deps.WebhookAdmin = admin

	mux := transport.NewMux(deps)
	req := httptest.NewRequest(http.MethodPost, "/admin/webhooks/42/delete", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if admin.deleted != 42 {
		t.Fatalf("deleted=%d", admin.deleted)
	}
}

func TestCreateWebhookAdminRejectsNonMember(t *testing.T) {
	t.Parallel()

	deps, _, _ := newFullDeps(t) // Membershipは空のまま
	deps.WebhookAdmin = &fakeWebhookAdmin{botUserID: 30}

	mux := transport.NewMux(deps)
	form := url.Values{"channel_id": {"12"}, "label": {"GitHub main"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/webhooks", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-User-Id", "1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestDeleteWebhookAdminRejectsNonMember(t *testing.T) {
	t.Parallel()

	deps, _, _ := newFullDeps(t) // Membershipは空のまま
	admin := &fakeWebhookAdmin{channelID: 12}
	deps.WebhookAdmin = admin

	mux := transport.NewMux(deps)
	req := httptest.NewRequest(http.MethodPost, "/admin/webhooks/42/delete", nil)
	req.Header.Set("X-User-Id", "1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d", rec.Code)
	}
	if admin.deleted != 0 {
		t.Fatalf("非メンバーの削除が実行されています: %d", admin.deleted)
	}
}

func TestHistoryHandlerRendersAscendingOrder(t *testing.T) {
	t.Parallel()

	deps, repo, _ := newFullDeps(t)
	repo.members[[2]int64{1, 10}] = true
	mux := transport.NewMux(deps)

	for i, body := range []string{"古いメッセージ", "新しいメッセージ"} {
		form := url.Values{"body": {body}, "client_msg_id": {"asc-" + strconv.Itoa(i)}}
		req := httptest.NewRequest(http.MethodPost, "/channels/10/messages",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-User-Id", "1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}

	req := httptest.NewRequest(http.MethodGet, "/channels/10/messages?limit=10", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	body := rec.Body.String()
	// hx-swap="afterbegin"でひとかたまり挿入するUIの前提として、時系列昇順を要求します。
	oldIdx := strings.Index(body, "古いメッセージ")
	newIdx := strings.Index(body, "新しいメッセージ")
	if oldIdx < 0 || newIdx < 0 || oldIdx > newIdx {
		t.Fatalf("昇順になっていません: %s", body)
	}
}

type fakeWebhookLookup struct {
	secret string
	err    error
}

func (f *fakeWebhookLookup) FindWebhookWithSecret(_ context.Context, token string) (domain.WebhookWithSecret, error) {
	if f.err != nil {
		return domain.WebhookWithSecret{}, f.err
	}
	return domain.WebhookWithSecret{
		Webhook: domain.Webhook{
			ID:        1,
			ChannelID: 12,
			Token:     token,
			Label:     "dev",
			BotUserID: 3,
			CreatedAt: time.Now().UTC(),
		},
		Secret: f.secret,
	}, nil
}

func signWebhookBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestHistoryHandlerReturnsRenderedMessages(t *testing.T) {
	t.Parallel()

	deps, repo, _ := newFullDeps(t)
	repo.members[[2]int64{1, 10}] = true
	mux := transport.NewMux(deps)

	// 2件投入
	for i := 0; i < 2; i++ {
		form := url.Values{"body": {"x"}, "client_msg_id": {time.Now().Format("150405.000000000")}}
		req := httptest.NewRequest(http.MethodPost, "/channels/10/messages",
			strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-User-Id", "1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		time.Sleep(time.Microsecond)
	}

	req := httptest.NewRequest(http.MethodGet, "/channels/10/messages?limit=10", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "msg-") {
		t.Fatalf("history body=%s", rec.Body.String())
	}
}

func TestHistoryHandlerRejectsInvalidChannelID(t *testing.T) {
	t.Parallel()

	deps, _, _ := newFullDeps(t)
	mux := transport.NewMux(deps)
	req := httptest.NewRequest(http.MethodGet, "/channels/abc/messages", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestHistoryHandlerRejectsUnparsableCursorParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantBody   string
	}{
		{name: "beforeが数値でない", query: "?before=abc", wantStatus: http.StatusBadRequest, wantBody: "invalid before\n"},
		{name: "limitが数値でない", query: "?limit=all", wantStatus: http.StatusBadRequest, wantBody: "invalid limit\n"},
		{name: "未指定は従来どおり最新から", query: "", wantStatus: http.StatusOK},
		{name: "空文字も未指定と同じ", query: "?before=&limit=", wantStatus: http.StatusOK},
		{name: "数値なら従来どおり", query: "?before=5&limit=10", wantStatus: http.StatusOK},
		{name: "intに収まらないlimitは400", query: "?limit=99999999999999999999", wantStatus: http.StatusBadRequest, wantBody: "invalid limit\n"},
		{name: "int64に収まらないbeforeは400", query: "?before=99999999999999999999", wantStatus: http.StatusBadRequest, wantBody: "invalid before\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, repo, _ := newFullDeps(t)
			repo.members[[2]int64{1, 10}] = true
			mux := transport.NewMux(deps)
			req := httptest.NewRequest(http.MethodGet, "/channels/10/messages"+tt.query, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status=%d, want %d (body=%q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Fatalf("body=%q, want %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestPostMessageReturnsErrorWhenMessagesNotWired(t *testing.T) {
	t.Parallel()

	mux := transport.NewMux(transport.Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	form := url.Values{"body": {"x"}, "client_msg_id": {"k"}}
	req := httptest.NewRequest(http.MethodPost, "/channels/1/messages",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", rec.Code)
	}
}

func newDeps(t *testing.T) transport.Deps {
	t.Helper()
	r, err := render.New()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	reader := &stubReader{
		channels: []domain.Channel{{ID: 10, Name: "general"}},
		users:    []domain.User{{ID: 1, DisplayName: "alice"}},
		recent: map[int64][]domain.Message{
			10: {{ID: 1, ChannelID: 10, Body: "hi", CreatedAt: time.Now().UTC()}},
		},
	}
	return transport.Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Snapshot: snapshot.New(reader, 20),
		Renderer: r,
	}
}

func TestHealthzReturnsOK(t *testing.T) {
	t.Parallel()

	mux := transport.NewMux(newDeps(t))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestSnapshotHandlerReturnsHTML(t *testing.T) {
	t.Parallel()

	mux := transport.NewMux(newDeps(t))
	req := httptest.NewRequest(http.MethodGet, "/workspaces/1/snapshot", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type=%q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"alice", "#general", "msg-1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestSnapshotHandlerRejectsInvalidID(t *testing.T) {
	t.Parallel()

	mux := transport.NewMux(newDeps(t))
	req := httptest.NewRequest(http.MethodGet, "/workspaces/abc/snapshot", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestRootRendersSnapshotWhenWired(t *testing.T) {
	t.Parallel()

	mux := transport.NewMux(newDeps(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "alice") {
		t.Fatalf("missing alice")
	}
}

func TestRootFallsBackWhenSnapshotMissing(t *testing.T) {
	t.Parallel()

	mux := transport.NewMux(transport.Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "snapshot未配線") {
		t.Fatalf("got=%s", rec.Body.String())
	}
}

func TestSnapshotHandlerReturnsErrorWhenServiceMissing(t *testing.T) {
	t.Parallel()

	mux := transport.NewMux(transport.Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	req := httptest.NewRequest(http.MethodGet, "/workspaces/1/snapshot", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestCurrentUserIDUsesHeader(t *testing.T) {
	t.Parallel()

	mux := transport.NewMux(newDeps(t))
	req := httptest.NewRequest(http.MethodGet, "/workspaces/1/snapshot", nil)
	req.Header.Set("X-User-Id", "42")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestCurrentUserIDFallsBackOnInvalidHeader(t *testing.T) {
	t.Parallel()

	mux := transport.NewMux(newDeps(t))
	req := httptest.NewRequest(http.MethodGet, "/workspaces/1/snapshot", nil)
	req.Header.Set("X-User-Id", "not-a-number")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
}
