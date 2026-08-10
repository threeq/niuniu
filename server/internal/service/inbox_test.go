package service_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

func TestInbox_SendAndRead(t *testing.T) {
	q := niutest.SetupDB(t)
	bus := event.NewBus()
	rootDir := t.TempDir()
	svc := service.NewInboxService(rootDir, q, bus)

	ctx := context.Background()
	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{Name: "t", Path: "/tmp/t", OwnerType: "user"})
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.Send(ctx, ws.ID, "alice", "bob", "hello bob"); err != nil {
		t.Fatal(err)
	}

	filePath := filepath.Join(rootDir, strconv.FormatInt(ws.ID, 10), "bob.json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	var msgs []service.InboxMessage
	if err := json.Unmarshal(data, &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].From != "alice" || msgs[0].Text != "hello bob" {
		t.Errorf("unexpected file contents: %+v", msgs)
	}
	if msgs[0].Read {
		t.Error("message should be unread initially")
	}

	// Read returns unread + marks read.
	unread, err := svc.Read(ctx, ws.ID, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 1 {
		t.Errorf("expected 1 unread, got %d", len(unread))
	}
	// Second read returns 0.
	unread2, err := svc.Read(ctx, ws.ID, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(unread2) != 0 {
		t.Errorf("expected 0 unread on second read, got %d", len(unread2))
	}
}

func TestInbox_RejectsImpostorPath(t *testing.T) {
	q := niutest.SetupDB(t)
	svc := service.NewInboxService(t.TempDir(), q, event.NewBus())
	ctx := context.Background()
	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{Name: "t", Path: "/tmp/t", OwnerType: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Send(ctx, ws.ID, "alice", "../etc", "owned"); err == nil {
		t.Error("expected path-traversal rejection")
	}
	if err := svc.Send(ctx, ws.ID, "", "bob", "hi"); err == nil {
		t.Error("expected empty-from rejection")
	}
}

func TestInbox_PublishesEvent(t *testing.T) {
	q := niutest.SetupDB(t)
	bus := event.NewBus()
	svc := service.NewInboxService(t.TempDir(), q, bus)
	ctx := context.Background()
	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{Name: "t", Path: "/tmp/t", OwnerType: "user"})
	if err != nil {
		t.Fatal(err)
	}

	// event.Bus.Subscribe()/Unsubscribe(ch) — no per-workspace filter; the
	// WorkspaceId field on the event is how consumers filter.
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	// Track goroutine completion so the file handles held by svc.Send are
	// fully released before t.TempDir() cleanup runs. Without this, Windows
	// fails the cleanup with "directory is not empty" when the file write
	// outlives the event publish.
	done := make(chan struct{})
	go func() {
		_ = svc.Send(ctx, ws.ID, "alice", "bob", "hi")
		close(done)
	}()
	select {
	case ev := <-ch:
		if ev.Type != event.EventTeamInboxSent {
			t.Errorf("got event type %q, want team:inbox_sent", ev.Type)
		}
		if ev.WorkspaceId != ws.ID {
			t.Errorf("got WorkspaceId=%d, want %d", ev.WorkspaceId, ws.ID)
		}
		if ev.InboxSent == nil {
			t.Fatal("expected InboxSent payload to be populated")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for team:inbox_sent event")
	}
	// Wait for Send to fully return (releases the file handle) before the
	// test exits and TempDir cleanup runs.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("svc.Send goroutine did not return")
	}
}
