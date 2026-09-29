package main

// Async video task records — the handle contract for video_generate.
//
// CONVENTION (frozen here, comments in tools.go point back to this file):
//
//	<ws>/video-project/shots/<task_group>.task.json   one task group = N provider tasks
//	<ws>/video-project/shots/<task_group>-<k>.mp4     fetched candidate k (1-based)
//
// A task group exists because one storyboard shot usually wants 2–3 candidates
// while every provider task yields exactly one video: the tool submits N provider
// tasks under ONE group id, so the caller polls a single handle. The file is the
// authoritative state — poll only ever advances what is written here, so a
// process restart mid-flight loses nothing (the provider keeps working; the next
// poll picks the tasks up from this record).
//
// Failure discipline (design §7.4): a failed provider task is recorded with the
// provider's error text verbatim and is NEVER auto-retried; re-running a failed
// shot is a human decision made against the quote delta.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// videoTaskEntry is one provider task inside a group.
type videoTaskEntry struct {
	Index         int             `json:"index"`
	TaskID        string          `json:"task_id,omitempty"`
	State         string          `json:"state"` // pending | running | succeeded | failed
	RawStatus     string          `json:"raw_status,omitempty"`
	Error         string          `json:"error,omitempty"`           // 供应商错误原文，或本地取片判定
	SubmitError   string          `json:"submit_error,omitempty"`    // failure of the submit call itself
	LastPollError string          `json:"last_poll_error,omitempty"` // transient poll failure (task not failed)
	SubmitRaw     json.RawMessage `json:"submit_raw,omitempty"`      // provider submit response, verbatim
	Candidates    []string        `json:"candidates,omitempty"`      // ws-relative fetched files
}

// videoTaskRecord is the persisted task group.
type videoTaskRecord struct {
	GroupID     string           `json:"group_id"`
	Backend     string           `json:"backend"`
	Model       string           `json:"model,omitempty"`
	QuoteID     string           `json:"quote_id,omitempty"`
	CallID      string           `json:"call_id,omitempty"`
	Label       string           `json:"label,omitempty"`
	CreatedAt   string           `json:"created_at"`
	UpdatedAt   string           `json:"updated_at"`
	Prompt      string           `json:"prompt"`
	DurationSec float64          `json:"duration_sec"`
	AspectRatio string           `json:"aspect_ratio,omitempty"`
	SourceImage string           `json:"source_image"`
	Requested   int              `json:"requested"`
	Status      string           `json:"status"` // submitted | running | succeeded | partial | failed
	Candidates  []string         `json:"candidates"`
	Tasks       []videoTaskEntry `json:"tasks"`
	Note        string           `json:"note,omitempty"`
	// Settled marks the ledger call as finalized, so repeated polling never
	// rewrites the same trace.
	Settled bool `json:"settled,omitempty"`
}

// allTerminal reports whether no task needs further polling.
func (r *videoTaskRecord) allTerminal() bool {
	for _, t := range r.Tasks {
		if t.TaskID == "" {
			continue // submit failed — nothing to poll
		}
		if !TaskState(t.State).Terminal() {
			return false
		}
	}
	return len(r.Tasks) > 0
}

// aggregateStatus folds task states into the group status.
func (r *videoTaskRecord) aggregateStatus() string {
	if len(r.Tasks) == 0 {
		return "failed"
	}
	submitted := 0
	for _, t := range r.Tasks {
		if t.TaskID != "" {
			submitted++
		}
	}
	if submitted == 0 {
		return "failed"
	}
	if !r.allTerminal() {
		return "running"
	}
	if len(r.Candidates) == 0 {
		return "failed"
	}
	for _, t := range r.Tasks {
		if t.State == string(TaskFailed) || t.SubmitError != "" {
			return "partial" // some candidates landed, some did not
		}
	}
	return "succeeded"
}

// taskRecordPath returns shots/<group_id>.task.json.
func taskRecordPath(wsDir, groupID string) string {
	return projectPath(wsDir, "shots", sanitizeShotID(groupID)+".task.json")
}

// saveTaskRecord persists the record atomically (tmp + rename) so a concurrent
// poll can never read a half-written file.
func saveTaskRecord(wsDir string, rec *videoTaskRecord) error {
	if err := ensureDirs(wsDir); err != nil {
		return err
	}
	rec.UpdatedAt = time.Now().Format(time.RFC3339)
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化任务记录失败: %w", err)
	}
	path := taskRecordPath(wsDir, rec.GroupID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("写入任务记录 %s 失败: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("提交任务记录 %s 失败: %w", path, err)
	}
	return nil
}

// loadTaskRecord reads a task group by id.
func loadTaskRecord(wsDir, groupID string) (*videoTaskRecord, error) {
	path := taskRecordPath(wsDir, groupID)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("未找到任务组记录 %s：请确认 task_group=%q 是 video_generate 提交时返回的值",
				relToWS(wsDir, path), groupID)
		}
		return nil, fmt.Errorf("读取任务记录 %s 失败: %w", path, err)
	}
	var rec videoTaskRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("解析任务记录 %s 失败: %w", relToWS(wsDir, path), err)
	}
	if strings.TrimSpace(rec.GroupID) == "" {
		rec.GroupID = groupID
	}
	return &rec, nil
}

// loadTaskRecordInto refreshes rec from disk (used after a poll round so the
// result the tool renders is exactly what the next poll will see).
func loadTaskRecordInto(wsDir string, rec *videoTaskRecord) error {
	fresh, err := loadTaskRecord(wsDir, rec.GroupID)
	if err != nil {
		return err
	}
	*rec = *fresh
	return nil
}
