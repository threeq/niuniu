package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/discovery"
	"github.com/niuniu-dev/niuniu/internal/logging"
	"github.com/niuniu-dev/niuniu/internal/migration"
	"github.com/niuniu-dev/niuniu/internal/server"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/niuniu-dev/niuniu/web"
)

// @title           Niuniu API
// @version         1.0.0
// @description     Niuniu is a full-stack application for managing multi-project, multi-task parallel development with Claude Code integration.
// @termsOfService  http://swagger.io/terms/

// @contact.name   Niuniu
// @contact.url    https://github.com/niuniu-dev/niuniu

// @license.name  MIT
// @license.url   https://opensource.org/licenses/MIT

// @host      localhost:3000
// @BasePath  /api

func main() {
	// Handle admin commands before server startup
	if len(os.Args) >= 2 && os.Args[1] == "admin" {
		handleAdminCommand()
		return
	}

	flags, _, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "flag parse error: %v\n", err)
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	applyEmbeddedOverrides(cfg, flags)

	if err := logging.Setup(logging.LogConfig{
		Output:        cfg.Log.Output,
		Level:         cfg.Log.Level,
		FileDir:       cfg.Log.FileDir,
		RetentionDays: cfg.Log.RetentionDays,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "failed to setup logging: %v\n", err)
		os.Exit(1)
	}

	db, err := store.Open(cfg)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Run migrations before server starts
	q := store.NewQueries(db)
	if err := migration.MigrateWTToWorktrees(context.Background(), q); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}

	// Seed configured auth.users into the DB BEFORE MigrateOwnerModel runs.
	// The owner-model migration calls ensureDefaultOrg/ensureSeedUser, which
	// require at least one user row to exist (the org's first member, or the
	// upgrade-target user). On a truly fresh DB with auth.enabled=true and
	// nothing migrated yet, the migration would otherwise fail with
	// "cannot create default org: no users in DB" before server.New runs.
	if len(cfg.Auth.Users) > 0 {
		authSvc := service.NewAuthService(q, db, config.GetAuthSecret(), cfg.Auth.TokenExpiry, cfg.Auth.RefreshExpiry)
		type seedUser = struct{ Username, Password, DisplayName, Role string }
		seeds := make([]seedUser, len(cfg.Auth.Users))
		for i, u := range cfg.Auth.Users {
			seeds[i] = seedUser{u.Username, u.Password, u.DisplayName, u.Role}
		}
		if err := authSvc.SeedUsers(context.Background(), seeds); err != nil {
			slog.Error("seed auth users (pre-migration) failed", "error", err)
			os.Exit(1)
		}
	}

	if err := migration.MigrateOwnerModel(context.Background(), db, cfg, cfg.DataDir); err != nil {
		slog.Error("owner-model migration failed", "error", err)
		os.Exit(1)
	}

	if err := migration.MigrateDropLegacyPhase7(context.Background(), db); err != nil {
		slog.Error("phase7 drop-legacy migration failed", "error", err)
		os.Exit(1)
	}

	if err := migration.MigrateDropWorkspacesHarnessID(context.Background(), db); err != nil {
		slog.Error("drop-workspaces-harness-id migration failed", "error", err)
		os.Exit(1)
	}

	if err := migration.MigrateDropWorkflowTables(context.Background(), db); err != nil {
		slog.Error("drop-workflow-tables migration failed", "error", err)
		os.Exit(1)
	}

	if err := migration.MigrateCleanupTeamCLAUDEMD(context.Background(), db, cfg.DataDir); err != nil {
		slog.Error("cleanup team CLAUDE.md migration failed", "error", err)
		os.Exit(1)
	}

	if err := migration.MigrateDropHarnessSpecOwner(context.Background(), db); err != nil {
		slog.Error("drop harness_spec owner/scope migration failed", "error", err)
		os.Exit(1)
	}

	frontendFS, err := web.DistFS()
	if err != nil {
		slog.Warn("no embedded frontend found, running in dev mode", "error", err)
		frontendFS = nil
	}

	// Security: never expose an unauthenticated (personal-edition) server on a
	// non-loopback interface — that would let anyone on the network reach the
	// full API and the workspace PTY (a remote shell). Force the bind back to
	// 127.0.0.1; NIUNIU_ALLOW_INSECURE_BIND=1 opts out on a trusted network.
	if original, overridden := config.EnforceLoopbackWhenUnauthenticated(cfg); overridden {
		slog.Warn("auth is disabled; forcing bind to loopback instead of exposing the server on the network",
			"configured_host", original, "bind_host", cfg.Server.Host,
			"opt_out", "set NIUNIU_ALLOW_INSECURE_BIND=1 (and enable auth) to bind a non-loopback address")
	}

	srv := server.New(cfg, db, frontendFS)

	// Start mDNS broadcast for LAN discovery (skipped in embedded mode)
	if !flags.Embedded {
		mdnsBroadcaster, err := discovery.NewMDNSBroadcaster(
			cfg.Server.Host, cfg.Server.Port, api.Version,
		)
		if err != nil {
			slog.Warn("mDNS broadcast failed to start", "error", err)
		} else {
			defer mdnsBroadcaster.Shutdown()
		}
	}

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	var ln net.Listener
	if flags.Embedded {
		// Reuse the last successfully bound port when an ephemeral one is
		// requested, so the personal-edition URL stays stable across restarts.
		ln, err = listenEmbedded(srv.Listen, cfg.Server.Host, cfg.Server.Port, preferredPortPath(cfg.DataDir))
	} else {
		ln, err = srv.Listen(addr)
	}
	if err != nil {
		slog.Error("failed to bind listener", "addr", addr, "error", err)
		os.Exit(1)
	}
	actualAddr := ln.Addr().String()
	slog.Info("starting niuniu", "addr", "http://"+actualAddr)

	if flags.Embedded {
		savePreferredPort(preferredPortPath(cfg.DataDir), actualAddr)
		if err := emitReady(os.Stdout, actualAddr); err != nil {
			slog.Error("emit ready failed", "error", err)
			os.Exit(1)
		}
	}

	// Write lock file so other niuniu processes can detect us and avoid
	// double-opening the same SQLite DB. Removed on clean shutdown below.
	lockPath := filepath.Join(cfg.DataDir, "server.lock")
	if err := writeLockfile(lockPath, LockfileInfo{
		PID:     os.Getpid(),
		Addr:    actualAddr,
		Version: api.Version,
	}); err != nil {
		slog.Warn("failed to write lockfile", "path", lockPath, "error", err)
	} else {
		defer func() {
			if err := removeLockfile(lockPath); err != nil {
				slog.Warn("failed to remove lockfile", "path", lockPath, "error", err)
			}
		}()
	}

	// Run server in background on the pre-bound listener
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	// Start the relay tunnel if credentials exist in the OS keychain.  This
	// has to happen AFTER Listen so we know the local addr the tunnel should
	// proxy inbound mobile requests to.  Cancelled by srv.Shutdown() below.
	relayCtx, cancelRelay := context.WithCancel(context.Background())
	defer cancelRelay()
	srv.StartRelay(relayCtx, "http://"+actualAddr)

	// Wait for signal or server error
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	parentGone := make(chan struct{})
	if flags.Embedded {
		watchParentPipe(os.Stdin, func() { close(parentGone) })
	}

	select {
	case <-sigCh:
		slog.Info("shutting down on signal")
	case <-parentGone:
		slog.Info("shutting down: parent pipe closed")
	case err := <-errCh:
		if err != nil {
			slog.Error("server error", "error", err)
		}
	}

	srv.Shutdown()
	// deferred cleanup (db.Close, mdnsBroadcaster.Shutdown) runs on return
}
