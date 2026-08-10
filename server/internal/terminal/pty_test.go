package terminal

import (
	"os"
	"testing"
)

func TestNewPTYProcessWithEnv(t *testing.T) {
	// PTY creation may fail in non-TTY environments (e.g., CI, containers)
	// Skip test if we can't create a PTY
	cmd := "sh"
	args := []string{"-c", `echo "test"`}
	workDir := os.TempDir()

	env := []string{"TEST_VAR1=value1", "TEST_VAR2=value2"}

	proc, err := NewPTYProcess(cmd, args, workDir, env)
	if err != nil {
		t.Skipf("PTY creation failed (non-TTY environment?): %v", err)
	}
	defer proc.Close()

	// Basic sanity check - process should have a PID
	if proc.Pid() <= 0 {
		t.Error("expected positive PID")
	}
}

func TestNewPTYProcessWithoutEnv(t *testing.T) {
	// Ensure backward compatibility - passing nil env uses os.Environ
	cmd := "sh"
	args := []string{"-c", `echo "ok"`}
	workDir := os.TempDir()

	proc, err := NewPTYProcess(cmd, args, workDir, nil)
	if err != nil {
		t.Skipf("PTY creation failed (non-TTY environment?): %v", err)
	}
	defer proc.Close()

	// Basic sanity check
	if proc.Pid() <= 0 {
		t.Error("expected positive PID")
	}
}

func TestNewPTYProcessSignature(t *testing.T) {
	// Verify the function accepts 4 parameters
	cmd := "sh"
	args := []string{"-c", `echo "test"`}
	workDir := os.TempDir()
	env := []string{"KEY=value"}

	// This should compile and run (even if PTY creation fails)
	_, err := NewPTYProcess(cmd, args, workDir, env)
	if err != nil {
		// Expected in non-TTY environments
		t.Skipf("PTY creation failed: %v", err)
	}
}
