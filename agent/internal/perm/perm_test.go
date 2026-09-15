package perm

import "testing"

func TestPolicyHeadless(t *testing.T) {
	// Strict headless: reads pass, mutations denied.
	p := NewPolicy(false)
	if p.Check("Read") != Allow || p.Check("LS") != Allow || p.Check("Grep") != Allow {
		t.Error("read-only tools must be allowed")
	}
	for _, name := range []string{"Write", "Edit", "Bash"} {
		if got := p.Check(name); got != Deny {
			t.Errorf("strict headless %s = %v, want Deny", name, got)
		}
	}
	if !IsWrite("Bash") || IsWrite("Read") {
		t.Error("IsWrite classification wrong")
	}

	// -y: mutations allowed.
	y := NewPolicy(true)
	for _, name := range []string{"Write", "Edit", "Bash"} {
		if got := y.Check(name); got != Allow {
			t.Errorf("-y %s = %v, want Allow", name, got)
		}
	}
}

func TestAllowAllChecker(t *testing.T) {
	c := AllowAllChecker()
	for _, name := range []string{"Write", "Bash", "LS", "Anything"} {
		if c.Check(name) != Allow {
			t.Errorf("AllowAll %s != Allow", name)
		}
	}
}
