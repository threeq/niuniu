package testing_test

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/service"
	nittest "github.com/niuniu-dev/niuniu/internal/testing"
)

func TestIsolationSuiteRunsOverFakeService(t *testing.T) {
	env := nittest.NewIsolationEnv(t)
	if env.OrgA == 0 || env.OrgB == 0 || env.UserA == 0 || env.UserB == 0 {
		t.Errorf("env = %+v; expected non-zero IDs", env)
	}
	if env.OrgOwner == nil {
		t.Error("expected OrgOwner helper to be populated")
	}
	_ = service.OwnerRef{Type: "org", ID: env.OrgA}
}
