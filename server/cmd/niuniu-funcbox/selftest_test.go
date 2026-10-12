package main

import "testing"

func probe(name string, fields map[string]any) map[string]any {
	p := map[string]any{"name": name, "denied": false}
	for k, v := range fields {
		p[k] = v
	}
	return p
}

// The whole point of --fence-selftest is the verdict, and the verdict must not
// be reachable by a report that let something through: an escape has to be
// distinguishable from a probe that was never configured.
func TestClassifyProbesSeparatesEscapesFromSkips(t *testing.T) {
	denied := probe("read_file", map[string]any{"denied": true})
	skipped := probe("direct_socket", map[string]any{"skipped": true})
	escaped := probe("spawn_subprocess", map[string]any{"escaped": true})

	cases := []struct {
		name          string
		probes        []map[string]any
		wantAllDenied bool
		wantEscaped   bool
	}{
		{"all refused", []map[string]any{denied, denied, denied}, true, false},
		{"nothing configured is not a failure", []map[string]any{skipped, skipped, denied}, true, false},
		{"one escape", []map[string]any{denied, escaped, denied}, false, true},
		{"escape hidden among skips", []map[string]any{skipped, escaped, skipped}, false, true},
		{"not denied and not marked", []map[string]any{denied, probe("direct_socket", nil), denied}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allDenied, escaped := classifyProbes(tc.probes)
			if allDenied != tc.wantAllDenied || escaped != tc.wantEscaped {
				t.Fatalf("classifyProbes = (allDenied=%v, escaped=%v), want (%v, %v)",
					allDenied, escaped, tc.wantAllDenied, tc.wantEscaped)
			}
		})
	}
}

// An unconfigured probe must be reported as skipped, and a real escape must be
// flagged — otherwise the exit code in main cannot tell the two apart.
func TestProbesMarkSkippedAndEscaped(t *testing.T) {
	if p := probeRead(""); p["skipped"] != true {
		t.Errorf("probeRead with no path: skipped=%v, want true", p["skipped"])
	}
	if p := probeConnect(""); p["skipped"] != true {
		t.Errorf("probeConnect with no address: skipped=%v, want true", p["skipped"])
	}
	// 127.0.0.1:1 is not listening, so the dial fails and the probe counts as
	// denied; the assertion here is only that a failed dial is never an escape.
	if p := probeConnect("127.0.0.1:1"); p["escaped"] == true {
		t.Errorf("a refused dial was reported as an escape: %v", p)
	}
}
