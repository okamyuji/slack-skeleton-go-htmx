//go:build integration

package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/okamyuji/slack-skeleton-go-htmx/internal/migrate"
	"github.com/okamyuji/slack-skeleton-go-htmx/internal/store"
)

// startMySQL testcontainers-goでMySQLコンテナを起動し、DSNとクリーンアップを返します。
// 外部DBに依存せず、テストプロセスの中で完結します。
func startMySQL(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcmysql.Run(ctx,
		"mysql:8.4",
		tcmysql.WithDatabase("slack_skeleton"),
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
	return dsn
}

func openTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dsn := startMySQL(t)

	db, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := migrate.Up(ctx, db, "../../migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	resetFixtures(db)
	cleanup := func() {
		resetFixtures(db)
		_ = db.Close()
	}
	return db, cleanup
}

func resetFixtures(db *sql.DB) {
	_, _ = db.Exec("DELETE FROM messages")
	_, _ = db.Exec("DELETE FROM webhooks")
	_, _ = db.Exec("DELETE FROM memberships")
	_, _ = db.Exec("DELETE FROM channels")
	_, _ = db.Exec("DELETE FROM users")
	_, _ = db.Exec("DELETE FROM workspaces")
}
