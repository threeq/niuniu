package localrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// errUnauthorized flags a reverse-channel dial that the server rejected with 401
// — the reused access token has expired. The client surfaces this via
// OnAuthExpired so the shell can force the connection webview (the sole owner of
// the rotating refresh token) to mint a fresh token, instead of spinning
// forever on a token that will never become valid again.
var errUnauthorized = errors.New("reverse channel unauthorized (401)")

// client.go is the reverse-channel client (#468/#469/#476/#477). It maintains a
// long-lived WS to the bound server's /ws/workspaces/:id/local-runner/runner,
// registers presence just by connecting (server RegisterRunner injects the MCP
// tool set), and on every server request runs it through the gateway, executes
// inside the bound directory, and streams the result back. On disconnect it
// reports status (never silent — #476) and reconnects with backoff until Stop.

// Status is a runner lifecycle state reported via OnStatus. The string values
// intentionally match desktop/internal/runnerstore so the app maps them 1:1.
type Status string

const (
	StatusConnecting Status = "connecting"
	StatusActive     Status = "active"
	StatusError      Status = "error"
	StatusStopped    Status = "stopped"
)

// heartbeatInterval keeps the server's 35s read deadline alive between commands.
const heartbeatInterval = 15 * time.Second

// initialBackoff / maxBackoff bound the reconnect delay.
const (
	initialBackoff = 1 * time.Second
	maxBackoff     = 30 * time.Second
)

// jitterFraction spreads each reconnect wait by ±50%. A single desktop can hold
// many reverse channels (one per started runner); when the server restarts they
// all drop at the same instant and, without jitter, would retry in lockstep and
// hammer the server in synchronized waves ("thundering herd"). Scaling each wait
// by a random factor in [0.5, 1.5) smears the retries across the interval.
const jitterFraction = 0.5

// jittered scales d by a random factor in [1-jitterFraction, 1+jitterFraction).
func jittered(d time.Duration) time.Duration {
	factor := 1 + (rand.Float64()*2-1)*jitterFraction
	return time.Duration(float64(d) * factor)
}

// wsConn is the subset of *websocket.Conn the client uses, so tests can inject a
// fake without a network.
type wsConn interface {
	Read(ctx context.Context) (websocket.MessageType, []byte, error)
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
	// Ping sends a WS ping and waits for the matching pong (bounded by ctx). It is
	// how the client detects a HALF-OPEN link — the read alone would block forever
	// when the server has gone away without a TCP FIN/RST reaching us, so the
	// runner would never reconnect while the server already dropped it.
	Ping(ctx context.Context) error
	Close(code websocket.StatusCode, reason string) error
}

// dialFunc opens a reverse-channel connection; overridable in tests.
type dialFunc func(ctx context.Context, url string, header http.Header) (wsConn, error)

func realDial(ctx context.Context, url string, header http.Header) (wsConn, error) {
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		// A 401 handshake means the access token expired; tag it so the client
		// can trigger a token refresh rather than reconnecting with the same dead
		// credential. coder/websocket returns the response on a non-101 status.
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("%w: %v", errUnauthorized, err)
		}
		return nil, err
	}
	// Server may stream large exec responses back; raise the read limit well
	// above the default 32KiB so an incoming request frame is never rejected.
	c.SetReadLimit(-1)
	return c, nil
}

// Config assembles a Client.
type Config struct {
	BaseURL     string // http(s)://host:port of the bound server (no trailing slash needed)
	WorkspaceID string // remote workspace id
	Token       string // bearer JWT reused from the desktop↔remote connection
	Dir         string // bound local directory (the gateway's boundary root)

	Gateway *Gateway // required — the security bottom line
	Syncer  Syncer   // optional — nil ⇒ sync requests report "sync unavailable"

	// OnStatus reports lifecycle transitions (connect/disconnect/error) so the
	// registry + UI never show a silently-dead runner (#476).
	OnStatus func(status Status, note string)
	// OnLog mirrors every streamed log line to the local registry / manager UI.
	OnLog func(level, text string)
	// OnAuthExpired fires when a dial is rejected with 401 (the reused access
	// token expired). The shell wires this to force the connection webview to
	// refresh + re-post a fresh token (see runnermanager). Optional; nil ⇒ the
	// client just keeps retrying and relies on the webview's proactive refresh.
	OnAuthExpired func()

	// test hooks
	dial  dialFunc
	nowMS func() int64
}

// Client is one bound runner's reverse channel.
type Client struct {
	cfg    Config
	dial   dialFunc
	nowMS  func() int64
	cancel context.CancelFunc
	done   chan struct{}

	// token is the bearer JWT used on (re)dial. It is refreshed live via SetToken
	// because the SPA rotates its access token; a stale snapshot would make every
	// reconnect after expiry fail the handshake with 401.
	tokenMu sync.Mutex
	token   string
}

// New builds a Client. Gateway must be non-nil.
func New(cfg Config) *Client {
	c := &Client{cfg: cfg, dial: cfg.dial, nowMS: cfg.nowMS, token: cfg.Token}
	if c.dial == nil {
		c.dial = realDial
	}
	if c.nowMS == nil {
		c.nowMS = func() int64 { return time.Now().UnixMilli() }
	}
	return c
}

// SetToken updates the bearer token used on the next (re)dial. Safe to call from
// any goroutine; a no-op when unchanged. The live socket is not torn down — the
// fresh token simply applies whenever the channel next reconnects.
func (c *Client) SetToken(token string) {
	if token == "" {
		return
	}
	c.tokenMu.Lock()
	c.token = token
	c.tokenMu.Unlock()
}

func (c *Client) currentToken() string {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	return c.token
}

// Start launches the reconnect loop in the background. Stop ends it.
func (c *Client) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		c.runLoop(ctx)
	}()
}

// Stop cancels the loop and waits for it to unwind.
func (c *Client) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.done != nil {
		<-c.done
	}
	c.status(StatusStopped, "本地执行器已停止 / runner stopped")
}

// wsURL derives the reverse-channel WS URL from the base HTTP URL.
func (c *Client) wsURL() string {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	base = strings.Replace(base, "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	return fmt.Sprintf("%s/ws/workspaces/%s/local-runner/runner", base, c.cfg.WorkspaceID)
}

// runLoop connects and serves, reconnecting with backoff until ctx is cancelled.
func (c *Client) runLoop(ctx context.Context) {
	backoff := initialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		c.status(StatusConnecting, "正在连接反向通道 / connecting reverse channel")
		connected, err := c.connectAndServe(ctx)
		if ctx.Err() != nil {
			return
		}
		// A previously-live connection that dropped reconnects fast (reset to the
		// initial backoff); only repeated DIAL failures (server unreachable)
		// escalate toward maxBackoff. This keeps the offline gap short on a normal
		// network blip instead of creeping up to 30s.
		if connected {
			backoff = initialBackoff
		}
		// Disconnected while we still want to be up — report and back off.
		note := "连接断开，正在重连 / disconnected, reconnecting"
		if err != nil {
			note = "连接断开：" + err.Error() + " / reconnecting"
		}
		c.status(StatusError, note)

		select {
		case <-ctx.Done():
			return
		case <-time.After(jittered(backoff)):
		}
		if !connected {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// connectAndServe dials once and serves until the connection drops or ctx ends.
// Returns connected=true once the dial succeeded (so the caller can reset the
// reconnect backoff for a dropped-but-previously-live connection, keeping the
// offline gap short instead of escalating toward maxBackoff).
func (c *Client) connectAndServe(ctx context.Context) (connected bool, err error) {
	header := http.Header{}
	if tok := c.currentToken(); tok != "" {
		header.Set("Authorization", "Bearer "+tok)
	}
	conn, err := c.dial(ctx, c.wsURL(), header)
	if err != nil {
		// 401 ⇒ the token is dead; ask the shell to refresh it (the webview owns
		// the rotating refresh token). Best-effort: reconnect backoff continues
		// regardless, and the fresh token applies on the next dial via SetToken.
		if errors.Is(err, errUnauthorized) && c.cfg.OnAuthExpired != nil {
			c.cfg.OnAuthExpired()
		}
		return false, err
	}

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	c.status(StatusActive, "反向通道已连接 / reverse channel connected")

	// Single writer goroutine serializes all frames (log/exit/response/pong).
	send := make(chan []byte, 128)
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		for {
			select {
			case <-connCtx.Done():
				return
			case frame := <-send:
				wctx, wcancel := context.WithTimeout(connCtx, 10*time.Second)
				werr := conn.Write(wctx, websocket.MessageText, frame)
				wcancel()
				if werr != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Heartbeat: two roles on each tick.
	//  1. Send an app-level pong (a TEXT data frame) to reset the SERVER's 35s
	//     read deadline. A WS-level ping would be auto-answered by the server's
	//     gorilla stack WITHOUT surfacing from ReadMessage, so it would NOT keep
	//     the server's deadline alive — only a data frame does.
	//  2. Send a WS ping and wait (bounded) for the pong to prove the link is
	//     still two-way. On a HALF-OPEN connection (server dropped us on its read
	//     deadline but no FIN/RST reached this side) the read below would block
	//     forever and the runner would never reconnect while the server shows it
	//     offline. A failed ping tears the connection down so runLoop reconnects.
	go func() {
		t := time.NewTicker(heartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-connCtx.Done():
				return
			case <-t.C:
				c.enqueue(connCtx, send, pongFrame{Type: "pong"})
				pctx, pcancel := context.WithTimeout(connCtx, heartbeatInterval)
				perr := conn.Ping(pctx)
				pcancel()
				if perr != nil && connCtx.Err() == nil {
					cancel() // link is dead — force a reconnect
					return
				}
			}
		}
	}()

	// Read loop (runs on this goroutine). Each request is dispatched to its own
	// goroutine so a long exec doesn't stall reads or the heartbeat.
	var handlersWG sync.WaitGroup
	for {
		_, data, rerr := conn.Read(connCtx)
		if rerr != nil {
			cancel()
			handlersWG.Wait()
			writerWG.Wait()
			if ctx.Err() != nil {
				return true, nil
			}
			return true, rerr
		}
		var req requestFrame
		if json.Unmarshal(data, &req) != nil || req.Type != "request" {
			continue
		}
		handlersWG.Add(1)
		go func(req requestFrame) {
			defer handlersWG.Done()
			c.handle(connCtx, send, req)
		}(req)
	}
}

// handle dispatches one request to the gateway + executor/reader/syncer and
// streams the reply frames back through send.
func (c *Client) handle(ctx context.Context, send chan []byte, req requestFrame) {
	switch req.Kind {
	case kindExec:
		c.handleExec(ctx, send, req)
	case kindRead:
		c.handleRead(ctx, send, req)
	case kindSync:
		c.handleSync(ctx, send, req)
	default:
		c.enqueue(ctx, send, responseFrame{Type: "response", ID: req.ID, OK: false, Error: "unknown request kind: " + req.Kind})
	}
}

func (c *Client) handleExec(ctx context.Context, send chan []byte, req requestFrame) {
	allowed, reason := c.cfg.Gateway.AuthorizeCommand(req.Command, c.nowMS)
	if !allowed {
		c.log(send, ctx, levelSystem, "命令被拦截 / command blocked: "+reason)
		c.enqueue(ctx, send, responseFrame{Type: "response", ID: req.ID, OK: false, Exit: -1, Error: reason})
		return
	}

	// Sync-before-exec (#472): mirror the remote worktree first. Best-effort —
	// a failure is logged but does not block the (already-authorized) command.
	if c.cfg.Syncer != nil {
		if summary, serr := c.cfg.Syncer.Sync(ctx); serr != nil {
			c.log(send, ctx, levelSystem, "代码同步警告 / sync warning: "+summary+" ("+serr.Error()+")")
		} else if summary != "" {
			c.log(send, ctx, levelSystem, "代码同步 / sync: "+summary)
		}
	}

	res := Execute(ctx, req.Command, c.cfg.Gateway.Dir(), func(level, text string) {
		c.log(send, ctx, level, text)
	})

	c.enqueue(ctx, send, exitFrame{Type: "exit", Code: res.Exit})

	resp := responseFrame{Type: "response", ID: req.ID, Stdout: res.Stdout, Stderr: res.Stderr, Exit: res.Exit, OK: res.OK}
	if res.Err != nil {
		resp.Error = res.Err.Error()
	}
	c.enqueue(ctx, send, resp)
}

func (c *Client) handleRead(ctx context.Context, send chan []byte, req requestFrame) {
	content, err := ReadFile(c.cfg.Gateway, req.Path, c.nowMS)
	if err != nil {
		c.enqueue(ctx, send, responseFrame{Type: "response", ID: req.ID, OK: false, Error: err.Error()})
		return
	}
	c.enqueue(ctx, send, responseFrame{Type: "response", ID: req.ID, OK: true, Content: content})
}

func (c *Client) handleSync(ctx context.Context, send chan []byte, req requestFrame) {
	if c.cfg.Syncer == nil {
		c.enqueue(ctx, send, responseFrame{Type: "response", ID: req.ID, OK: false, Error: "sync unavailable: bound directory is not a git mirror"})
		return
	}
	summary, err := c.cfg.Syncer.Sync(ctx)
	if err != nil {
		c.log(send, ctx, levelSystem, "代码同步失败 / sync failed: "+summary)
		c.enqueue(ctx, send, responseFrame{Type: "response", ID: req.ID, OK: false, Error: err.Error(), Content: summary})
		return
	}
	c.log(send, ctx, levelSystem, "代码同步 / sync: "+summary)
	c.enqueue(ctx, send, responseFrame{Type: "response", ID: req.ID, OK: true, Content: summary})
}

// log streams one log frame to the server AND mirrors it to the local registry.
func (c *Client) log(send chan []byte, ctx context.Context, level, text string) {
	c.enqueue(ctx, send, logFrame{Type: "log", Level: level, Text: text})
	if c.cfg.OnLog != nil {
		c.cfg.OnLog(level, text)
	}
}

// enqueue marshals frame and hands it to the writer, dropping it if the
// connection is tearing down (ctx cancelled) rather than blocking a handler.
func (c *Client) enqueue(ctx context.Context, send chan []byte, frame any) {
	data, err := json.Marshal(frame)
	if err != nil {
		return
	}
	select {
	case send <- data:
	case <-ctx.Done():
	}
}

func (c *Client) status(s Status, note string) {
	if c.cfg.OnStatus != nil {
		c.cfg.OnStatus(s, note)
	}
}
