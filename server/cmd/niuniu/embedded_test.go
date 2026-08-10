package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/stretchr/testify/require"
)

func TestApplyEmbeddedOverrides(t *testing.T) {
	cfg := &config.Config{
		Server:  config.ServerConfig{Host: "0.0.0.0", Port: 3000},
		Auth:    config.AuthConfig{Enabled: true},
		Log:     config.LogConfig{Output: "terminal"},
		DataDir: "/home/u/.niuniu",
	}
	applyEmbeddedOverrides(cfg, Flags{Embedded: true, Addr: "127.0.0.1:0"})

	require.False(t, cfg.Auth.Enabled, "auth must be forced off")
	require.Equal(t, "127.0.0.1", cfg.Server.Host, "host must be forced to localhost")
	require.Equal(t, 0, cfg.Server.Port, "port must be 0 when --addr provides ephemeral")
	require.Equal(t, "file", cfg.Log.Output,
		"log output MUST be forced to file in embedded mode — otherwise slog writes to stdout before emitReady and breaks the handshake")
	require.Equal(t, filepath.Join("/home/u/.niuniu", "logs"), cfg.Log.FileDir,
		"log file dir defaults under DataDir when not set")
}

func TestApplyEmbeddedOverrides_PreservesUserFileDir(t *testing.T) {
	cfg := &config.Config{
		Server:  config.ServerConfig{Host: "0.0.0.0", Port: 3000},
		Log:     config.LogConfig{Output: "both", FileDir: "/custom/log/dir"},
		DataDir: "/home/u/.niuniu",
	}
	applyEmbeddedOverrides(cfg, Flags{Embedded: true})
	require.Equal(t, "file", cfg.Log.Output)
	require.Equal(t, "/custom/log/dir", cfg.Log.FileDir, "user-provided FileDir preserved")
}

func TestApplyEmbeddedOverrides_AddrHostOnly(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Host: "0.0.0.0", Port: 3000}}
	applyEmbeddedOverrides(cfg, Flags{Embedded: true, Addr: "127.0.0.1"})
	require.Equal(t, "127.0.0.1", cfg.Server.Host)
	require.Equal(t, 3000, cfg.Server.Port, "port preserved when --addr has no colon")
}

func TestApplyEmbeddedOverrides_NoEmbeddedFlag(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "0.0.0.0", Port: 3000},
		Auth:   config.AuthConfig{Enabled: true},
		Log:    config.LogConfig{Output: "terminal"},
	}
	applyEmbeddedOverrides(cfg, Flags{Embedded: false})
	require.True(t, cfg.Auth.Enabled, "cfg untouched when --embedded not set")
	require.Equal(t, "0.0.0.0", cfg.Server.Host)
	require.Equal(t, "terminal", cfg.Log.Output, "log output untouched when --embedded not set")
}

func TestApplyEmbeddedOverrides_BarePortDoesNotBlankHost(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "0.0.0.0", Port: 3000},
	}
	// Bare ":0" must NOT blank out the host — the bind would otherwise be
	// 0.0.0.0:0 which violates the embedded "localhost-only" invariant.
	applyEmbeddedOverrides(cfg, Flags{Embedded: true, Addr: ":0"})
	require.Equal(t, "127.0.0.1", cfg.Server.Host,
		"embedded forces host=127.0.0.1; --addr=:0 must not overwrite it")
}

func TestApplyEmbeddedOverrides_AddrWithoutEmbedded(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "0.0.0.0", Port: 3000},
		Auth:   config.AuthConfig{Enabled: true},
		Log:    config.LogConfig{Output: "terminal"},
	}
	applyEmbeddedOverrides(cfg, Flags{Embedded: false, Addr: "10.0.0.1:8080"})
	require.Equal(t, "10.0.0.1", cfg.Server.Host, "host overridden by --addr")
	require.Equal(t, 8080, cfg.Server.Port, "port overridden by --addr")
	require.True(t, cfg.Auth.Enabled, "Auth untouched when --embedded not set")
	require.Equal(t, "terminal", cfg.Log.Output, "Log.Output untouched when --embedded not set")
}

func TestApplyEmbeddedOverrides_BarePortSetsPort(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 3000},
	}
	applyEmbeddedOverrides(cfg, Flags{Embedded: false, Addr: ":54321"})
	require.Equal(t, "127.0.0.1", cfg.Server.Host, "host must be unchanged for bare :port form")
	require.Equal(t, 54321, cfg.Server.Port, "port must be set by bare :port form")
}

func TestApplyEmbeddedOverrides_InvalidAddrIgnored(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", Port: 3000},
	}
	applyEmbeddedOverrides(cfg, Flags{Embedded: false, Addr: "xxx:abc"})
	require.Equal(t, "127.0.0.1", cfg.Server.Host, "host must be unchanged for invalid addr")
	require.Equal(t, 3000, cfg.Server.Port, "port must be unchanged for invalid addr")
}

func tcpListen(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }

func TestListenEmbedded_ReusesPreferredPort(t *testing.T) {
	portFile := filepath.Join(t.TempDir(), "embedded.port")

	// First launch: no port file -> ephemeral; persist the bound port.
	ln1, err := listenEmbedded(tcpListen, "127.0.0.1", 0, portFile)
	require.NoError(t, err)
	addr1 := ln1.Addr().String()
	savePreferredPort(portFile, addr1)
	require.NoError(t, ln1.Close())

	// Second launch: must come back on the same port.
	ln2, err := listenEmbedded(tcpListen, "127.0.0.1", 0, portFile)
	require.NoError(t, err)
	defer ln2.Close()
	require.Equal(t, addr1, ln2.Addr().String(), "restart must reuse the persisted port")
}

func TestListenEmbedded_FallsBackWhenPreferredPortOccupied(t *testing.T) {
	portFile := filepath.Join(t.TempDir(), "embedded.port")

	// Occupy a port and persist it as the preferred one.
	occupier, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer occupier.Close()
	savePreferredPort(portFile, occupier.Addr().String())

	ln, err := listenEmbedded(tcpListen, "127.0.0.1", 0, portFile)
	require.NoError(t, err, "occupied preferred port must fall back, not fail")
	defer ln.Close()
	require.NotEqual(t, occupier.Addr().String(), ln.Addr().String())
}

func TestListenEmbedded_ExplicitPortIgnoresPreferred(t *testing.T) {
	portFile := filepath.Join(t.TempDir(), "embedded.port")
	require.NoError(t, os.WriteFile(portFile, []byte("1"), 0o600))

	// Pick a known-free port, then bind it explicitly via listenEmbedded.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := probe.Addr().(*net.TCPAddr).Port
	require.NoError(t, probe.Close())

	ln, err := listenEmbedded(tcpListen, "127.0.0.1", port, portFile)
	require.NoError(t, err)
	defer ln.Close()
	require.Equal(t, port, ln.Addr().(*net.TCPAddr).Port,
		"explicit port must win over the persisted one")
}

func TestReadPreferredPort_InvalidOrMissing(t *testing.T) {
	dir := t.TempDir()
	require.Zero(t, readPreferredPort(filepath.Join(dir, "absent")), "missing file -> 0")

	for name, body := range map[string]string{
		"garbage":  "not-a-port",
		"zero":     "0",
		"negative": "-1",
		"too-big":  "70000",
	} {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		require.Zero(t, readPreferredPort(p), "body %q must be rejected", body)
	}

	valid := filepath.Join(dir, "valid")
	require.NoError(t, os.WriteFile(valid, []byte(" 54321\n"), 0o600))
	require.Equal(t, 54321, readPreferredPort(valid), "whitespace-padded port accepted")
}

func TestSavePreferredPort_InvalidAddrIsNoop(t *testing.T) {
	portFile := filepath.Join(t.TempDir(), "embedded.port")
	savePreferredPort(portFile, "not-an-addr")
	_, err := os.Stat(portFile)
	require.True(t, os.IsNotExist(err), "invalid addr must not create the port file")
}
