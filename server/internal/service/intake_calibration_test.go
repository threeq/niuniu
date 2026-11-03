package service

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

// seedCalibrationDecision inserts one intake event + decision. wouldDo is the
// literal would_do_json value; a non-empty object marks a dry-run candidate.
func seedCalibrationDecision(t *testing.T, db *sql.DB, seq, projectID int64, sourceKind, mode, decision string, executed int, wouldDo string) {
	t.Helper()
	eventID := seq
	if _, err := db.Exec(
		`INSERT INTO intake_events (id, project_id, source_kind, source_ref, event_ext_id, event_kind, actor_ext_id, actor_name)
		 VALUES (?, ?, ?, 'chat-1', ?, 'message', 'ou_1', 'Alice')`,
		eventID, projectID, sourceKind, fmt.Sprintf("evt-%d", seq),
	); err != nil {
		t.Fatalf("seed event %d: %v", seq, err)
	}
	if _, err := db.Exec(
		`INSERT INTO intake_decisions (id, event_id, project_id, mode_at_decision, strength, decision, executed, would_do_json)
		 VALUES (?, ?, ?, ?, 'inferred', ?, ?, ?)`,
		seq, eventID, projectID, mode, decision, executed, wouldDo,
	); err != nil {
		t.Fatalf("seed decision %d: %v", seq, err)
	}
}

func TestIntakeCalibration_CountsResolvedDryRunsOnly(t *testing.T) {
	svc, f := intakePolicyFixture(t)
	ctx := context.Background()
	brief := `{"decision":"accept","brief":{"Goal":"ship it"}}`

	// 12 dry-run candidates: 9 accepted, 2 ignored, 1 still pending.
	for i := int64(1); i <= 9; i++ {
		seedCalibrationDecision(t, f.db, i, f.projectID, intake.SourceKindIMBot, "observe", "accept", 1, brief)
	}
	seedCalibrationDecision(t, f.db, 10, f.projectID, intake.SourceKindIMBot, "observe", "ignore", 0, brief)
	seedCalibrationDecision(t, f.db, 11, f.projectID, intake.SourceKindIMBot, "observe", "ignore", 0, brief)
	seedCalibrationDecision(t, f.db, 12, f.projectID, intake.SourceKindIMBot, "observe", "resume", 0, brief)

	// Noise that must not count: an explicit request that executed while
	// observing (no brief), an auto-mode execution, and a denied inferred event.
	seedCalibrationDecision(t, f.db, 13, f.projectID, intake.SourceKindIMBot, "observe", "accept", 1, "{}")
	seedCalibrationDecision(t, f.db, 14, f.projectID, "github", "auto", "accept", 1, "{}")
	seedCalibrationDecision(t, f.db, 15, f.projectID, "github", "off", "ignore", 0, "{}")

	stats, err := svc.GetCalibration(ctx, f.projectID)
	if err != nil {
		t.Fatalf("get calibration: %v", err)
	}
	if stats.Calibrated != 11 || stats.Accepted != 9 || stats.Ignored != 2 || stats.Pending != 1 {
		t.Fatalf("stats = %+v, want calibrated=11 accepted=9 ignored=2 pending=1", stats)
	}
	if stats.AcceptanceRate < 0.81 || stats.AcceptanceRate > 0.82 {
		t.Fatalf("acceptance rate = %v, want ~0.818", stats.AcceptanceRate)
	}
	if !stats.Ready {
		t.Fatalf("11 calibrated at 81.8%% must meet the >=10 / >=80%% threshold: %+v", stats)
	}
	if stats.MinCalibrated != IntakeCalibrationMinDecisions || stats.MinAcceptanceRate != IntakeCalibrationMinRate {
		t.Fatalf("threshold echoed to the client is wrong: %+v", stats)
	}
	if len(stats.Sources) != 1 || stats.Sources[0].SourceKind != intake.SourceKindIMBot {
		t.Fatalf("sources = %+v, want only im_bot", stats.Sources)
	}
	if len(stats.Recent) != 10 {
		t.Fatalf("recent records = %d, want the newest 10", len(stats.Recent))
	}
	for _, rec := range stats.Recent {
		if rec.Outcome != "accepted" && rec.Outcome != "ignored" {
			t.Fatalf("unexpected recent outcome: %+v", rec)
		}
	}
}

func TestIntakeCalibration_ThresholdGatesTheRecommendation(t *testing.T) {
	svc, f := intakePolicyFixture(t)
	ctx := context.Background()
	brief := `{"decision":"accept","brief":{"Goal":"ship it"}}`

	// Only 9 calibrated: a perfect rate is still not enough.
	for i := int64(1); i <= 9; i++ {
		seedCalibrationDecision(t, f.db, i, f.projectID, intake.SourceKindIMBot, "observe", "accept", 1, brief)
	}
	stats, err := svc.GetCalibration(ctx, f.projectID)
	if err != nil {
		t.Fatalf("get calibration: %v", err)
	}
	if stats.Calibrated != 9 || stats.Ready {
		t.Fatalf("9 calibrated decisions must not be ready: %+v", stats)
	}

	// 10 calibrated but 70% accepted: sample large enough, quality is not.
	seedCalibrationDecision(t, f.db, 10, f.projectID, intake.SourceKindIMBot, "observe", "accept", 1, brief)
	for i := int64(11); i <= 13; i++ {
		seedCalibrationDecision(t, f.db, i, f.projectID, intake.SourceKindIMBot, "observe", "ignore", 0, brief)
	}
	stats, err = svc.GetCalibration(ctx, f.projectID)
	if err != nil {
		t.Fatalf("get calibration: %v", err)
	}
	if stats.Calibrated != 13 || stats.Ready {
		t.Fatalf("70%% acceptance must not be ready: %+v", stats)
	}
}

func TestIntakeCalibration_ResumeKeepsModesAndSample(t *testing.T) {
	svc, f := intakePolicyFixture(t)
	ctx := context.Background()
	brief := `{"decision":"accept","brief":{"Goal":"ship it"}}`
	for i := int64(1); i <= 10; i++ {
		seedCalibrationDecision(t, f.db, i, f.projectID, intake.SourceKindIMBot, "observe", "accept", 1, brief)
	}
	if _, err := svc.SetMode(ctx, f.projectID, intake.SourceKindIMBot, "auto"); err != nil {
		t.Fatalf("set auto: %v", err)
	}

	if _, err := svc.SetPaused(ctx, f.projectID, true); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := svc.SetPaused(ctx, f.projectID, false); err != nil {
		t.Fatalf("resume: %v", err)
	}

	view, err := svc.GetSettings(ctx, f.projectID)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if view.Paused {
		t.Fatalf("still paused after resume")
	}
	var mode string
	for _, s := range view.Settings {
		if s.SourceKind == intake.SourceKindIMBot {
			mode = s.Mode
		}
	}
	if mode != "auto" {
		t.Fatalf("resume changed the stored mode to %q, want auto", mode)
	}

	stats, err := svc.GetCalibration(ctx, f.projectID)
	if err != nil {
		t.Fatalf("get calibration: %v", err)
	}
	if stats.Calibrated != 10 || !stats.Ready {
		t.Fatalf("pausing/resuming must not reset the calibration sample: %+v", stats)
	}
}
