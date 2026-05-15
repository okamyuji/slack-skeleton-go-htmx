# slack-skeleton-go-htmx

Slackのコアにある「永続化されたチャットの順序付きリアルタイム配信」だけを、Goの標準ライブラリとhtmxで再実装した教材プロジェクトです。

各章は同じリポジトリ上のブランチに対応します。ブランチを切り替えるとその章までの最小コードを写経できます。

## 章とブランチ

| 章 | ブランチ / タグ | 主な内容 |
|---|---|---|
| 1 | `ch01-skeleton` | 骨格、Dockerfile、`compose.yml`、pre-commit、gitleaks、govulncheck、CI、品質ゲート |
| 2 | `ch02-domain` | ドメイン型、自作マイグレータ、`internal/store`のMySQL接続 |
| 3 | `ch03-snapshot` | Snapshotサービス、`html/template`レンダラ、初期画面ハンドラ |
| 4 | `ch04-send` | POST `/channels/:id/messages`、冪等性、カーソルベース履歴取得 |
| 6 | `ch06-hub-ws` | in-process Hub、`/ws`ハンドラ、HTMLフラグメント配信 |
| 8 | `ch08-toast` | メンション含むメッセージ受信時の画面内トースト |

Outboxは10章で「触れるだけ」とし、専用ブランチは設けません。

## 依存

- Go 1.25以降
- MySQL 8.4(`compose.yml`で起動できます)
- 開発支援ツール
  - `staticcheck`(`go install honnef.co/go/tools/cmd/staticcheck@latest`)
  - `golangci-lint`(`brew install golangci-lint`など)
  - `gitleaks`(`brew install gitleaks`など)
  - `govulncheck`(`go install golang.org/x/vuln/cmd/govulncheck@latest`)
  - `pre-commit`(`pipx install pre-commit`など)

Goの外部依存は2つだけです。

```text
github.com/go-sql-driver/mysql
github.com/coder/websocket
```

## 起動

```sh
docker compose -f compose.yml up -d mysql
DB_DSN='appuser:apppass@tcp(127.0.0.1:3306)/slack_skeleton?parseTime=true&loc=UTC' make run
open http://localhost:8080/
```

ブラウザを2タブ開き、片方でメッセージを送信するともう片方にWebSocket経由でリアルタイムに反映されます。

## 品質ゲート

手動、`pre-commit`、CIのいずれからも同じ`scripts/quality-gate.sh`を呼びます。

```sh
make gate              # 単体テストのみ
make gate-integration  # 統合テスト込み(MySQLが必要です)
```

このスクリプトは次を順に通します。

1. `gofmt`差分検知
2. `go vet ./...`
3. `staticcheck ./...`
4. `golangci-lint run ./...`
5. `gitleaks detect`
6. `govulncheck ./...`
7. `go test ./... -race -shuffle=on -count=1`
8. `go build ./...`

pre-commitはこれらを個別フックとして登録しており、コミット時にローカルで自動実行します。
