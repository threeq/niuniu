package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAskBroker is a test double for the irreversible-op confirmation broker.
type fakeAskBroker struct {
	result service.AskUserResult
	err    error
	calls  int
}

func (f *fakeAskBroker) Request(_ context.Context, _ int64, _ string, _ int64,
	_ string, _ []event.AskUserQuestion) (service.AskUserResult, error) {
	f.calls++
	return f.result, f.err
}

// gateRouter wires the gate behind a middleware that simulates MCPTokenAuth setting
// the workspace id, then a sentinel handler whose run is recorded via *ran.
func gateRouter(broker api.IrreversibleAskBroker, wsID int64, ran *bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if wsID != 0 {
			c.Set("mcp_workspace_id", wsID)
		}
		c.Next()
	})
	r.DELETE("/mcp/issues/:id",
		api.IrreversibleOpGate(broker, nil, "delete_issue"),
		func(c *gin.Context) { *ran = true; c.JSON(http.StatusOK, gin.H{"ok": true}) })
	return r
}

func confirmAnswer(label string) service.AskUserResult {
	return service.AskUserResult{Answered: true, Answers: []service.AskUserAnswer{{Labels: []string{label}}}}
}

func TestIrreversibleGate_Confirmed_RunsHandler(t *testing.T) {
	broker := &fakeAskBroker{result: confirmAnswer("确认执行")}
	var ran bool
	r := gateRouter(broker, 7, &ran)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/mcp/issues/3", nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, ran, "handler runs after the user confirms")
	assert.Equal(t, 1, broker.calls, "user was asked once")
}

func TestIrreversibleGate_Declined_Blocks(t *testing.T) {
	broker := &fakeAskBroker{result: confirmAnswer("取消")}
	var ran bool
	r := gateRouter(broker, 7, &ran)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/mcp/issues/3", nil))

	require.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, ran, "destructive handler must NOT run when declined")
}

func TestIrreversibleGate_Timeout_Blocks(t *testing.T) {
	broker := &fakeAskBroker{result: service.AskUserResult{Answered: false, Reason: "timeout"}}
	var ran bool
	r := gateRouter(broker, 7, &ran)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/mcp/issues/3", nil))

	require.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, ran)
}

func TestIrreversibleGate_BrokerError_Blocks(t *testing.T) {
	broker := &fakeAskBroker{err: errors.New("boom")}
	var ran bool
	r := gateRouter(broker, 7, &ran)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/mcp/issues/3", nil))

	require.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, ran)
}

func TestIrreversibleGate_NoWorkspaceContext_BlocksWithoutAsking(t *testing.T) {
	broker := &fakeAskBroker{result: confirmAnswer("确认执行")}
	var ran bool
	r := gateRouter(broker, 0, &ran) // no mcp_workspace_id set

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/mcp/issues/3", nil))

	require.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, ran)
	assert.Equal(t, 0, broker.calls, "no workspace -> fail closed without even asking")
}

func TestIrreversibleGate_NilBroker_Blocks(t *testing.T) {
	var ran bool
	r := gateRouter(nil, 7, &ran)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/mcp/issues/3", nil))

	require.Equal(t, http.StatusForbidden, w.Code)
	assert.False(t, ran)
}
