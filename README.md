# slack-skeleton-go-htmx

Slackのコアにある「永続化されたチャットの順序付きリアルタイム配信」だけを、Goの標準ライブラリと[htmx](https://htmx.org/)で再実装した教材プロジェクトです。

各章は同じリポジトリ上のブランチに対応します。ブランチを切り替えるとその章までの最小コードを写経できます。

外部Go依存は2つだけです。

```text
github.com/go-sql-driver/mysql
github.com/coder/websocket
```

ルータ、テンプレート、マイグレータ、ロガー、ID生成、テストフレームワーク、設定読み込みはすべて[Go標準ライブラリ](https://pkg.go.dev/std)で賄います。htmxはCDNから読み込むのでGo側の依存にはなりません。

## アーキテクチャ概略

```
   Browser (htmx)
   ├── HTTP POST  /channels/:id/messages   ──┐ 送信
   └── WebSocket  /ws?channel_ids=...       ──┘ 受信
                    │
                    ▼
        ┌───────────────────────┐
        │   HTTP API Server     │
        │   net/http.ServeMux   │
        └──────┬──────────┬─────┘
               │          │
        ┌──────▼──────┐   │
        │ MessageSvc  │   │
        │  Persist    │   │
        └──────┬──────┘   │
               │          │
        ┌──────▼──────────▼─────┐
        │   In-Process Hub       │
        │  channel_id → conns    │
        └──────┬─────────────────┘
               │ broadcast (HTMLフラグメント)
               ▼
          WebSocket clients

        ┌────────────────────┐
        │  MySQL 8           │
        │  chat history      │
        └────────────────────┘
```

Slack内部の各コンポーネントとの対応です。

| 本物のSlack | 本記事の表現 |
|---|---|
| Chat Service | `internal/message` |
| Gateway Server | `internal/hub` |
| Snapshot Service | `internal/snapshot` |
| Real-Time API | `internal/transport`のWebSocketハンドラ |
| Web API | `net/http.ServeMux`配下のハンドラ |

## 章とブランチ

| 章 | ブランチ / タグ | 主な内容 |
|---|---|---|
| 1 | `ch01-skeleton` | 骨格、Dockerfile、`compose.yml`、pre-commit、gitleaks、govulncheck、CI、品質ゲート |
| 2 | `ch02-domain` | ドメイン型、自作マイグレータ、`internal/store`のMySQL接続、testcontainers統合テスト |
| 3 | `ch03-snapshot` | Snapshotサービス、`html/template`レンダラ、初期画面ハンドラ |
| 4 | `ch04-send` | POST `/channels/:id/messages`、冪等性、カーソルベース履歴取得、Repositoryインターフェイス |
| 5 | `ch05-hub-ws` | in-process Hub、`/ws`ハンドラ、HTMLフラグメント配信 |
| 6 | `ch06-toast` | メンション含むメッセージ受信時の画面内トースト、seedデータ |

各ブランチには同名のtagも打ってあるので、`git checkout ch04-send`または`git checkout ch04-send`(tag)で章のスナップショットを取り出せます。

## 動作要件

- Go 1.25以降
- Docker(MySQLコンテナ起動と統合テスト用のtestcontainers)
- 開発支援ツール(品質ゲート実行に必要)
  - `staticcheck`: `go install honnef.co/go/tools/cmd/staticcheck@latest`
  - `golangci-lint`: `brew install golangci-lint` など
  - `gitleaks`: `brew install gitleaks` など
  - `govulncheck`: `go install golang.org/x/vuln/cmd/govulncheck@latest`
  - `pre-commit`: `pipx install pre-commit` など

## ローカル起動

```sh
docker compose -f compose.yml up -d mysql
DB_DSN='appuser:apppass@tcp(127.0.0.1:3306)/slack_skeleton?parseTime=true&loc=UTC' make run
open http://localhost:8080/
```

ブラウザを2タブ開き、片方でメッセージを送信するともう片方にWebSocket経由でリアルタイムに反映されます。`@`を含む本文を送ると右下に画面内トーストが出ます。

## 品質ゲート

手動、`pre-commit`、CIのいずれからも同じ`scripts/quality-gate.sh`を呼びます。

```sh
make gate              # 単体テストのみ
make gate-integration  # 統合テスト込み(testcontainersでMySQLを自動起動)
```

スクリプトは次を順に通します。

1. `gofmt`差分検知
2. `go vet ./...`
3. `staticcheck ./...`
4. `golangci-lint run ./...`
5. `gitleaks detect`
6. `govulncheck ./...`
7. `go test ./... -race -shuffle=on -count=1`
8. `go build ./...`

`pre-commit`はこれらを個別フックとして登録しており、コミット時にローカルで自動実行します。CIは同じスクリプトをUbuntu runner上で1ステップとして実行します。

## テスト方針

- repository層は[testcontainers-go](https://golang.testcontainers.org/modules/mysql/)でMySQL 8.4をテスト内から起動して当てます。`DB_DSN`環境変数は不要です。
- サービス層は具象`*store.Store`ではなく`message.Repository`や`snapshot.Reader`といったインターフェイスに依存させ、in-memoryのfakeで単体テストします。
- 統合テストはGoの[build tag](https://pkg.go.dev/cmd/go#hdr-Build_constraints)で`//go:build integration`として隔離してあるため、デフォルトの`go test ./...`では実行されません。
- 合計カバレッジは83%程度を保ちます(`go test -coverprofile=...`で計測)。

## Docker Image

```sh
docker compose -f compose.yml up --build
```

`Dockerfile`は3段のマルチステージ構成(deps → builder → runtime)で、最終イメージは[distroless static](https://github.com/GoogleContainerTools/distroless)に基づきnonrootユーザーで動かします。

## ライセンス

MIT License

(c) okamyuji

## 参考

- [Slack Engineering Blog](https://slack.engineering/)
- [Keith Adams: How Slack Works (QCon SF 2016)](https://www.infoq.com/presentations/slack-infrastructure/)
- [Bing Wei: Scaling Slack (QCon SF 2017)](https://www.infoq.com/presentations/slack-scalability/)
- [Flannel: An Application-Level Edge Cache to Make Slack Scale](https://slack.engineering/flannel-an-application-level-edge-cache-to-make-slack-scale-b8a6400e2f6b)
- [htmx WebSocket extension](https://htmx.org/extensions/ws/)
