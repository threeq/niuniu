package server

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// runSQLiteMaintenance runs periodic SQLite-only maintenance tasks.
// Delays 10 minutes after startup to avoid contention, then runs every 24h.
// Blocks until ctx is cancelled; must be called as a goroutine.
func runSQLiteMaintenance(ctx context.Context, rawDB *sql.DB, q *store.Queries, retentionDays int) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Minute):
	}

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	runSQLiteMaintenanceOnce(ctx, rawDB, q, retentionDays)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runSQLiteMaintenanceOnce(ctx, rawDB, q, retentionDays)
		}
	}
}

func runSQLiteMaintenanceOnce(ctx context.Context, rawDB *sql.DB, q *store.Queries, retentionDays int) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("sqlite maintenance panicked", "recover", r)
		}
	}()

	// Merge WAL into main file and truncate to near-zero.
	// Passive checkpoint: does not block readers or writers.
	if _, err := rawDB.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		slog.Warn("sqlite maintenance: wal_checkpoint failed", "err", err)
	}

	// Update query planner statistics (safe, lock-free on SQLite 3.38+).
	if _, err := rawDB.ExecContext(ctx, "PRAGMA optimize"); err != nil {
		slog.Warn("sqlite maintenance: optimize failed", "err", err)
	}

	// Prune agent_messages for archived workspaces older than retentionDays.
	if retentionDays > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
		if err := q.PruneAgentMessages(ctx, cutoff); err != nil {
			slog.Warn("sqlite maintenance: prune agent_messages failed", "err", err)
		}
	}

	// VACUUM when freelist exceeds 20% of pages.
	// Uses a dedicated connection with extended busy_timeout so the server
	// does NOT need to stop: VACUUM waits for in-flight transactions to finish,
	// then acquires exclusive lock, compacts the file, and releases.
	// For a small database (<50MB) this completes in under a second.
	vacuumIfNeeded(ctx, rawDB)
}

// vacuumIfNeeded runs VACUUM when the SQLite freelist ratio exceeds 20%.
// It uses a dedicated connection with a 30-second busy_timeout so it can be
// called while the server is running — no service restart required.
func vacuumIfNeeded(ctx context.Context, rawDB *sql.DB) {
	var pageCount, freelistCount int64
	if err := rawDB.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return
	}
	if err := rawDB.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&freelistCount); err != nil {
		return
	}
	if pageCount == 0 || float64(freelistCount)/float64(pageCount) < 0.20 {
		return
	}

	ratio := float64(freelistCount) / float64(pageCount)
	slog.Info("sqlite maintenance: VACUUM triggered", "freelist_ratio", ratio, "freelist_pages", freelistCount)

	// Dedicated connection: isolates the VACUUM from the pool so pool connections
	// can still serve requests while VACUUM is waiting for exclusive lock.
	conn, err := rawDB.Conn(ctx)
	if err != nil {
		slog.Warn("sqlite maintenance: VACUUM conn failed", "err", err)
		return
	}
	defer conn.Close()

	// 30s to acquire exclusive lock: in practice, with WAL + short-lived
	// transactions, this resolves in milliseconds.
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout(30000)"); err != nil {
		slog.Warn("sqlite maintenance: set busy_timeout failed", "err", err)
		return
	}
	if _, err := conn.ExecContext(ctx, "VACUUM"); err != nil {
		slog.Warn("sqlite maintenance: VACUUM failed", "err", err)
		return
	}
	slog.Info("sqlite maintenance: VACUUM completed")
}
