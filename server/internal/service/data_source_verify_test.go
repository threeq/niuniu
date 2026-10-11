// Tests for the data-source "verify after publish" rollback (#732): a source
// whose post-create connection check fails is deleted again by default, so a
// broken connection never lingers; keep_on_fail opts out.
package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

// failingPingConnector is a mysql connector whose Ping always fails (the
// connection is unreachable), while Classify/Execute are unused here.
type failingPingConnector struct{}

func (failingPingConnector) Kind() dataconn.SourceKind { return dataconn.KindMySQL }
func (failingPingConnector) Ping(context.Context, dataconn.ConnConfig) error {
	return errors.New("dial tcp: connection refused")
}
func (failingPingConnector) Classify(dataconn.Operation) (dataconn.AccessMode, dataconn.ResourceRef, error) {
	return dataconn.ModeRead, dataconn.ResourceRef{}, nil
}
func (failingPingConnector) Execute(context.Context, dataconn.ConnConfig, dataconn.Operation) (*dataconn.ResultSet, error) {
	return nil, errors.New("unused")
}

func newVerifyRollbackEnv(t *testing.T) (*service.DataSourceService, int64, int64) {
	t.Helper()
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	require.NoError(t, err)
	reg := dataconn.NewRegistry()
	reg.Register(dataconn.KindMySQL, failingPingConnector{})
	dsSvc := service.NewDataSourceService(env.Queries(), env.DB, kr, reg)
	uid := env.UserA
	ctx := context.Background()
	id, err := dsSvc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "unreachable", Kind: "mysql",
		Config:            map[string]any{"host": "nowhere", "port": 3306, "user": "u", "password": "p", "database": "d"},
		ScopeConfig:       map[string]any{},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	require.NoError(t, err)
	return dsSvc, id, uid
}

func TestVerifyOrRollbackDeletesOnFailure(t *testing.T) {
	dsSvc, id, uid := newVerifyRollbackEnv(t)
	ctx := context.Background()

	verified, rolledBack, err := dsSvc.VerifyOrRollback(ctx, id, uid, nil, false)
	require.False(t, verified)
	require.True(t, rolledBack)
	require.Error(t, err)

	_, gerr := dsSvc.Get(ctx, id, uid, nil)
	require.ErrorIs(t, gerr, service.ErrDataSourceNotFound, "a failed verify must roll the source back")
}

func TestVerifyOrRollbackKeepOnFail(t *testing.T) {
	dsSvc, id, uid := newVerifyRollbackEnv(t)
	ctx := context.Background()

	verified, rolledBack, err := dsSvc.VerifyOrRollback(ctx, id, uid, nil, true)
	require.False(t, verified)
	require.False(t, rolledBack)
	require.Error(t, err)

	got, gerr := dsSvc.Get(ctx, id, uid, nil)
	require.NoError(t, gerr)
	require.Equal(t, id, got.ID, "keep_on_fail keeps the source even though it is unreachable")
}
