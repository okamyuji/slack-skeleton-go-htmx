//go:build integration

package migrate_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/migrate"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
)

func openMigrateTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcmysql.Run(ctx,
		"mysql:8.4",
		tcmysql.WithDatabase("migrate_test"),
		tcmysql.WithUsername("appuser"),
		tcmysql.WithPassword("apppass"),
	)
	if err != nil {
		t.Fatalf("testcontainers: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate: %v", err)
		}
	})
	dsn, err := container.ConnectionString(ctx, "parseTime=true", "loc=UTC")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return db
}

// TestUpCannotRecoverFromMidFileDDLFailure この最小マイグレータの復旧境界を固定します。
// 1ファイル内でDDLが成功した後に後続文が失敗すると、適用済み記録が書かれないまま
// 部分適用が残ります。MySQLのDDLはロールバックできないため、再実行は同じDDLの
// 重複適用で失敗し続け、手作業での修復なしには前に進めません。
func TestUpCannotRecoverFromMidFileDDLFailure(t *testing.T) {
	db := openMigrateTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dir := t.TempDir()
	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeFile("0001_init.up.sql", "CREATE TABLE demo (id BIGINT PRIMARY KEY);")
	// 1文目のDDLは成功し、2文目が存在しないテーブルへのINSERTで失敗します
	writeFile("0002_bad.up.sql",
		"ALTER TABLE demo ADD COLUMN extra BIGINT;\nINSERT INTO missing_table (id) VALUES (1);")

	first := migrate.Up(ctx, db, dir)
	if first == nil {
		t.Fatal("途中失敗するはずの初回適用が成功しています")
	}

	// 部分適用の証拠として、失敗したファイルのDDLだけが反映されています
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'demo' AND column_name = 'extra'",
	).Scan(&count); err != nil {
		t.Fatalf("column check: %v", err)
	}
	if count != 1 {
		t.Fatalf("ALTERが適用されていません: count=%d", count)
	}

	// 再実行は同じALTERの重複適用で失敗し、自動では復旧できません
	second := migrate.Up(ctx, db, dir)
	if second == nil {
		t.Fatal("再実行が成功しています(復旧可能なら教材の制約記述を見直してください)")
	}
	if !strings.Contains(second.Error(), "Duplicate column") {
		t.Fatalf("重複適用エラーを期待しましたが: %v", second)
	}
}
