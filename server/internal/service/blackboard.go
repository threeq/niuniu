package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type BlackboardEntry struct {
	Key           string    `json:"key"`
	EntryType     string    `json:"entry_type"`
	ProducerAgent string    `json:"producer_agent"`
	Content       string    `json:"content"`
	RefPath       string    `json:"ref_path,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type Blackboard struct {
	workspaceID int64
	mu          sync.RWMutex
	entries     map[string]BlackboardEntry
	q           *store.Queries
	bus         *event.Bus
}

func NewBlackboard(workspaceID int64, q *store.Queries, bus *event.Bus) *Blackboard {
	return &Blackboard{
		workspaceID: workspaceID,
		entries:     make(map[string]BlackboardEntry),
		q:           q,
		bus:         bus,
	}
}

func (bb *Blackboard) Write(key, entryType, producer, content, refPath string) {
	entry := BlackboardEntry{
		Key:           key,
		EntryType:     entryType,
		ProducerAgent: producer,
		Content:       content,
		RefPath:       refPath,
		CreatedAt:     time.Now(),
	}

	bb.mu.Lock()
	bb.entries[key] = entry
	bb.mu.Unlock()

	if bb.q != nil {
		bb.q.UpsertBlackboardEntry(context.Background(), store.UpsertBlackboardEntryParams{
			WorkspaceID:   bb.workspaceID,
			ProducerAgent: producer,
			EntryType:     entryType,
			EntryKey:      key,
			Content:       content,
			Metadata:      "{}",
			RefPath: sql.NullString{
				String: refPath,
				Valid:  refPath != "",
			},
		})
	}

	if bb.bus != nil {
		payload, _ := json.Marshal(entry)
		bb.bus.Publish(event.OutputEvent{
			Type:        event.EventTeamBlackboardUpdated,
			Content:     string(payload),
			WorkspaceId: bb.workspaceID,
			Ts:          time.Now().UnixMilli(),
		})
	}
}

func (bb *Blackboard) Read(key string) (BlackboardEntry, bool) {
	bb.mu.RLock()
	defer bb.mu.RUnlock()
	e, ok := bb.entries[key]
	return e, ok
}

func (bb *Blackboard) List(typeFilter string) []BlackboardEntry {
	bb.mu.RLock()
	defer bb.mu.RUnlock()
	var result []BlackboardEntry
	for _, e := range bb.entries {
		if typeFilter == "" || e.EntryType == typeFilter {
			result = append(result, e)
		}
	}
	return result
}

func (bb *Blackboard) Delete(key string) {
	bb.mu.Lock()
	delete(bb.entries, key)
	bb.mu.Unlock()

	if bb.q != nil {
		bb.q.DeleteBlackboardEntry(context.Background(), store.DeleteBlackboardEntryParams{
			WorkspaceID: bb.workspaceID,
			EntryKey:    key,
		})
	}
}

func (bb *Blackboard) Clear() {
	bb.mu.Lock()
	bb.entries = make(map[string]BlackboardEntry)
	bb.mu.Unlock()

	if bb.q != nil {
		bb.q.ClearBlackboardForWorkspace(context.Background(), bb.workspaceID)
	}
}

func (bb *Blackboard) LoadFromDB(ctx context.Context) error {
	if bb.q == nil {
		return nil
	}
	rows, err := bb.q.ListBlackboardEntries(ctx, bb.workspaceID)
	if err != nil {
		return err
	}
	bb.mu.Lock()
	defer bb.mu.Unlock()
	for _, row := range rows {
		bb.entries[row.EntryKey] = BlackboardEntry{
			Key:           row.EntryKey,
			EntryType:     row.EntryType,
			ProducerAgent: row.ProducerAgent,
			Content:       row.Content,
			RefPath:       row.RefPath.String,
			CreatedAt:     row.CreatedAt,
		}
	}
	return nil
}
