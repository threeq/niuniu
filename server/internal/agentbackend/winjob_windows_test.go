//go:build windows

package agentbackend

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestIsTransientSpawnError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"sharing violation", syscall.Errno(32), true},                    // ERROR_SHARING_VIOLATION
		{"lock violation", syscall.Errno(33), true},                       // ERROR_LOCK_VIOLATION
		{"wrapped sharing violation", fmt.Errorf("fork/exec x: %w", syscall.Errno(32)), true},
		{"file not found", syscall.Errno(2), false},                       // ERROR_FILE_NOT_FOUND
		{"plain error", errors.New("exec: no command"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := IsTransientSpawnError(tc.err); got != tc.want {
			t.Errorf("%s: IsTransientSpawnError = %v, want %v", tc.name, got, tc.want)
		}
	}
}
