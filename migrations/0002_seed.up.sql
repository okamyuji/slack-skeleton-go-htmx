-- 動作確認用のseedデータ
-- 1 workspace、3 channel、2 user、両ユーザーが全チャンネルに参加します

INSERT INTO workspaces (id, name) VALUES (1, 'Demo Workspace')
  ON DUPLICATE KEY UPDATE name=VALUES(name);

INSERT INTO users (id, workspace_id, display_name) VALUES
  (1, 1, 'alice'),
  (2, 1, 'bob')
  ON DUPLICATE KEY UPDATE display_name=VALUES(display_name);

INSERT INTO channels (id, workspace_id, name) VALUES
  (10, 1, 'general'),
  (11, 1, 'random'),
  (12, 1, 'dev')
  ON DUPLICATE KEY UPDATE name=VALUES(name);

INSERT INTO memberships (user_id, channel_id) VALUES
  (1, 10), (1, 11), (1, 12),
  (2, 10), (2, 11), (2, 12)
  ON DUPLICATE KEY UPDATE joined_at=joined_at;
