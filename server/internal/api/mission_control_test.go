package api_test

import (
	"database/sql"
	"net/http"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMissionControl seeds one owner with a project + intake event + decision,
// then wires GET /api/mission-control and PUT /api/mission-control/pause behind
// a stub identity middleware (X-Test-Uid), mirroring the intake contract tests.
func setupMissionControl(t *testing.T) (*gin.Engine, *sql.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := openMCPPermTestDB(t)
	db.SetMaxOpenConns(1)
	q := store.New(db)

	mustExec(t, db, `INSERT INTO users (id, username, password_hash, role) VALUES (1,'u1','x','member')`)
	mustExec(t, db, `INSERT INTO projects (id, name, owner_type, owner_id) VALUES (1,'p1','user',1)`)
	mustExec(t, db, `INSERT INTO columns (id, project_id, name, position, op_primitive) VALUES (1,1,'todo',0,'none')`)
	mustExec(t, db, `INSERT INTO columns (id, project_id, name, position, op_primitive) VALUES (2,1,'done',1,'complete')`)
	mustExec(t, db, `INSERT INTO issues (id, column_id, title, position) VALUES (1,2,'done task',0)`)
	mustExec(t, db, `INSERT INTO workspaces (id, name, path, status, owner_type, owner_id) VALUES (1,'ws','/tmp/ws','running','user',1)`)
	mustExec(t, db, `INSERT INTO intake_events (id, project_id, source_kind, source_ref, event_ext_id, event_kind, thread_ext_id) VALUES (1,1,'im_bot','chat-1','evt-1','message','thread-1')`)
	mustExec(t, db, `INSERT INTO intake_decisions (id, event_id, project_id, mode_at_decision, strength, decision, executed, issue_id, workspace_id) VALUES (10,1,1,'auto','explicit','accept',1,1,1)`)

	authz := service.NewAuthz(q, db)
	intakeSvc := service.NewIntakeService(db, q)
	missionSvc := service.NewMissionControlService(store.Wrap(db), q, authz, intakeSvc)
	handler := &api.MissionControlHandler{Svc: missionSvc, DB: db}

	r := gin.New()
	withIdentity := func(c *gin.Context) {
		if uid := c.GetHeader("X-Test-Uid"); uid != "" {
			if n, err := strconv.ParseInt(uid, 10, 64); err == nil {
				c.Set("auth_user_id", n)
			}
		}
		c.Next()
	}
	g := r.Group("/api")
	g.Use(withIdentity)
	g.GET("/mission-control", handler.Get)
	g.PUT("/mission-control/pause", handler.SetPause)
	return r, db
}

func TestMissionControl_GetPanels(t *testing.T) {
	r, _ := setupMissionControl(t)

	w := doJSON(t, r, http.MethodGet, "/api/mission-control", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	view := decodeBody[service.MissionControlView](t, w)

	assert.Equal(t, 1, view.Intake.ProjectCount)
	require.NotEmpty(t, view.Intake.Metrics)
	require.NotEmpty(t, view.Completion.Metrics)

	byKey := func(ms []service.MissionControlMetric, key string) service.MissionControlMetric {
		for _, m := range ms {
			if m.Key == key {
				return m
			}
		}
		t.Fatalf("metric %s missing", key)
		return service.MissionControlMetric{}
	}
	today := byKey(view.Intake.Metrics, "today_accepted")
	require.NotNil(t, today.Value)
	assert.Equal(t, float64(1), *today.Value)
	assert.Equal(t, "intake", today.Drill)

	inProgress := byKey(view.Completion.Metrics, "in_progress")
	require.NotNil(t, inProgress.Value)
	assert.Equal(t, float64(1), *inProgress.Value)
	assert.Equal(t, "workspaces", inProgress.Drill)

	// The two panels are separate payloads; no combined score field exists.
	assert.NotNil(t, view.Intake.Metrics)
	assert.NotNil(t, view.Completion.Metrics)
}

func TestMissionControl_PauseAndAuth(t *testing.T) {
	r, db := setupMissionControl(t)

	w := doJSON(t, r, http.MethodGet, "/api/mission-control", "", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	w = doJSON(t, r, http.MethodPut, "/api/mission-control/pause", "1", `{"paused":true}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"paused":true`)
	assert.Contains(t, w.Body.String(), `"projects":1`)

	var paused int
	require.NoError(t, db.QueryRow(`SELECT paused FROM intake_project_state WHERE project_id = 1`).Scan(&paused))
	assert.Equal(t, 1, paused)

	w = doJSON(t, r, http.MethodGet, "/api/mission-control", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	view := decodeBody[service.MissionControlView](t, w)
	assert.True(t, view.Intake.Paused)

	// Missing body is a 400, not a silent toggle.
	w = doJSON(t, r, http.MethodPut, "/api/mission-control/pause", "1", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// A foreign owner filter narrows to zero projects → every tile is no_data.
	w = doJSON(t, r, http.MethodGet, "/api/mission-control?owner=user:2", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	view = decodeBody[service.MissionControlView](t, w)
	assert.Equal(t, 0, view.Intake.ProjectCount)
	for _, m := range view.Intake.Metrics {
		assert.Equal(t, "no_data", m.Status, m.Key)
	}
}
