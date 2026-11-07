package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	chromeStartTimeout = 20 * time.Second
	cdpCallTimeout     = 30 * time.Second
)

// newCDPDriver launches a Chrome instance with the account's profile
// directory and attaches to its first page over the Chrome DevTools Protocol.
// Each account gets its own process and profile: cookies, localStorage and the
// fingerprint never cross accounts.
func newCDPDriver(ctx context.Context, profileDir string, cfg LocalConfig) (Driver, error) {
	chrome, err := findChrome(cfg.ChromePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return nil, fmt.Errorf("opsbot/browser: create chrome profile: %w", err)
	}
	// A stale port file from a previous run would make us connect to a dead
	// browser. Chrome rewrites it during startup.
	portFile := filepath.Join(profileDir, "DevToolsActivePort")
	_ = os.Remove(portFile)

	stderr := &limitedBuffer{limit: 8192}
	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + profileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-sync",
		"--disable-dev-shm-usage",
		"--disable-gpu",
		"--mute-audio",
		"--password-store=basic",
		"--use-mock-keychain",
		"--window-size=1366,900",
	}
	if cfg.Headless {
		args = append(args, "--headless=new")
	}

	cmd := exec.Command(chrome, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("opsbot/browser: start chrome %q: %w", chrome, err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	navTimeout := cfg.NavigateTimeout
	if navTimeout <= 0 {
		navTimeout = defaultNavigateTimeout
	}
	d := &cdpDriver{cmd: cmd, exited: exited, stderr: stderr, profileDir: profileDir, navigateTimeout: navTimeout}
	startCtx, cancel := context.WithTimeout(ctx, chromeStartTimeout)
	defer cancel()

	port, wsPath, err := waitDevToolsPort(startCtx, portFile, exited, stderr)
	if err != nil {
		d.kill()
		return nil, err
	}
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d%s", port, wsPath)
	conn, err := dialCDP(startCtx, wsURL)
	if err != nil {
		d.kill()
		return nil, fmt.Errorf("opsbot/browser: connect to chrome: %w", err)
	}
	d.conn = conn

	if err := d.openPage(startCtx); err != nil {
		d.kill()
		return nil, err
	}
	return d, nil
}

// findChrome resolves the browser binary: explicit config, then
// NIUNIU_CHROME_PATH, then well-known install paths, then PATH.
func findChrome(explicit string) (string, error) {
	if p := strings.TrimSpace(explicit); p != "" {
		return p, nil
	}
	if p := strings.TrimSpace(os.Getenv("NIUNIU_CHROME_PATH")); p != "" {
		return p, nil
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		candidates = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		}
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	default:
		candidates = []string{
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
		}
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "msedge"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", &ConfigError{
		Kind:   BackendLocal,
		Reason: "no Chrome/Chromium executable found",
		Hint:   "install Chrome or set LocalConfig.ChromePath / NIUNIU_CHROME_PATH",
	}
}

func waitDevToolsPort(ctx context.Context, file string, exited <-chan error, stderr *limitedBuffer) (int, string, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(file)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= 2 {
				port, perr := strconv.Atoi(strings.TrimSpace(lines[0]))
				path := strings.TrimSpace(lines[1])
				if perr == nil && port > 0 && path != "" {
					return port, path, nil
				}
			}
		}
		select {
		case err := <-exited:
			return 0, "", fmt.Errorf("opsbot/browser: chrome exited before DevTools was ready: %v%s", err, stderrTail(stderr))
		case <-ctx.Done():
			return 0, "", fmt.Errorf("opsbot/browser: waiting for chrome DevTools port: %w%s", ctx.Err(), stderrTail(stderr))
		case <-ticker.C:
		}
	}
}

func stderrTail(b *limitedBuffer) string {
	if b == nil {
		return ""
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return ""
	}
	return " (chrome stderr: " + s + ")"
}

type cdpDriver struct {
	conn       *cdpConn
	cmd        *exec.Cmd
	exited     chan error
	stderr     *limitedBuffer
	profileDir string
	targetID   string
	sessionID  string

	navigateTimeout time.Duration

	closeOnce sync.Once
	closeErr  error
}

func (d *cdpDriver) openPage(ctx context.Context) error {
	raw, err := d.conn.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"})
	if err != nil {
		return err
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return fmt.Errorf("opsbot/browser: decode target: %w", err)
	}
	if created.TargetID == "" {
		return errors.New("opsbot/browser: chrome returned an empty target id")
	}
	d.targetID = created.TargetID

	raw, err = d.conn.call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": created.TargetID,
		"flatten":  true,
	})
	if err != nil {
		return err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &attached); err != nil {
		return fmt.Errorf("opsbot/browser: decode session: %w", err)
	}
	if attached.SessionID == "" {
		return errors.New("opsbot/browser: chrome returned an empty session id")
	}
	d.sessionID = attached.SessionID

	for _, method := range []string{"Page.enable", "Runtime.enable", "DOM.enable"} {
		if _, err := d.conn.call(ctx, d.sessionID, method, nil); err != nil {
			return err
		}
	}
	return nil
}

func (d *cdpDriver) Goto(ctx context.Context, url string) error {
	raw, err := d.conn.call(ctx, d.sessionID, "Page.navigate", map[string]any{"url": url})
	if err != nil {
		return err
	}
	var nav struct {
		ErrorText string `json:"errorText"`
	}
	if err := json.Unmarshal(raw, &nav); err != nil {
		return fmt.Errorf("opsbot/browser: decode navigation: %w", err)
	}
	if nav.ErrorText != "" {
		return fmt.Errorf("opsbot/browser: navigate %s: %s", url, nav.ErrorText)
	}
	return d.awaitReady(ctx)
}

func (d *cdpDriver) awaitReady(ctx context.Context) error {
	deadline := time.Now().Add(d.navigateTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := d.evalString(ctx, "document.readyState")
		if err == nil && (state == "complete" || state == "interactive") {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: page did not finish loading", ErrTimeout)
		}
		if err := sleepCtx(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}

func (d *cdpDriver) CurrentURL(ctx context.Context) (string, error) {
	return d.evalString(ctx, "location.href")
}

func (d *cdpDriver) Count(ctx context.Context, selector string) (int, error) {
	n, err := d.evalInt(ctx, fmt.Sprintf("document.querySelectorAll(%s).length", jsString(selector)))
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (d *cdpDriver) Wait(ctx context.Context, target Target, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := d.Count(ctx, target.Selector)
		if err == nil && n > target.Index {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s[%d]", ErrSelectorNotFound, target.Selector, target.Index)
		}
		if err := sleepCtx(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
}

func (d *cdpDriver) Click(ctx context.Context, target Target, timeout time.Duration) error {
	if err := d.Wait(ctx, target, timeout); err != nil {
		return err
	}
	script := fmt.Sprintf(`(() => {
		const el = document.querySelectorAll(%s)[%d];
		if (!el) return null;
		el.scrollIntoView({block: 'center', inline: 'center'});
		const r = el.getBoundingClientRect();
		return {x: r.left + r.width / 2, y: r.top + r.height / 2, w: r.width, h: r.height};
	})()`, jsString(target.Selector), target.Index)
	raw, err := d.eval(ctx, script)
	if err != nil {
		return err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return fmt.Errorf("%w: %s[%d]", ErrSelectorNotFound, target.Selector, target.Index)
	}
	var rect struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		W float64 `json:"w"`
		H float64 `json:"h"`
	}
	if err := json.Unmarshal(raw, &rect); err != nil {
		return fmt.Errorf("opsbot/browser: decode element rect: %w", err)
	}
	for _, typ := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		if _, err := d.conn.call(ctx, d.sessionID, "Input.dispatchMouseEvent", map[string]any{
			"type":       typ,
			"x":          rect.X,
			"y":          rect.Y,
			"button":     "left",
			"clickCount": 1,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (d *cdpDriver) Fill(ctx context.Context, target Target, value string, timeout time.Duration) error {
	if err := d.Wait(ctx, target, timeout); err != nil {
		return err
	}
	script := fmt.Sprintf(`(() => {
		const el = document.querySelectorAll(%s)[%d];
		if (!el) return false;
		el.focus();
		if (el.isContentEditable) {
			el.textContent = %s;
		} else {
			const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype
				: el instanceof HTMLSelectElement ? HTMLSelectElement.prototype
				: HTMLInputElement.prototype;
			const desc = Object.getOwnPropertyDescriptor(proto, 'value');
			if (desc && desc.set) { desc.set.call(el, %s); } else { el.value = %s; }
		}
		el.dispatchEvent(new Event('input', {bubbles: true}));
		el.dispatchEvent(new Event('change', {bubbles: true}));
		return true;
	})()`, jsString(target.Selector), target.Index, jsString(value), jsString(value), jsString(value))
	ok, err := d.evalBool(ctx, script)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s[%d]", ErrSelectorNotFound, target.Selector, target.Index)
	}
	return nil
}

func (d *cdpDriver) Upload(ctx context.Context, target Target, files []string, timeout time.Duration) error {
	if err := d.Wait(ctx, target, timeout); err != nil {
		return err
	}
	raw, err := d.conn.call(ctx, d.sessionID, "DOM.getDocument", map[string]any{"depth": 0})
	if err != nil {
		return err
	}
	var doc struct {
		Root struct {
			NodeID int64 `json:"nodeId"`
		} `json:"root"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("opsbot/browser: decode document: %w", err)
	}
	raw, err = d.conn.call(ctx, d.sessionID, "DOM.querySelectorAll", map[string]any{
		"nodeId":   doc.Root.NodeID,
		"selector": target.Selector,
	})
	if err != nil {
		return err
	}
	var nodes struct {
		NodeIDs []int64 `json:"nodeIds"`
	}
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return fmt.Errorf("opsbot/browser: decode matched nodes: %w", err)
	}
	if target.Index >= len(nodes.NodeIDs) {
		return fmt.Errorf("%w: %s[%d]", ErrSelectorNotFound, target.Selector, target.Index)
	}
	_, err = d.conn.call(ctx, d.sessionID, "DOM.setFileInputFiles", map[string]any{
		"files":  files,
		"nodeId": nodes.NodeIDs[target.Index],
	})
	return err
}

func (d *cdpDriver) Extract(ctx context.Context, target Target, attr string) (string, error) {
	script := fmt.Sprintf(`(() => {
		const el = document.querySelectorAll(%s)[%d];
		if (!el) return null;
		if (%s !== '') return el.getAttribute(%s);
		if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT') return el.value;
		return el.innerText !== undefined ? el.innerText : el.textContent;
	})()`, jsString(target.Selector), target.Index, jsString(attr), jsString(attr))
	raw, err := d.eval(ctx, script)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "", fmt.Errorf("%w: %s[%d]", ErrSelectorNotFound, target.Selector, target.Index)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("opsbot/browser: decode extracted value: %w", err)
	}
	return s, nil
}

func (d *cdpDriver) Screenshot(ctx context.Context) ([]byte, error) {
	raw, err := d.conn.call(ctx, d.sessionID, "Page.captureScreenshot", map[string]any{
		"format": "png",
	})
	if err != nil {
		return nil, err
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &shot); err != nil {
		return nil, fmt.Errorf("opsbot/browser: decode screenshot: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil {
		return nil, fmt.Errorf("opsbot/browser: decode screenshot bytes: %w", err)
	}
	return data, nil
}

func (d *cdpDriver) Close(ctx context.Context) error {
	d.closeOnce.Do(func() {
		closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if d.conn != nil {
			// Browser.close flushes the profile (cookies/localStorage) before
			// exiting; killing the process first would lose the login state.
			_, _ = d.conn.call(closeCtx, "", "Browser.close", nil)
			d.conn.close()
		}
		select {
		case <-d.exited:
		case <-closeCtx.Done():
			d.kill()
		}
		d.closeErr = nil
	})
	return d.closeErr
}

func (d *cdpDriver) kill() {
	if d.conn != nil {
		d.conn.close()
	}
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
	}
	select {
	case <-d.exited:
	case <-time.After(2 * time.Second):
	}
}

// jsString renders a Go string as a JSON/JavaScript string literal. Every
// value interpolated into a CDP script goes through this, so selectors and
// fill values cannot break out of their literal.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (d *cdpDriver) evalString(ctx context.Context, expr string) (string, error) {
	raw, err := d.eval(ctx, expr)
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("opsbot/browser: decode string: %w", err)
	}
	return s, nil
}

func (d *cdpDriver) evalInt(ctx context.Context, expr string) (int, error) {
	raw, err := d.eval(ctx, expr)
	if err != nil {
		return 0, err
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("opsbot/browser: decode number: %w", err)
	}
	return n, nil
}

func (d *cdpDriver) evalBool(ctx context.Context, expr string) (bool, error) {
	raw, err := d.eval(ctx, expr)
	if err != nil {
		return false, err
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, fmt.Errorf("opsbot/browser: decode bool: %w", err)
	}
	return b, nil
}

type cdpEvalResponse struct {
	Result struct {
		Type        string          `json:"type"`
		Subtype     string          `json:"subtype"`
		Value       json.RawMessage `json:"value"`
		Description string          `json:"description"`
	} `json:"result"`
	ExceptionDetails *struct {
		Text      string `json:"text"`
		Exception *struct {
			Description string `json:"description"`
		} `json:"exception"`
	} `json:"exceptionDetails"`
}

func (d *cdpDriver) eval(ctx context.Context, expr string) (json.RawMessage, error) {
	raw, err := d.conn.call(ctx, d.sessionID, "Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
		"awaitPromise":  true,
		"userGesture":   true,
	})
	if err != nil {
		return nil, err
	}
	var res cdpEvalResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("opsbot/browser: decode evaluate response: %w", err)
	}
	if res.ExceptionDetails != nil {
		msg := res.ExceptionDetails.Text
		if res.ExceptionDetails.Exception != nil && res.ExceptionDetails.Exception.Description != "" {
			msg = res.ExceptionDetails.Exception.Description
		}
		return nil, fmt.Errorf("opsbot/browser: page script failed: %s", msg)
	}
	if res.Result.Subtype == "error" {
		return nil, fmt.Errorf("opsbot/browser: page script returned %s", res.Result.Description)
	}
	if len(res.Result.Value) == 0 {
		return json.RawMessage("null"), nil
	}
	return res.Result.Value, nil
}

// ---- CDP websocket client ----

type cdpConn struct {
	conn *websocket.Conn

	writeMu sync.Mutex
	nextID  int64

	mu      sync.Mutex
	pending map[int64]chan cdpCallResult

	closed    chan struct{}
	closeOnce sync.Once
	readErr   error
}

type cdpCallResult struct {
	result json.RawMessage
	err    error
}

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func dialCDP(ctx context.Context, wsURL string) (*cdpConn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	c := &cdpConn{
		conn:    conn,
		pending: map[int64]chan cdpCallResult{},
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func (c *cdpConn) readLoop() {
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			c.closeWithError(err)
			return
		}
		var msg cdpMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.ID == 0 {
			continue // events are not needed: the driver polls state instead
		}
		c.mu.Lock()
		ch := c.pending[msg.ID]
		delete(c.pending, msg.ID)
		c.mu.Unlock()
		if ch == nil {
			continue
		}
		if msg.Error != nil {
			ch <- cdpCallResult{err: fmt.Errorf("cdp: %s (code %d)", msg.Error.Message, msg.Error.Code)}
			continue
		}
		ch <- cdpCallResult{result: msg.Result}
	}
}

func (c *cdpConn) closeWithError(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.readErr = err
		pending := c.pending
		c.pending = map[int64]chan cdpCallResult{}
		c.mu.Unlock()
		close(c.closed)
		for _, ch := range pending {
			ch <- cdpCallResult{err: err}
		}
	})
}

func (c *cdpConn) close() {
	_ = c.conn.Close()
	c.closeWithError(io.EOF)
}

func (c *cdpConn) call(ctx context.Context, sessionID, method string, params any) (json.RawMessage, error) {
	var raw json.RawMessage
	if params == nil {
		raw = json.RawMessage(`{}`)
	} else {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("opsbot/browser: encode %s params: %w", method, err)
		}
		raw = b
	}
	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan cdpCallResult, 1)

	c.mu.Lock()
	select {
	case <-c.closed:
		err := c.readErr
		c.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return nil, fmt.Errorf("opsbot/browser: CDP connection closed: %w", err)
	default:
	}
	c.pending[id] = ch
	c.mu.Unlock()

	msg := cdpMessage{ID: id, Method: method, Params: raw, SessionID: sessionID}
	b, err := json.Marshal(msg)
	if err != nil {
		c.forget(id)
		return nil, err
	}
	c.writeMu.Lock()
	err = c.conn.WriteMessage(websocket.TextMessage, b)
	c.writeMu.Unlock()
	if err != nil {
		c.forget(id)
		return nil, fmt.Errorf("opsbot/browser: send %s: %w", method, err)
	}

	callCtx, cancel := context.WithTimeout(ctx, cdpCallTimeout)
	defer cancel()
	select {
	case res := <-ch:
		if res.err != nil {
			return nil, fmt.Errorf("opsbot/browser: %s: %w", method, res.err)
		}
		return res.result, nil
	case <-ctx.Done():
		c.forget(id)
		return nil, ctx.Err()
	case <-callCtx.Done():
		c.forget(id)
		return nil, fmt.Errorf("%w: CDP %s timed out", ErrTimeout, method)
	}
}

func (c *cdpConn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// limitedBuffer keeps the first limit bytes written to it. Chrome's startup
// stderr is the most useful part when the launch fails.
type limitedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() < b.limit {
		room := b.limit - b.buf.Len()
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
