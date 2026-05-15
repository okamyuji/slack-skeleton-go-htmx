# slack-skeleton-go-htmx

Slackのコアにある「永続化されたチャットの順序付きリアルタイム配信」だけを、Goの標準ライブラリとhtmxで再実装した教材プロジェクトです。

各章は同じリポジトリ上の `ch01`、`ch02`、`ch03`...というブランチに対応します。ブランチを切り替えると、その章までの最小コードを写経できます。

## 依存

- Go 1.22 以降
- MySQL 8.4(`compose.yml` で起動できます)
- 開発支援ツール
  - `staticcheck`(`go install honnef.co/go/tools/cmd/staticcheck@latest`)
  - `golangci-lint`(`brew install golangci-lint` など)
  - `gitleaks`(`brew install gitleaks` など)
  - `pre-commit`(`pipx install pre-commit` など)

外部Go依存は2つだけです。

```text
github.com/go-sql-driver/mysql
nhooyr.io/websocket
```

## 起動

```sh
docker compose -f compose.yml up -d
make run
open http://localhost:8080/
```

## 品質ゲート

手動、`pre-commit`、CIのいずれからも同じ `scripts/quality-gate.sh` を呼びます。

```sh
make gate              # 単体テストのみ
make gate-integration  # 統合テスト込み(MySQLが必要です)
```

このスクリプトは次を順に通します。

1. `gofmt` 差分検知
2. `go vet ./...`
3. `staticcheck ./...`
4. `golangci-lint run ./...`
5. `gitleaks detect`
6. `go test ./... -race -shuffle=on -count=1`
7. `go build ./...`

## 章とブランチ

| 章 | ブランチ | 内容 |
|---|---|---|
| 1 | `ch01-skeleton` | 骨格、Dockerfile、`compose.yml`、pre-commit、CI、品質ゲート |
| 2 | `ch02-domain` | ドメイン型、`internal/store` のMySQL接続 |
| 3 | `ch03-snapshot` | Snapshot APIの最小実装 |
| 4 | `ch04-send` | POST `/channels/:id/messages` と冪等性キー |
| 5 | `ch05-pagination` | カーソルベース履歴取得 |
| 6 | `ch06-hub` | In-process Hub |
| 7 | `ch07-ws` | WebSocketファンアウト |
| 8 | `ch08-toast` | 画面内トースト |
| 9 | `ch09-outbox-note` | Outboxの境界線(触れるだけ) |

各章のコミットには対応する `chXX-...` のタグも打ちます。
