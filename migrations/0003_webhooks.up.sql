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

INSERT INTO users (workspace_id, display_name, is_bot)
SELECT w.id, 'webhook-bot', TRUE
  FROM workspaces w
 WHERE w.id = 1
   AND NOT EXISTS (
     SELECT 1 FROM users u
      WHERE u.workspace_id = w.id
        AND u.display_name = 'webhook-bot'
   );

UPDATE users
   SET is_bot = TRUE
 WHERE workspace_id = 1
   AND display_name = 'webhook-bot';

INSERT INTO memberships (user_id, channel_id)
SELECT u.id, c.id
  FROM users u
  JOIN channels c ON c.workspace_id = u.workspace_id
 WHERE u.workspace_id = 1
   AND u.display_name = 'webhook-bot'
  ON DUPLICATE KEY UPDATE joined_at=joined_at;
