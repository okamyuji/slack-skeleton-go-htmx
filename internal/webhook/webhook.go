// Package webhook Incoming Webhookの認証、payload変換、投稿連携を担います。
package webhook

import (
	"context"
	"crypto/hmac"
	crypto_rand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/domain"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/message"
)

// Sentinel errors for transport status mapping.
var (
	ErrUnauthorized = errors.New("webhook: unauthorized")
	ErrBadPayload   = errors.New("webhook: bad payload")
)

type webhookStore interface {
	FindWebhookWithSecret(ctx context.Context, token string) (domain.WebhookWithSecret, error)
}

// GenerateToken Webhook URL用の64文字hexトークンを生成します。
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := crypto_rand.Read(b); err != nil {
		return "", fmt.Errorf("generate webhook token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

type messageSender interface {
	Send(ctx context.Context, in message.SendInput) (domain.Message, bool, error)
}

// Service Webhook payloadを既存messageパイプラインへ合流させます。
type Service struct {
	store    webhookStore
	messages messageSender
}

// New Serviceを構築します。
func New(store webhookStore, messages messageSender) *Service {
	return &Service{store: store, messages: messages}
}

// HandlePayload token認証、署名検証、payload変換、冪等送信をまとめて実行します。
func (s *Service) HandlePayload(
	ctx context.Context,
	token string,
	headers http.Header,
	body []byte,
) (domain.Message, bool, error) {
	wh, err := s.store.FindWebhookWithSecret(ctx, token)
	if err != nil {
		return domain.Message{}, false, err
	}
	if err := verifySignature(wh.Secret, body, headers.Get("X-Hub-Signature-256")); err != nil {
		return domain.Message{}, false, err
	}

	text, err := payloadText(headers, body)
	if err != nil {
		return domain.Message{}, false, err
	}

	return s.messages.Send(ctx, message.SendInput{
		ChannelID:   wh.ChannelID,
		UserID:      wh.BotUserID,
		Body:        text,
		ClientMsgID: generateClientMsgID(token, body),
	})
}

func verifySignature(secret string, payload []byte, signatureHeader string) error {
	if secret == "" {
		return nil
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return ErrUnauthorized
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signatureHeader, prefix))
	if err != nil {
		return ErrUnauthorized
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrUnauthorized
	}
	return nil
}

func generateClientMsgID(token string, body []byte) string {
	h := sha256.Sum256(append([]byte(token+":"), body...))
	return "wh-" + hex.EncodeToString(h[:])[:28]
}

func payloadText(headers http.Header, body []byte) (string, error) {
	if strings.EqualFold(headers.Get("X-GitHub-Event"), "push") {
		return formatGitHubPush(body)
	}
	return parseGenericPayload(body)
}

func formatGitHubPush(body []byte) (string, error) {
	var push struct {
		Ref        string `json:"ref"`
		Repository struct {
			Name string `json:"name"`
		} `json:"repository"`
		Pusher struct {
			Name string `json:"name"`
		} `json:"pusher"`
		Commits []struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(body, &push); err != nil {
		return "", fmt.Errorf("%w: github push json", ErrBadPayload)
	}

	branch := strings.TrimPrefix(push.Ref, "refs/heads/")
	if branch == "" {
		branch = push.Ref
	}
	var b strings.Builder
	if len(push.Commits) == 0 {
		_, _ = fmt.Fprintf(&b, "[%s] %s pushed to %s (no commits)", push.Repository.Name, push.Pusher.Name, branch)
		return b.String(), nil
	}

	_, _ = fmt.Fprintf(
		&b,
		"[%s] %s pushed %d commit(s) to %s",
		push.Repository.Name,
		push.Pusher.Name,
		len(push.Commits),
		branch,
	)
	for _, commit := range push.Commits {
		id := commit.ID
		if len(id) > 7 {
			id = id[:7]
		}
		msg := strings.SplitN(commit.Message, "\n", 2)[0]
		_, _ = fmt.Fprintf(&b, "\n- %s %s", id, msg)
	}
	return b.String(), nil
}

func parseGenericPayload(body []byte) (string, error) {
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("%w: generic json", ErrBadPayload)
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		return "", fmt.Errorf("%w: text required", ErrBadPayload)
	}
	return text, nil
}
