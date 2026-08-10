package checkers_test

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness/checkers"
	"github.com/stretchr/testify/assert"
)

func TestParseCoveragePercent_ValidOutput(t *testing.T) {
	cases := []struct {
		name     string
		output   string
		expected float64
		ok       bool
	}{
		{
			name:     "standard go test output",
			output:   "ok  \tgithub.com/foo/bar\tcoverage: 85.3% of statements",
			expected: 85.3,
			ok:       true,
		},
		{
			name:     "100 percent coverage",
			output:   "coverage: 100.0% of statements",
			expected: 100.0,
			ok:       true,
		},
		{
			name:     "zero coverage",
			output:   "coverage: 0.0% of statements",
			expected: 0.0,
			ok:       true,
		},
		{
			name:     "integer coverage no decimal",
			output:   "coverage: 75% of statements",
			expected: 75.0,
			ok:       true,
		},
		{
			name:   "no coverage in output",
			output: "PASS\nok  github.com/foo/bar 0.123s",
			ok:     false,
		},
		{
			name:   "empty output",
			output: "",
			ok:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := checkers.ParseCoveragePercent(tc.output)
			assert.Equal(t, tc.ok, ok)
			if tc.ok {
				assert.InDelta(t, tc.expected, got, 0.001)
			}
		})
	}
}
