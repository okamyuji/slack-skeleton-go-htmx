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

	clientMsgID, err := clientMsgIDFor(token, headers, body)
	if err != nil {
		return domain.Message{}, false, err
	}

	return s.messages.Send(ctx, message.SendInput{
		ChannelID:   wh.ChannelID,
		UserID:      wh.BotUserID,
		Body:        text,
		ClientMsgID: clientMsgID,
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

// clientMsgIDFor 配信の同一性を表す冪等キーを決めます。
// 本文のハッシュをキーにすると、定期通知のような「同一本文の別イベント」まで
// 重複として消えてしまうため、本文ではなくイベントの識別子から導出します。
//
//   - GitHub配信: 配信ごとに一意なX-GitHub-Deliveryヘッダをキーにします。
//     重複扱いになるのは同一配信のredeliveryだけです。
//   - 汎用形式: 呼び出し側が明示したclient_msg_idフィールドをキーにします。
//     指定がなければ重複排除を行わず、毎回ランダムなキーを採番します。
func clientMsgIDFor(token string, headers http.Header, body []byte) (string, error) {
	if delivery := strings.TrimSpace(headers.Get("X-GitHub-Delivery")); delivery != "" {
		return hashedClientMsgID(token, delivery), nil
	}
	var payload struct {
		ClientMsgID string `json:"client_msg_id"`
	}
	// 形式エラーはpayloadTextが先に検出しているため、ここでは無視できます。
	_ = json.Unmarshal(body, &payload)
	if key := strings.TrimSpace(payload.ClientMsgID); key != "" {
		return hashedClientMsgID(token, key), nil
	}
	return randomClientMsgID()
}

func hashedClientMsgID(token, eventID string) string {
	h := sha256.Sum256([]byte(token + ":" + eventID))
	return "wh-" + hex.EncodeToString(h[:])[:28]
}

func randomClientMsgID() (string, error) {
	b := make([]byte, 12)
	if _, err := crypto_rand.Read(b); err != nil {
		return "", fmt.Errorf("webhook client_msg_id: %w", err)
	}
	return "wh-" + hex.EncodeToString(b), nil
}

// maxBodyRunes 投稿本文の上限です。message.Serviceが同じ値で検査するため、
// ここで超えたまま渡すとErrInvalidInputになり400を返すことになります。
// GitHubは失敗した配信を自動では再送しないので、400にするとその通知は
// 二度と届きません。長すぎる本文は落とさず切り詰めて投稿します。
const maxBodyRunes = 4000

func payloadText(headers http.Header, body []byte) (string, error) {
	var (
		text string
		err  error
	)
	if strings.EqualFold(headers.Get("X-GitHub-Event"), "push") {
		text, err = formatGitHubPush(body)
	} else {
		text, err = parseGenericPayload(body)
	}
	if err != nil {
		return "", err
	}
	return truncateRunes(text, maxBodyRunes), nil
}

// truncateRunes 本文をルーン単位でn文字までに収めます。
// 切り詰めたことが読み手に分かるよう、末尾を省略記号に置き換えます。
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	const ellipsis = "…"
	return string(runes[:n-len([]rune(ellipsis))]) + ellipsis
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
