// Package migrate migrationsディレクトリ配下の.up.sqlを名前順で適用する
// 最小マイグレータです。外部ライブラリを使わない教材向け実装です。
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Up dir配下の*.up.sqlを名前順に読み込み、dbに対して順次実行します。
// 各ファイルはセミコロン区切りの複数文を含むことができます。
// schema_migrationsテーブルに適用済みファイル名を記録し、二度目以降の実行ではスキップします。
//
// 承知のうえで割り切っている点があります。適用の記録はファイル単位で、
// しかも全文が成功したあとに書きます。1つのファイルの途中で失敗すると、
// そこまでの文はDBに残ったまま記録だけが残りません。次回の実行は同じファイルを
// 先頭からやり直すので、すでに適用済みの文で再び失敗します。
// MySQLのDDLはトランザクションで巻き戻せず、ALTER TABLE ... ADD COLUMN には
// IF NOT EXISTS もないため、この状況からの自動復旧はできません。
// 復旧は人手で行います。失敗したファイルの残りの文だけを手で流し、
// INSERT INTO schema_migrations (filename) VALUES ('<失敗したファイル名>') で
// 適用済みとして記録します。
// 本番運用でこれを避けるなら、1ファイル1文に分けるか、文単位で記録する
// マイグレータへ差し替えることになります。
func Up(ctx context.Context, db *sql.DB, dir string) error {
	if err := ensureTrackingTable(ctx, db); err != nil {
		return fmt.Errorf("migrate: ensure table: %w", err)
	}

	files, err := collectFiles(dir)
	if err != nil {
		return fmt.Errorf("migrate: collect: %w", err)
	}

	applied, err := loadApplied(ctx, db)
	if err != nil {
		return fmt.Errorf("migrate: applied: %w", err)
	}

	for _, name := range files {
		if applied[name] {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path) //nolint:gosec // 教材用の固定パス読み込みです
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		if err := execScript(ctx, db, string(raw)); err != nil {
			return fmt.Errorf("migrate: exec %s: %w", name, err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO schema_migrations (filename) VALUES (?)", name); err != nil {
			return fmt.Errorf("migrate: record %s: %w", name, err)
		}
	}
	return nil
}

func ensureTrackingTable(ctx context.Context, db *sql.DB) error {
	const ddl = `CREATE TABLE IF NOT EXISTS schema_migrations (
		filename VARCHAR(255) PRIMARY KEY,
		applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`
	_, err := db.ExecContext(ctx, ddl)
	return err
}

func collectFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".up.sql") {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	return files, nil
}

func loadApplied(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT filename FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	applied := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return applied, nil
}

// execScript セミコロン区切りで分割した複数文を1つずつ実行します。
// 文字列リテラル中のセミコロンを厳密に解釈する必要はない教材スコープです。
func execScript(ctx context.Context, db *sql.DB, script string) error {
	for _, stmt := range splitStatements(script) {
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("stmt %q: %w", truncateForError(stmt, 80), err)
		}
	}
	return nil
}

func splitStatements(script string) []string {
	parts := strings.Split(script, ";")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(stripComments(p))
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func stripComments(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// truncateForError エラー表示用のSQL文をバイト単位で最大nバイトまで切ります。
func truncateForError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
