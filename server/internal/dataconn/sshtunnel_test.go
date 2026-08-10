package dataconn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// --- test fixtures: an in-process SSH server + a target TCP echo server ---

// newHostKey generates an ephemeral ed25519 host key for the test SSH server.
func newHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer from host key: %v", err)
	}
	return signer
}

// newClientKeyPEM generates an ephemeral ed25519 client key, returning its
// PEM encoding and public key.
func newClientKeyPEM(t *testing.T) (keyPEM string, pub ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer from client key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), signer.PublicKey()
}

// startEchoServer starts a TCP server that replies "echo:"+request. Returns its
// address and a stop func.
func startEchoServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 4096)
				n, err := conn.Read(buf)
				if err != nil {
					return
				}
				_, _ = conn.Write(append([]byte("echo:"), buf[:n]...))
			}(c)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

// sshServerOpts configures the in-process test SSH server.
type sshServerOpts struct {
	hostKey       ssh.Signer
	password      string        // if set, password auth accepted for this value
	authorizedKey ssh.PublicKey // if set, publickey auth accepted for this key
}

// startSSHServer starts an in-process SSH server that authenticates per opts and
// services direct-tcpip channels by dialing the requested target and piping.
func startSSHServer(t *testing.T, opts sshServerOpts) (addr string, stop func()) {
	t.Helper()
	cfg := &ssh.ServerConfig{}
	if opts.password != "" {
		cfg.PasswordCallback = func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == opts.password {
				return &ssh.Permissions{}, nil
			}
			return nil, io.EOF
		}
	}
	if opts.authorizedKey != nil {
		want := opts.authorizedKey.Marshal()
		cfg.PublicKeyCallback = func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(want) {
				return &ssh.Permissions{}, nil
			}
			return nil, io.EOF
		}
	}
	cfg.AddHostKey(opts.hostKey)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ssh listen: %v", err)
	}
	go func() {
		for {
			nConn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSHConn(nConn, cfg)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func serveSSHConn(nConn net.Conn, cfg *ssh.ServerConfig) {
	defer nConn.Close()
	sc, chans, reqs, err := ssh.NewServerConn(nConn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "direct-tcpip" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only direct-tcpip supported")
			continue
		}
		go serveDirectTCPIP(newCh)
	}
}

// directTCPIPExtra is the RFC 4254 direct-tcpip channel payload.
type directTCPIPExtra struct {
	DestHost string
	DestPort uint32
	SrcHost  string
	SrcPort  uint32
}

func serveDirectTCPIP(newCh ssh.NewChannel) {
	var payload directTCPIPExtra
	if err := ssh.Unmarshal(newCh.ExtraData(), &payload); err != nil {
		_ = newCh.Reject(ssh.ConnectionFailed, "bad payload")
		return
	}
	target := net.JoinHostPort(payload.DestHost, strconv.Itoa(int(payload.DestPort)))
	remote, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		_ = newCh.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := newCh.Accept()
	if err != nil {
		remote.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() { _, _ = io.Copy(remote, ch); remote.Close() }()
	go func() { _, _ = io.Copy(ch, remote); ch.Close() }()
}

// splitHostPort returns host and int port from an addr like "127.0.0.1:5432".
func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("atoi %q: %v", portStr, err)
	}
	return host, p
}

// dialAndEcho dials the tunnel's local endpoint, sends "ping", returns the reply.
func dialAndEcho(t *testing.T, cc ConnConfig) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(cc.Host, strconv.Itoa(cc.Port)), 5*time.Second)
	if err != nil {
		t.Fatalf("dial local tunnel endpoint: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buf[:n])
}

// --- tests ---

func TestOpenTunnel_DisabledIsNoop(t *testing.T) {
	cc := ConnConfig{Host: "db.internal", Port: 5432}
	out, cleanup, err := OpenTunnel(context.Background(), cc)
	if err != nil {
		t.Fatalf("OpenTunnel: %v", err)
	}
	defer cleanup()
	if out.Host != "db.internal" || out.Port != 5432 {
		t.Fatalf("nil SSH must not rewrite host/port, got %s:%d", out.Host, out.Port)
	}

	cc.SSH = &SSHConfig{Enabled: false, Host: "bastion"}
	out2, cleanup2, err := OpenTunnel(context.Background(), cc)
	if err != nil {
		t.Fatalf("OpenTunnel disabled: %v", err)
	}
	defer cleanup2()
	if out2.Host != "db.internal" {
		t.Fatalf("SSH.Enabled=false must be a no-op, got host %s", out2.Host)
	}
}

func TestOpenTunnel_PrivateKeyAuthForwards(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()
	keyPEM, clientPub := newClientKeyPEM(t)
	hostKey := newHostKey(t)
	sshAddr, stopSSH := startSSHServer(t, sshServerOpts{hostKey: hostKey, authorizedKey: clientPub})
	defer stopSSH()

	sshHost, sshPort := splitHostPort(t, sshAddr)
	echoHost, echoPort := splitHostPort(t, echoAddr)

	cc := ConnConfig{
		Host: echoHost, Port: echoPort,
		SSH: &SSHConfig{
			Enabled: true, Host: sshHost, Port: sshPort, User: "tester",
			AuthMethod: SSHAuthPrivateKey, PrivateKey: keyPEM,
		},
	}
	out, cleanup, err := OpenTunnel(context.Background(), cc)
	if err != nil {
		t.Fatalf("OpenTunnel: %v", err)
	}
	defer cleanup()
	if out.Host != "127.0.0.1" || out.Port == 0 || out.Port == echoPort {
		t.Fatalf("tunnel must rewrite host/port to loopback, got %s:%d", out.Host, out.Port)
	}
	if got := dialAndEcho(t, out); got != "echo:ping" {
		t.Fatalf("tunnel did not forward to target, got %q", got)
	}
}

func TestOpenTunnel_PasswordAuthForwards(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()
	hostKey := newHostKey(t)
	sshAddr, stopSSH := startSSHServer(t, sshServerOpts{hostKey: hostKey, password: "s3cret"})
	defer stopSSH()

	sshHost, sshPort := splitHostPort(t, sshAddr)
	echoHost, echoPort := splitHostPort(t, echoAddr)

	cc := ConnConfig{
		Host: echoHost, Port: echoPort,
		SSH: &SSHConfig{
			Enabled: true, Host: sshHost, Port: sshPort, User: "tester",
			AuthMethod: SSHAuthPassword, Password: "s3cret",
		},
	}
	out, cleanup, err := OpenTunnel(context.Background(), cc)
	if err != nil {
		t.Fatalf("OpenTunnel: %v", err)
	}
	defer cleanup()
	if got := dialAndEcho(t, out); got != "echo:ping" {
		t.Fatalf("password-auth tunnel did not forward, got %q", got)
	}
}

func TestOpenTunnel_HostKeyPinMatch(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()
	hostKey := newHostKey(t)
	sshAddr, stopSSH := startSSHServer(t, sshServerOpts{hostKey: hostKey, password: "pw"})
	defer stopSSH()

	sshHost, sshPort := splitHostPort(t, sshAddr)
	echoHost, echoPort := splitHostPort(t, echoAddr)
	fp := ssh.FingerprintSHA256(hostKey.PublicKey())

	cc := ConnConfig{
		Host: echoHost, Port: echoPort,
		SSH: &SSHConfig{
			Enabled: true, Host: sshHost, Port: sshPort, User: "u",
			AuthMethod: SSHAuthPassword, Password: "pw", HostKey: fp,
		},
	}
	out, cleanup, err := OpenTunnel(context.Background(), cc)
	if err != nil {
		t.Fatalf("OpenTunnel with correct host key pin: %v", err)
	}
	defer cleanup()
	if got := dialAndEcho(t, out); got != "echo:ping" {
		t.Fatalf("pinned tunnel did not forward, got %q", got)
	}
}

func TestOpenTunnel_HostKeyPinMismatch(t *testing.T) {
	hostKey := newHostKey(t)
	sshAddr, stopSSH := startSSHServer(t, sshServerOpts{hostKey: hostKey, password: "pw"})
	defer stopSSH()
	sshHost, sshPort := splitHostPort(t, sshAddr)

	cc := ConnConfig{
		Host: "127.0.0.1", Port: 65000,
		SSH: &SSHConfig{
			Enabled: true, Host: sshHost, Port: sshPort, User: "u",
			AuthMethod: SSHAuthPassword, Password: "pw",
			HostKey: "SHA256:definitely-not-the-real-fingerprint",
		},
	}
	_, cleanup, err := OpenTunnel(context.Background(), cc)
	defer cleanup()
	if err == nil {
		t.Fatal("expected host key mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "host key mismatch") {
		t.Fatalf("expected host key mismatch error, got %v", err)
	}
}

func TestOpenTunnel_WrongPasswordFails(t *testing.T) {
	hostKey := newHostKey(t)
	sshAddr, stopSSH := startSSHServer(t, sshServerOpts{hostKey: hostKey, password: "right"})
	defer stopSSH()
	sshHost, sshPort := splitHostPort(t, sshAddr)

	cc := ConnConfig{
		Host: "127.0.0.1", Port: 65000,
		SSH: &SSHConfig{
			Enabled: true, Host: sshHost, Port: sshPort, User: "u",
			AuthMethod: SSHAuthPassword, Password: "wrong",
		},
	}
	_, cleanup, err := OpenTunnel(context.Background(), cc)
	defer cleanup()
	if err == nil {
		t.Fatal("expected auth failure, got nil")
	}
}

func TestParseSSHPrivateKey_PassphraseRejected(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("hunter2"))
	if err != nil {
		t.Fatalf("marshal with passphrase: %v", err)
	}
	_, perr := parseSSHPrivateKey(pem.EncodeToMemory(block))
	if perr != ErrSSHPassphraseKey {
		t.Fatalf("expected ErrSSHPassphraseKey, got %v", perr)
	}
}

func TestSSHClientConfig_Validation(t *testing.T) {
	if _, err := sshClientConfig(SSHConfig{User: "u", AuthMethod: SSHAuthPassword, Password: "p"}); err == nil {
		t.Fatal("expected error when host missing")
	}
	if _, err := sshClientConfig(SSHConfig{Host: "h", AuthMethod: SSHAuthPassword, Password: "p"}); err == nil {
		t.Fatal("expected error when user missing")
	}
	if _, err := sshClientConfig(SSHConfig{Host: "h", User: "u"}); err == nil {
		t.Fatal("expected error when no credential present")
	}
}

func TestOpenTunnel_ContextCancelledDialFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cc := ConnConfig{
		Host: "127.0.0.1", Port: 65000,
		SSH: &SSHConfig{
			Enabled: true, Host: "127.0.0.1", Port: 1, User: "u",
			AuthMethod: SSHAuthPassword, Password: "p",
		},
	}
	_, cleanup, err := OpenTunnel(ctx, cc)
	defer cleanup()
	if err == nil {
		t.Fatal("expected dial failure with cancelled context")
	}
}
