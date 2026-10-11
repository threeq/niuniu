// Dry-run (Precheck) tests for DataProxyService (#732): the pre-check reports
// in-scope / read-or-write / confirmation without ever executing the query or
// firing a permission card. Reads reuse the same fake mysql connector as
// data_proxy_test.go; execs counts Execute calls so "did not run" is provable.
package service_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/service"
)

func TestDataProxyPrecheckReadInScope(t *testing.T) {
	deps := newTestDataProxy(t, "writes_only", "read")
	ctx := context.Background()

	pre, err := deps.svc.Precheck(ctx, service.DataQueryInput{
		UserID: deps.uid, SourceID: deps.readSourceID, Statement: "SELECT id FROM orders",
	})
	require.NoError(t, err)
	require.True(t, pre.OK)
	require.False(t, pre.Rejected)
	require.True(t, pre.InScope)
	require.Equal(t, "read", pre.AccessMode)
	require.False(t, pre.RequiresConfirmation)
	require.Contains(t, pre.Objects, "orders")
	require.Equal(t, int32(0), atomic.LoadInt32(&deps.conn.execs), "dry run must not execute")
	require.Zero(t, deps.perm.called, "dry run must not fire a permission card")
}

func TestDataProxyPrecheckOutOfScope(t *testing.T) {
	deps := newTestDataProxy(t, "writes_only", "read")
	ctx := context.Background()

	pre, err := deps.svc.Precheck(ctx, service.DataQueryInput{
		UserID: deps.uid, SourceID: deps.readSourceID, Statement: "SELECT id FROM secrets",
	})
	require.NoError(t, err)
	require.False(t, pre.OK)
	require.True(t, pre.Rejected)
	require.Equal(t, "scope_denied", pre.Code)
	require.False(t, pre.InScope)
	require.NotEmpty(t, pre.NextAction)
	require.Equal(t, int32(0), atomic.LoadInt32(&deps.conn.execs), "dry run must not execute")
}

func TestDataProxyPrecheckWriteDisabled(t *testing.T) {
	deps := newTestDataProxy(t, "writes_only", "read") // read-only source
	ctx := context.Background()

	pre, err := deps.svc.Precheck(ctx, service.DataQueryInput{
		UserID: deps.uid, SourceID: deps.readSourceID, Statement: "DELETE FROM orders",
	})
	require.NoError(t, err)
	require.True(t, pre.Rejected)
	require.Equal(t, "write_disabled", pre.Code)
	require.Equal(t, "write", pre.AccessMode, "the dry run still reports the operation is a write")
	require.Equal(t, int32(0), atomic.LoadInt32(&deps.conn.execs))
}

func TestDataProxyPrecheckReportsPendingConfirmation(t *testing.T) {
	// require_confirm=always makes a READ need confirmation; the dry run reports
	// that WITHOUT firing the card.
	deps := newTestDataProxy(t, "always", "read")
	ctx := context.Background()

	pre, err := deps.svc.Precheck(ctx, service.DataQueryInput{
		UserID: deps.uid, SourceID: deps.readSourceID, Statement: "SELECT id FROM orders",
	})
	require.NoError(t, err)
	require.True(t, pre.OK)
	require.Equal(t, "read", pre.AccessMode)
	require.True(t, pre.RequiresConfirmation, "require_confirm=always must surface as a pending confirmation")
	require.Zero(t, deps.perm.called, "the dry run must not actually request confirmation")
	require.Equal(t, int32(0), atomic.LoadInt32(&deps.conn.execs))
}
