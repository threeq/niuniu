package dataconn

// SSH-tunnel support for data sources. All connector families (SQL, redis,
// mongo, elasticsearch, http) ultimately dial ConnConfig.Host:Port, so a single
// TCP-level local port forward transparently covers every kind: OpenTunnel dials
// the SSH server, opens a loopback listener that forwards each accepted
// connection over the SSH transport to the real target, and rewrites Host/Port
// to that loopback endpoint. The connectors are untouched.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHAuthMethod selects how the tunnel authenticates to the SSH server.
type SSHAuthMethod string

const (
	SSHAuthPassword   SSHAuthMethod = "password"
	SSHAuthPrivateKey SSHAuthMethod = "private_key"
)

// sshDialTimeout bounds both the TCP dial and the SSH handshake.
const sshDialTimeout = 10 * time.Second

// SSHConfig holds decrypted SSH-tunnel parameters for a data source. It travels
// inside ConnConfig and, like the rest of ConnConfig, MUST NOT escape the
// service layer. When Enabled is false the tunnel is a no-op.
type SSHConfig struct {
	Enabled    bool
	Host       string
	Port       int // SSH server port; 0 -> 22
	User       string
	AuthMethod SSHAuthMethod
	Password   string // used when AuthMethod == password
	PrivateKey string // PEM; used when AuthMethod == private_key; MUST NOT be passphrase-encrypted
	// HostKey, when non-empty, pins the SSH server's host key: the connection is
	// rejected unless the server key's SHA256 fingerprint (the "SHA256:..." form
	// ssh.FingerprintSHA256 returns) equals this value. Empty disables host-key
	// verification (accept any) — a documented trade-off for the local-first case
	// where the user configures their own bastion.
	HostKey string
}

// ErrSSHPassphraseKey mirrors the git-credential convention: niuniu does not
// store SSH key passphrases, so a passphrase-encrypted private key is rejected;
// the user must decrypt it locally first.
var ErrSSHPassphraseKey = errors.New("dataconn: passphrase-protected SSH keys are not supported; decrypt the key locally first")

// SSHTunnel is a loopback TCP listener that forwards every accepted connection
// over an SSH client to a fixed remote address. Close it to stop forwarding and
// drop the SSH connection.
type SSHTunnel struct {
	listener  net.Listener
	client    *ssh.Client
	remote    string
	localHost string
	localPort int
	closed    chan struct{}
}

// LocalHost / LocalPort are the loopback endpoint callers dial instead of the
// real remote; the tunnel forwards it to remote over SSH.
func (t *SSHTunnel) LocalHost() string { return t.localHost }
func (t *SSHTunnel) LocalPort() int    { return t.localPort }

// Close stops the accept loop and closes the SSH client. Safe to call once.
func (t *SSHTunnel) Close() error {
	select {
	case <-t.closed:
		return nil
	default:
		close(t.closed)
	}
	if t.listener != nil {
		_ = t.listener.Close()
	}
	if t.client != nil {
		return t.client.Close()
	}
	return nil
}

// OpenTunnel establishes an SSH tunnel for cc when cc.SSH is enabled and returns
// a copy of cc with Host/Port rewritten to the local forward endpoint, plus a
// cleanup func. When SSH is disabled it returns cc unchanged with a no-op
// cleanup, so callers can unconditionally `defer cleanup()`.
func OpenTunnel(ctx context.Context, cc ConnConfig) (ConnConfig, func() error, error) {
	noop := func() error { return nil }
	if cc.SSH == nil || !cc.SSH.Enabled {
		return cc, noop, nil
	}
	if cc.Port == 0 {
		return cc, noop, fmt.Errorf("dataconn: ssh tunnel: target port is required")
	}
	tun, err := dialTunnel(ctx, *cc.SSH, cc.Host, cc.Port)
	if err != nil {
		return cc, noop, err
	}
	out := cc
	out.Host = tun.LocalHost()
	out.Port = tun.LocalPort()
	return out, tun.Close, nil
}

// dialTunnel connects to the SSH server, opens a loopback listener, and starts
// forwarding accepted connections to remoteHost:remotePort over SSH.
func dialTunnel(ctx context.Context, cfg SSHConfig, remoteHost string, remotePort int) (*SSHTunnel, error) {
	clientCfg, err := sshClientConfig(cfg)
	if err != nil {
		return nil, err
	}
	sshPort := cfg.Port
	if sshPort == 0 {
		sshPort = 22
	}
	sshAddr := net.JoinHostPort(cfg.Host, strconv.Itoa(sshPort))

	d := net.Dialer{Timeout: sshDialTimeout}
	netConn, err := d.DialContext(ctx, "tcp", sshAddr)
	if err != nil {
		return nil, fmt.Errorf("dataconn: ssh tunnel: dial %s: %w", sshAddr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, sshAddr, clientCfg)
	if err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("dataconn: ssh tunnel: handshake %s: %w", sshAddr, err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("dataconn: ssh tunnel: local listen: %w", err)
	}
	t := &SSHTunnel{
		listener:  ln,
		client:    client,
		remote:    net.JoinHostPort(remoteHost, strconv.Itoa(remotePort)),
		localHost: "127.0.0.1",
		localPort: ln.Addr().(*net.TCPAddr).Port,
		closed:    make(chan struct{}),
	}
	go t.acceptLoop()
	return t, nil
}

// acceptLoop forwards each inbound loopback connection over SSH until Close.
func (t *SSHTunnel) acceptLoop() {
	for {
		local, err := t.listener.Accept()
		if err != nil {
			return // listener closed (by Close) or fatal accept error
		}
		go t.forward(local)
	}
}

// forward pipes a single loopback connection to the remote target over SSH,
// closing both ends when either direction finishes.
func (t *SSHTunnel) forward(local net.Conn) {
	defer local.Close()
	remote, err := t.client.Dial("tcp", t.remote)
	if err != nil {
		return
	}
	defer remote.Close()
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go pipe(remote, local)
	go pipe(local, remote)
	<-done
}

// sshClientConfig builds the *ssh.ClientConfig from an SSHConfig, resolving the
// auth method and host-key callback.
func sshClientConfig(cfg SSHConfig) (*ssh.ClientConfig, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("dataconn: ssh tunnel: host is required")
	}
	if strings.TrimSpace(cfg.User) == "" {
		return nil, fmt.Errorf("dataconn: ssh tunnel: user is required")
	}
	auth, err := sshAuthMethod(cfg)
	if err != nil {
		return nil, err
	}
	hkc, err := hostKeyCallback(cfg.HostKey)
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: hkc,
		Timeout:         sshDialTimeout,
	}, nil
}

// sshAuthMethod resolves the ssh.AuthMethod. An empty AuthMethod is inferred
// from whichever credential is present (private key preferred) so a config that
// only carries a key still works.
func sshAuthMethod(cfg SSHConfig) (ssh.AuthMethod, error) {
	method := cfg.AuthMethod
	if method == "" {
		switch {
		case strings.TrimSpace(cfg.PrivateKey) != "":
			method = SSHAuthPrivateKey
		case cfg.Password != "":
			method = SSHAuthPassword
		}
	}
	switch method {
	case SSHAuthPrivateKey:
		signer, err := parseSSHPrivateKey([]byte(cfg.PrivateKey))
		if err != nil {
			return nil, err
		}
		return ssh.PublicKeys(signer), nil
	case SSHAuthPassword:
		if cfg.Password == "" {
			return nil, fmt.Errorf("dataconn: ssh tunnel: password is required for password auth")
		}
		return ssh.Password(cfg.Password), nil
	default:
		return nil, fmt.Errorf("dataconn: ssh tunnel: no usable auth method (need a private key or password)")
	}
}

// parseSSHPrivateKey parses a PEM private key, mapping a passphrase-protected
// key to ErrSSHPassphraseKey (same convention as git_remote_credential).
func parseSSHPrivateKey(pem []byte) (ssh.Signer, error) {
	pem = []byte(strings.TrimSpace(string(pem)))
	if len(pem) == 0 {
		return nil, fmt.Errorf("dataconn: ssh tunnel: private key is empty")
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		if _, ok := err.(*ssh.PassphraseMissingError); ok {
			return nil, ErrSSHPassphraseKey
		}
		return nil, fmt.Errorf("dataconn: ssh tunnel: parse private key: %w", err)
	}
	return signer, nil
}

// hostKeyCallback returns a strict fingerprint-pinning callback when pinned is
// non-empty, else an accept-any callback (documented trade-off).
func hostKeyCallback(pinned string) (ssh.HostKeyCallback, error) {
	pinned = strings.TrimSpace(pinned)
	if pinned == "" {
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // opt-in: empty host_key means accept-any, documented on SSHConfig.HostKey
	}
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)
		if got == pinned {
			return nil
		}
		return fmt.Errorf("dataconn: ssh tunnel: host key mismatch: server presented %s, expected %s", got, pinned)
	}, nil
}
