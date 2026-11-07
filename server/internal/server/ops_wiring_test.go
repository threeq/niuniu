package server

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/opsbot"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

func newOpsWiringServer(t *testing.T) *Server {
	t.Helper()
	store.Driver = "sqlite"
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(store.Schema)
	require.NoError(t, err)

	s := &Server{
		cfg:                 &config.Config{DataDir: t.TempDir()},
		db:                  db,
		queries:             store.NewQueries(db),
		workspaceMessageSvc: service.NewWorkspaceMessageService(store.NewQueries(db), nil),
	}
	s.initOpsBot(nil)
	return s
}

// TestOpsRuntimeWiring is the Epic acceptance check: every runnable Ops Bot
// service is constructed and injected in server.New, not left for a later task.
func TestOpsRuntimeWiring(t *testing.T) {
	s := newOpsWiringServer(t)

	require.NotNil(t, s.opsBotSvc)
	require.NotNil(t, s.opsApproval)
	require.NotNil(t, s.opsBudget)
	// Budget: every hook the service exposes is a real implementation.
	require.NotNil(t, s.opsBudget.Sum)
	require.NotNil(t, s.opsBudget.Breakdown)
	require.NotNil(t, s.opsBudget.Limit)
	require.NotNil(t, s.opsBudget.SaveLimit)
	require.NotNil(t, s.opsBudget.Pause)
	require.NotNil(t, s.opsBudget.Resume)
	require.NotNil(t, s.opsBudget.Notify)
	require.NotNil(t, s.opsRisk)
	require.NotNil(t, s.opsResolver)
	require.NotNil(t, s.opsSessions)
	require.NotNil(t, s.opsWorker)
	require.NotNil(t, s.opsScheduler)
	require.NotNil(t, s.opsPublisher)
	require.NotNil(t, s.opsEngagement)
	require.NotNil(t, s.opsDispatcher)
	require.NotNil(t, s.opsMetrics)
	require.NotNil(t, s.opsCalendar)
	require.NotNil(t, s.opsFeedback)
	require.NotNil(t, s.opsDirector)
	require.NotNil(t, s.opsTrend)

	// Publisher: adapter resolution, manual fallback, problem issues, events,
	// notification, budget pre-check and scheduling are all injected.
	require.NotNil(t, s.opsPublisher.AdapterFor)
	require.NotNil(t, s.opsPublisher.Manual)
	require.NotNil(t, s.opsPublisher.Problems)
	require.NotNil(t, s.opsPublisher.Events)
	require.NotNil(t, s.opsPublisher.Notify)
	require.NotNil(t, s.opsPublisher.CheckBudget)
	require.NotNil(t, s.opsPublisher.BotStatus)
	require.NotNil(t, s.opsPublisher.Schedule)

	// Engagement: store, DB wrapper, adapter resolution and approvals.
	require.NotNil(t, s.opsEngagement.Q)
	require.NotNil(t, s.opsEngagement.DB)
	require.NotNil(t, s.opsEngagement.AdapterFor)
	require.NotNil(t, s.opsEngagement.Approvals)

	// Resolver: browser worker and manual adapter are wired; credentials and
	// IM notification are attached by wireOpsLate.
	require.NotNil(t, s.opsResolver.Worker)
	require.NotNil(t, s.opsResolver.Manual)
	// Risk-signal producers: the browser worker and the publisher feed the
	// account risk controller.
	require.NotNil(t, s.opsWorker.OnRisk)
	require.NotNil(t, s.opsPublisher.OnAdapterFailure)
	require.Nil(t, s.opsResolver.Creds)
	require.Nil(t, s.opsRisk.VerifyLogin)

	require.NotNil(t, s.opsDirector.Bots)
	require.NotNil(t, s.opsDirector.Planner)
	require.NotNil(t, s.opsDirector.Calendar)
	require.NotNil(t, s.opsDirector.Approvals)
	require.NotNil(t, s.opsDirector.Budget)
	require.NotNil(t, s.opsDirector.Feedback)

	s.wireOpsLate()
	require.NotNil(t, s.opsRisk.VerifyLogin)
	require.NotNil(t, s.opsDirector.Messenger)

	// The director board writes through the real blackboard store.
	_, err := s.db.Exec(`INSERT INTO workspaces (id, name, path, owner_type, owner_id) VALUES (20, 'director', '/tmp/d', 'user', 2)`)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO ops_bots
		(id, owner_type, owner_id, role, display_name, control_workspace_id, status)
		VALUES (10, 'user', 2, 'director', '总监', 20, 'active')`)
	require.NoError(t, err)
	s.opsDirector.BotID = 10
	s.opsDirector.Owner = opsbot.OwnerRef{Type: "user", ID: 2}
	require.NoError(t, s.opsDirector.Board.Write(context.Background(), "week-plan", `{"week":"2026-W45"}`))
	var content string
	require.NoError(t, s.db.QueryRow(`SELECT content FROM blackboard_entries WHERE workspace_id = 20 AND entry_key = 'week-plan'`).Scan(&content))
	require.Contains(t, content, "2026-W45")

	// The approved-reply dispatcher starts with the server and stops cleanly.
	require.NoError(t, s.opsDispatcher.Start(context.Background()))
	require.True(t, s.opsDispatcher.Running())
	s.opsDispatcher.Stop()
	require.False(t, s.opsDispatcher.Running())

	// Scheduled publishes are cancellable at shutdown.
	require.NoError(t, s.opsScheduler.Register(context.Background(), time.Now().Add(time.Hour), "k", func(context.Context) {}))
	s.opsScheduler.Stop()
	require.ErrorIs(t,
		s.opsScheduler.Register(context.Background(), time.Now(), "k2", func(context.Context) {}),
		errOpsSchedulerStopped)
}
