#!/usr/bin/env bash
# 品質ゲート実行スクリプト
# 手動実行とpre-commit、CIから共通で呼び出します
set -euo pipefail

cd "$(dirname "$0")/.."

run_step() {
    local label="$1"
    shift
    printf '\n==> %s\n' "$label"
    "$@"
}

run_step "go fmt 検査" bash -c '
    diff=$(gofmt -l .)
    if [ -n "$diff" ]; then
        echo "go fmtが未適用です:"
        echo "$diff"
        exit 1
    fi
'

run_step "go vet" go vet ./...

run_step "staticcheck" staticcheck ./...

run_step "golangci-lint" golangci-lint run ./...

run_step "gitleaks (リポジトリ走査)" gitleaks detect --redact --verbose --config .gitleaks.toml --no-banner

run_step "govulncheck (脆弱性検査)" govulncheck ./...

INTEGRATION_FLAG=""
if [ "${WITH_INTEGRATION:-0}" = "1" ]; then
    INTEGRATION_FLAG="-tags=integration"
    echo "統合テストを有効化します (WITH_INTEGRATION=1)"
fi

run_step "go test (shuffle + no cache)" go test ./... \
    -race \
    -shuffle=on \
    -count=1 \
    ${INTEGRATION_FLAG}

run_step "go build" go build ./...

printf '\n品質ゲートをすべて通過しました\n'
