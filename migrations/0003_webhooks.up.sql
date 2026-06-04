-- Incoming Webhook用の最小スキーマとボットユーザー

ALTER TABLE users ADD COLUMN is_bot BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS webhooks (
  id          BIGINT PRIMARY KEY AUTO_INCREMENT,
  channel_id  BIGINT NOT NULL,
  token       VARCHAR(64) NOT NULL,
  label       VARCHAR(255) NOT NULL DEFAULT '',
  secret      VARCHAR(255) NOT NULL DEFAULT '',
  bot_user_id BIGINT NOT NULL,
  created_at  DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  UNIQUE KEY uk_webhooks_token (token),
  INDEX idx_webhooks_channel (channel_id),
  CONSTRAINT fk_webhooks_channel FOREIGN KEY (channel_id) REFERENCES channels(id),
  CONSTRAINT fk_webhooks_bot FOREIGN KEY (bot_user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO users (id, workspace_id, display_name, is_bot) VALUES
  (3, 1, 'webhook-bot', TRUE)
  ON DUPLICATE KEY UPDATE display_name=VALUES(display_name), is_bot=VALUES(is_bot);

INSERT INTO memberships (user_id, channel_id) VALUES
  (3, 10), (3, 11), (3, 12)
  ON DUPLICATE KEY UPDATE joined_at=joined_at;
