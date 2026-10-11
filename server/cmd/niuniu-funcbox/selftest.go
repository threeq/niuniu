package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/niuniu-dev/niuniu/internal/sandbox"
)

// fenceSelfTest exercises the OS fence DIRECTLY, from Go, without going through
// the JavaScript runtime.
//
// That distinction is the whole point of the acceptance criterion in #739: the
// test must show the OPERATING SYSTEM refusing a file read, a direct socket and
// a child process — not merely show that the JS sandbox has no such API. Running
// the attempts in Go is the strongest available proof, because it behaves as if
// the interpreter had already been escaped.
func fenceSelfTest(file, addr string) sandbox.ResultMessage {
	probes := []map[string]any{
		probeRead(file),
		probeConnect(addr),
		probeSpawn(),
	}
	allDenied := true
	for _, p := range probes {
		if denied, _ := p["denied"].(bool); !denied {
			allDenied = false
		}
	}
	return sandbox.ResultMessage{
		Kind: sandbox.KindResult,
		OK:   true,
		Output: map[string]any{
			"all_denied": allDenied,
			"probes":     probes,
			"pid":        os.Getpid(),
		},
	}
}

func probeRead(path string) map[string]any {
	if path == "" {
		return map[string]any{"name": "read_file", "denied": false,
			"error": "no probe path was supplied"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{"name": "read_file", "denied": true, "error": err.Error()}
	}
	// A successful read that returned nothing is still an escape; report the
	// size so a false positive cannot hide behind an empty file.
	return map[string]any{"name": "read_file", "denied": false,
		"error": fmt.Sprintf("the file was readable (%d bytes)", len(data))}
}

func probeConnect(addr string) map[string]any {
	if addr == "" {
		return map[string]any{"name": "direct_socket", "denied": false,
			"error": "no probe address was supplied"}
	}
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return map[string]any{"name": "direct_socket", "denied": true, "error": err.Error()}
	}
	_ = conn.Close()
	return map[string]any{"name": "direct_socket", "denied": false,
		"error": "a TCP connection was established"}
}

func probeSpawn() map[string]any {
	// The command string is a hardcoded literal and the process is never meant
	// to run: the whole assertion is that the fence refuses to create it. No
	// caller-supplied value reaches it.
	var cmd *exec.Cmd
	if _, err := os.Stat(`C:\Windows\System32\cmd.exe`); err == nil {
		cmd = exec.Command(`C:\Windows\System32\cmd.exe`, "/c", "echo box-escape")
	} else {
		cmd = exec.Command("/bin/sh", "-c", "echo box-escape")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return map[string]any{"name": "spawn_subprocess", "denied": true, "error": err.Error()}
	}
	return map[string]any{"name": "spawn_subprocess", "denied": false,
		"error": fmt.Sprintf("a child process ran and produced %q", string(out))}
}
