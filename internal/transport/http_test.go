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
	"strings"
	"sync"
	"testing"
	"time"

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
	webhooks []domain.WebhookSetting
}

func (s *stubReader) ListChannelsByWorkspace(_ context.Context, _ int64) ([]domain.Channel, error) {
	return s.channels, nil
}
func (s *stubReader) ListUsersByWorkspace(_ context.Context, _ int64) ([]domain.User, error) {
	return s.users, nil
}
func (s *stubReader) RecentMessages(_ context.Context, channelID int64, _ int) ([]domain.Message, error) {
	return s.recent[channelID], nil
}
func (s *stubReader) ListWebhookSettingsByWorkspace(_ context.Context, _ int64) ([]domain.WebhookSetting, error) {
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
	}
	deps := transport.Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Snapshot: snapshot.New(reader, 20),
		Renderer: r,
		Messages: message.New(repo),
		Hub:      h,
	}
	return deps, repo, h
}

type fakeWebhookAdmin struct {
	created store.CreateWebhookInput
	deleted int64
}

func (f *fakeWebhookAdmin) CreateWebhook(_ context.Context, in store.CreateWebhookInput) (domain.Webhook, error) {
	f.created = in
	return domain.Webhook{ID: 1, ChannelID: in.ChannelID, Token: in.Token, Label: in.Label, BotUserID: in.BotUserID}, nil
}

func (f *fakeWebhookAdmin) DeleteWebhook(_ context.Context, id int64) error {
	f.deleted = id
	return nil
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

func TestCreateWebhookAdminCreatesAndRedirects(t *testing.T) {
	t.Parallel()

	deps, _, _ := newFullDeps(t)
	admin := &fakeWebhookAdmin{}
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
	if admin.created.BotUserID != 3 {
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

	deps, _, _ := newFullDeps(t)
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
