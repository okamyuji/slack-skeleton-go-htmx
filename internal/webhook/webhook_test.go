package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/message"
)

func TestVerifySignature(t *testing.T) {
	t.Parallel()

	body := []byte(`{"text":"hello"}`)
	secret := "top-secret"
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	valid := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	tests := []struct {
		name      string
		secret    string
		signature string
		wantErr   bool
	}{
		{name: "valid", secret: secret, signature: valid},
		{name: "invalid", secret: secret, signature: "sha256=bad", wantErr: true},
		{name: "empty secret skips verification", secret: "", signature: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifySignature(tt.secret, body, tt.signature)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGenerateClientMsgID(t *testing.T) {
	t.Parallel()

	body := []byte(`{"text":"hello"}`)
	got := generateClientMsgID("token", body)
	if got == "" {
		t.Fatal("id is empty")
	}
	if len(got) > 64 {
		t.Fatalf("len=%d, want <= 64", len(got))
	}
	if got != generateClientMsgID("token", body) {
		t.Fatal("same input produced different ids")
	}
	if got == generateClientMsgID("token", []byte(`{"text":"other"}`)) {
		t.Fatal("different bodies produced same id")
	}
}

func TestFormatGitHubPush(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"ref":"refs/heads/main",
		"repository":{"name":"my-repo"},
		"pusher":{"name":"alice"},
		"commits":[
			{"id":"abc1234567","message":"Fix bug\n\nbody"},
			{"id":"def5678901","message":"Add feature"}
		]
	}`)

	got, err := formatGitHubPush(body)
	if err != nil {
		t.Fatalf("formatGitHubPush: %v", err)
	}
	for _, want := range []string{
		"[my-repo] alice pushed 2 commit(s) to main",
		"abc1234 Fix bug",
		"def5678 Add feature",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted push missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "refs/heads/main") {
		t.Fatalf("ref prefix was not stripped: %q", got)
	}
}

func TestFormatGitHubPushNoCommits(t *testing.T) {
	t.Parallel()

	got, err := formatGitHubPush([]byte(`{
		"ref":"refs/heads/main",
		"repository":{"name":"my-repo"},
		"pusher":{"name":"alice"},
		"commits":[]
	}`))
	if err != nil {
		t.Fatalf("formatGitHubPush: %v", err)
	}
	if !strings.Contains(got, "(no commits)") {
		t.Fatalf("formatted push missing no commits marker: %q", got)
	}
}

func TestFormatGitHubPushInvalidJSON(t *testing.T) {
	t.Parallel()

	if _, err := formatGitHubPush([]byte(`{`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseGenericPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    []byte
		want    string
		wantErr bool
	}{
		{name: "valid", body: []byte(`{"text":"hello"}`), want: "hello"},
		{name: "empty text", body: []byte(`{"text":""}`), wantErr: true},
		{name: "missing text", body: []byte(`{"body":"hello"}`), wantErr: true},
		{name: "invalid json", body: []byte(`{`), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGenericPayload(tt.body)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHandlePayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		headers   http.Header
		body      []byte
		storeErr  error
		secret    string
		duplicate bool
		wantBody  string
		wantErr   error
		wantDup   bool
		wantSend  bool
	}{
		{
			name:     "generic success",
			body:     []byte(`{"text":"hello"}`),
			wantBody: "hello",
			wantSend: true,
		},
		{
			name: "github push success",
			headers: http.Header{
				"X-Github-Event": []string{"push"},
			},
			body: []byte(`{
				"ref":"refs/heads/main",
				"repository":{"name":"my-repo"},
				"pusher":{"name":"alice"},
				"commits":[{"id":"abc1234567","message":"Fix bug"}]
			}`),
			wantBody: "abc1234 Fix bug",
			wantSend: true,
		},
		{
			name:     "token not found",
			body:     []byte(`{"text":"hello"}`),
			storeErr: errStoreNotFound,
			wantErr:  errStoreNotFound,
		},
		{
			name: "signature failure",
			body: []byte(`{"text":"hello"}`),
			headers: http.Header{
				"X-Hub-Signature-256": []string{"sha256=bad"},
			},
			secret:  "top-secret",
			wantErr: ErrUnauthorized,
		},
		{
			name:      "duplicate idempotent",
			body:      []byte(`{"text":"hello"}`),
			duplicate: true,
			wantBody:  "hello",
			wantDup:   true,
			wantSend:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeWebhookStore{secret: tt.secret, err: tt.storeErr}
			sender := &fakeMessageSender{duplicate: tt.duplicate}
			svc := New(store, sender)

			got, dup, err := svc.HandlePayload(context.Background(), "token", tt.headers, tt.body)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err=%v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dup != tt.wantDup {
				t.Fatalf("duplicate=%v, want %v", dup, tt.wantDup)
			}
			if got.Body != sender.in.Body {
				t.Fatalf("returned body=%q, sent body=%q", got.Body, sender.in.Body)
			}
			if tt.wantSend && !strings.Contains(sender.in.Body, tt.wantBody) {
				t.Fatalf("sent body=%q, want contains %q", sender.in.Body, tt.wantBody)
			}
			if sender.in.ChannelID != 12 || sender.in.UserID != 3 {
				t.Fatalf("sent IDs channel=%d user=%d", sender.in.ChannelID, sender.in.UserID)
			}
		})
	}
}

var errStoreNotFound = errors.New("store: not found")

type fakeWebhookStore struct {
	secret string
	err    error
}

func (f *fakeWebhookStore) FindWebhookWithSecret(_ context.Context, token string) (domain.WebhookWithSecret, error) {
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

type fakeMessageSender struct {
	in        message.SendInput
	duplicate bool
}

func (f *fakeMessageSender) Send(_ context.Context, in message.SendInput) (domain.Message, bool, error) {
	f.in = in
	return domain.Message{
		ID:          101,
		ChannelID:   in.ChannelID,
		UserID:      in.UserID,
		Body:        in.Body,
		ClientMsgID: in.ClientMsgID,
		CreatedAt:   time.Now().UTC(),
	}, f.duplicate, nil
}
