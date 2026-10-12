//go:build linux

package sandbox

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// #739 requires that a distribution without unprivileged user namespaces
// degrades to Landlock + seccomp instead of refusing to run: "无 userns 的发行版
// 降级 landlock+seccomp（内核 5.13+）". The requirement is only met if the
// degraded fence is a REAL fence, so this test forces the degraded path on a
// host whose namespaces work and then re-runs the same three OS-level escape
// attempts. If Landlock and seccomp cannot carry the fence on their own, the
// probes pass and this test fails — which is the answer that matters, because
// otherwise the degradation would be a hole rather than a weaker fence.
func TestDegradedFenceStillDeniesWithoutNamespaces(t *testing.T) {
	testHelper(t)

	// Stand in for a kernel that refused CLONE_NEWUSER (Ubuntu 23.10+ does this
	// through AppArmor, which no sysctl reflects, so the refusal is only visible
	// from a failed clone — exactly what Spawn records).
	noteNamespacesRefused(errors.New("simulated: the kernel refused CLONE_NEWUSER"))
	defer resetNamespacesRefused()
	ResetSelfCheckCache()

	av := SelfCheck(true)
	if !av.Available {
		t.Fatalf("the degraded fence must still be usable, got unavailable: %s", av.Reason)
	}
	if !av.Degraded {
		t.Fatal("a fence missing user namespaces must be reported as degraded, not as fully healthy")
	}
	if av.Backend != "linux-landlock-seccomp" {
		t.Errorf("backend = %q, want the degraded name so the UI names what is actually enforcing", av.Backend)
	}
	found := false
	for _, c := range av.Components {
		if c.Name != "user-namespaces" {
			continue
		}
		found = true
		if c.OK {
			t.Error("user-namespaces must not be reported OK once a clone was refused")
		}
		if c.Required {
			t.Error("user-namespaces must be optional, otherwise the host refuses to run instead of degrading")
		}
	}
	if !found {
		t.Fatal("the verdict does not mention user-namespaces at all")
	}

	// The filesystem probe: landlock alone has to deny this.
	secretDir, err := os.MkdirTemp("", "funcbox-degraded-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(secretDir)
	secret := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("top-secret-value"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The socket probe: seccomp alone has to deny this, with no empty netns.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rep, err := FenceSelfTest(ctx, secret, ln.Addr().String())
	if err != nil {
		t.Fatalf("degraded fence self-test: %v", err)
	}
	for _, p := range rep.Probes {
		t.Logf("degraded probe %-18s denied=%-5v %s", p.Name, p.Denied, p.Error)
	}
	if !rep.OK {
		t.Fatalf("the degraded fence let an escape through (backend=%s)", rep.Backend)
	}
	if rep.Backend != "linux-landlock-seccomp" {
		t.Errorf("self-test ran against backend %q, want the degraded one", rep.Backend)
	}
}

// The namespaced path must come back once the refusal is cleared, so a test
// that forced the degraded path cannot leak into the rest of the package.
func TestNamespacesAreUsedWhenAvailableAfterADegradedRun(t *testing.T) {
	resetNamespacesRefused()
	if refused, why := namespacesRefused(); refused {
		t.Fatalf("the refusal flag survived a reset: %s", why)
	}
	if ok, detail := probeUserNamespaces(); ok {
		if got := (linuxFence{}).Backend(); got != "linux-namespaces-landlock-seccomp" {
			t.Errorf("backend = %q, want the namespaced one on a host whose sysctls allow it", got)
		}
		_ = detail
	}
}
