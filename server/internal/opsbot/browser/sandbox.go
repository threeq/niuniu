package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Sandbox runs a recipe end to end against local fixture HTML served over
// HTTP. It never reaches a real platform: every allowed host is rewritten to
// the in-process server, and the server only serves the declared fixtures.
type Sandbox struct {
	// Root is the Pack version directory fixture paths are relative to.
	Root string
	// AllowedHosts is the Pack manifest's allowed_hosts. When empty the sandbox
	// derives them from the recipe's goto steps; the sandbox is offline, so
	// this only enables exercising the recipe, it is not an egress boundary.
	AllowedHosts []string
	// Values fill {{placeholders}} in the recipe.
	Values map[string]string
	// StepTimeout overrides the runner's default step timeout.
	StepTimeout time.Duration
}

// Run executes the recipe against its fixtures. A nil fixtures argument falls
// back to recipe.Fixtures. Every extract must yield a non-empty value and every
// screenshot must produce bytes, so a passing Run is a real recipe check.
func (s *Sandbox) Run(ctx context.Context, recipe Recipe, fixtures []string) error {
	if strings.TrimSpace(s.Root) == "" {
		return fmt.Errorf("%w: sandbox root is empty", ErrInvalidRecipe)
	}
	if len(fixtures) == 0 {
		fixtures = recipe.Fixtures
	}
	if len(fixtures) == 0 {
		return fmt.Errorf("%w: recipe %q declares no sandbox fixture", ErrInvalidRecipe, recipe.Name)
	}

	files := make(map[string][]byte, len(fixtures)*2)
	primary := ""
	for _, fixture := range fixtures {
		if !filepath.IsLocal(fixture) {
			return fmt.Errorf("%w: fixture %q escapes the pack root", ErrInvalidRecipe, fixture)
		}
		clean := filepath.ToSlash(filepath.Clean(fixture))
		data, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(clean)))
		if err != nil {
			return fmt.Errorf("opsbot/browser: read fixture %q: %w", fixture, err)
		}
		files[clean] = data
		files[path.Base(clean)] = data
		if primary == "" {
			primary = clean
		}
	}

	server := httptest.NewServer(fixtureHandler(files, primary))
	defer server.Close()

	allowed := s.allowedHosts(recipe)
	hostMap := make(map[string]string, len(allowed))
	for _, host := range allowed {
		hostMap[strings.ToLower(strings.TrimSpace(host))] = server.URL
	}

	runner := &RecipeRunner{
		AllowedHosts:       allowed,
		DefaultStepTimeout: s.StepTimeout,
		Open: func(context.Context, Input) (Page, error) {
			return newDOMPage(hostMap), nil
		},
	}

	out, err := runner.Run(ctx, recipe, Input{Values: s.Values})
	if err != nil {
		return err
	}
	return verifyRecipeOutput(recipe, out)
}

func (s *Sandbox) allowedHosts(recipe Recipe) []string {
	if len(s.AllowedHosts) > 0 {
		return s.AllowedHosts
	}
	seen := make(map[string]bool)
	var hosts []string
	for _, step := range recipe.Steps {
		if step.Action != "goto" {
			continue
		}
		u, err := url.Parse(step.Value)
		if err != nil || u.Hostname() == "" {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	return hosts
}

// verifyRecipeOutput enforces the sandbox's own contract: a recipe that
// silently extracts nothing is not a passing run.
func verifyRecipeOutput(recipe Recipe, out Output) error {
	for i, step := range recipe.Steps {
		switch step.Action {
		case "extract":
			if strings.TrimSpace(step.Value) == "" {
				return fmt.Errorf("%w: step %d extract has no output name", ErrInvalidRecipe, i+1)
			}
			if strings.TrimSpace(out.Values[step.Value]) == "" {
				return fmt.Errorf("opsbot/browser: sandbox step %d extracted an empty %q", i+1, step.Value)
			}
		case "screenshot":
			if strings.TrimSpace(step.Value) == "" {
				continue
			}
			if len(out.Screenshots[step.Value]) == 0 {
				return fmt.Errorf("opsbot/browser: sandbox step %d produced an empty screenshot %q", i+1, step.Value)
			}
		}
	}
	return nil
}

func fixtureHandler(files map[string][]byte, primary string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		data, ok := files[key]
		if !ok {
			data, ok = files[path.Base(key)]
		}
		if !ok {
			data = files[primary]
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
}

// domPage is a Page backed by a static HTML document fetched over HTTP.
// Interactions are recorded on the DOM; nothing is evaluated as JavaScript.
type domPage struct {
	client  *http.Client
	hostMap map[string]string
	doc     *html.Node
	raw     []byte
}

func newDOMPage(hostMap map[string]string) *domPage {
	return &domPage{
		client:  &http.Client{Timeout: 30 * time.Second},
		hostMap: hostMap,
	}
}

func (p *domPage) Goto(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: parse %q: %v", ErrInvalidRecipe, rawURL, err)
	}
	base, ok := p.hostMap[strings.ToLower(u.Hostname())]
	if !ok {
		return fmt.Errorf("%w: %q", ErrHostNotAllowed, u.Hostname())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+u.RequestURI(), nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opsbot/browser: fixture for %q returned status %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("opsbot/browser: parse fixture: %w", err)
	}
	p.doc = doc
	p.raw = body
	return nil
}

func (p *domPage) Find(_ context.Context, selectors []string) (string, error) {
	if p.doc == nil {
		return "", fmt.Errorf("%w: no page loaded", ErrInvalidRecipe)
	}
	for _, selector := range selectors {
		if strings.TrimSpace(selector) == "" {
			continue
		}
		parts, err := parseSelector(selector)
		if err != nil {
			return "", fmt.Errorf("%w: selector %q: %v", ErrInvalidRecipe, selector, err)
		}
		if len(p.findAll(parts)) > 0 {
			return selector, nil
		}
	}
	return "", ErrSelectorNotFound
}

func (p *domPage) WaitAny(ctx context.Context, selectors []string) (string, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		selector, err := p.Find(ctx, selectors)
		if err == nil {
			return selector, nil
		}
		if !isSelectorMiss(err) {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func isSelectorMiss(err error) bool {
	return errors.Is(err, ErrSelectorNotFound)
}

func (p *domPage) Click(ctx context.Context, selector string) error {
	_, err := p.first(selector)
	return err
}

func (p *domPage) Fill(_ context.Context, selector, value string) error {
	node, err := p.first(selector)
	if err != nil {
		return err
	}
	switch strings.ToLower(node.Data) {
	case "input", "textarea":
		setAttr(node, "value", value)
	default:
		setText(node, value)
	}
	return nil
}

func (p *domPage) Upload(_ context.Context, selector string, files []string) error {
	node, err := p.first(selector)
	if err != nil {
		return err
	}
	setAttr(node, "data-sandbox-files", strings.Join(files, ","))
	return nil
}

func (p *domPage) Extract(_ context.Context, selector string) (string, error) {
	if p.doc == nil {
		return "", fmt.Errorf("%w: no page loaded", ErrInvalidRecipe)
	}
	parts, err := parseSelector(selector)
	if err != nil {
		return "", fmt.Errorf("%w: selector %q: %v", ErrInvalidRecipe, selector, err)
	}
	nodes := p.findAll(parts)
	if len(nodes) == 0 {
		return "", ErrSelectorNotFound
	}
	if len(nodes) == 1 {
		return elementValue(nodes[0]), nil
	}
	records := make([]map[string]string, 0, len(nodes))
	for _, node := range nodes {
		records = append(records, elementRecord(node))
	}
	data, err := json.Marshal(records)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (p *domPage) Count(_ context.Context, selector string) (int, error) {
	if p.doc == nil {
		return 0, fmt.Errorf("%w: no page loaded", ErrInvalidRecipe)
	}
	parts, err := parseSelector(selector)
	if err != nil {
		return 0, fmt.Errorf("%w: selector %q: %v", ErrInvalidRecipe, selector, err)
	}
	return len(p.findAll(parts)), nil
}

func (p *domPage) Scroll(ctx context.Context, selector string) error {
	_, err := p.first(selector)
	return err
}

func (p *domPage) Screenshot(_ context.Context) ([]byte, error) {
	if p.doc == nil {
		return nil, fmt.Errorf("%w: no page loaded", ErrInvalidRecipe)
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, p.doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var blockMarkers = []string{
	".captcha", ".verify-dialog", ".risk-control", ".rate-limit",
	".blocked-notice", ".frozen-tip",
}

var blockTexts = []string{
	"验证码", "请求过于频繁", "操作过于频繁", "账号异常", "账号被封", "封禁",
}

func (p *domPage) BlockReason(_ context.Context) (string, bool) {
	if p.doc == nil {
		return "", false
	}
	for _, selector := range blockMarkers {
		parts, err := parseSelector(selector)
		if err == nil && len(p.findAll(parts)) > 0 {
			return selector, true
		}
	}
	text := textContent(p.doc)
	for _, marker := range blockTexts {
		if strings.Contains(text, marker) {
			return marker, true
		}
	}
	return "", false
}

func (p *domPage) first(selector string) (*html.Node, error) {
	if p.doc == nil {
		return nil, fmt.Errorf("%w: no page loaded", ErrInvalidRecipe)
	}
	parts, err := parseSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("%w: selector %q: %v", ErrInvalidRecipe, selector, err)
	}
	nodes := p.findAll(parts)
	if len(nodes) == 0 {
		return nil, ErrSelectorNotFound
	}
	return nodes[0], nil
}

func (p *domPage) findAll(parts []selectorPart) []*html.Node {
	var out []*html.Node
	walkElements(p.doc, func(node *html.Node) {
		if matchesSelector(node, parts) {
			out = append(out, node)
		}
	})
	return out
}

// --- minimal CSS selector engine -------------------------------------------------

type selectorPart struct {
	tag     string
	id      string
	classes []string
	attrs   []attrCondition
}

type attrCondition struct {
	name  string
	op    string
	value string
}

func parseSelector(selector string) ([]selectorPart, error) {
	tokens := splitSelector(selector)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty selector")
	}
	parts := make([]selectorPart, 0, len(tokens))
	for _, token := range tokens {
		part, err := parseCompound(token)
		if err != nil {
			return nil, err
		}
		parts = append(parts, part)
	}
	return parts, nil
}

// splitSelector splits on descendant combinators, ignoring whitespace inside
// attribute brackets and quotes.
func splitSelector(selector string) []string {
	var (
		out   []string
		b     strings.Builder
		depth int
		quote byte
	)
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for i := 0; i < len(selector); i++ {
		c := selector[i]
		switch {
		case quote != 0:
			b.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
			b.WriteByte(c)
		case c == '[':
			depth++
			b.WriteByte(c)
		case c == ']':
			depth--
			b.WriteByte(c)
		case (c == ' ' || c == '\t' || c == '\n' || c == '\r') && depth == 0:
			flush()
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return out
}

func parseCompound(token string) (selectorPart, error) {
	var part selectorPart
	i := 0
	for i < len(token) && isSelectorIdent(token[i]) {
		i++
	}
	part.tag = strings.ToLower(token[:i])

	for i < len(token) {
		switch token[i] {
		case '#':
			i++
			start := i
			for i < len(token) && isSelectorIdent(token[i]) {
				i++
			}
			if i == start {
				return part, fmt.Errorf("empty id in %q", token)
			}
			part.id = token[start:i]
		case '.':
			i++
			start := i
			for i < len(token) && isSelectorIdent(token[i]) {
				i++
			}
			if i == start {
				return part, fmt.Errorf("empty class in %q", token)
			}
			part.classes = append(part.classes, token[start:i])
		case '[':
			end, err := closingBracket(token, i)
			if err != nil {
				return part, err
			}
			cond, err := parseAttrCondition(token[i+1 : end])
			if err != nil {
				return part, err
			}
			part.attrs = append(part.attrs, cond)
			i = end + 1
		default:
			return part, fmt.Errorf("unexpected %q in %q", token[i], token)
		}
	}

	if part.tag == "" && part.id == "" && len(part.classes) == 0 && len(part.attrs) == 0 {
		return part, fmt.Errorf("empty compound selector")
	}
	return part, nil
}

func isSelectorIdent(c byte) bool {
	return c == '-' || c == '_' || c == '*' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func closingBracket(token string, start int) (int, error) {
	var quote byte
	for i := start + 1; i < len(token); i++ {
		c := token[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case ']':
			return i, nil
		}
	}
	return 0, fmt.Errorf("unclosed attribute in %q", token)
}

func parseAttrCondition(expr string) (attrCondition, error) {
	expr = strings.TrimSpace(expr)
	for _, op := range []string{"*=", "^=", "$=", "="} {
		if idx := strings.Index(expr, op); idx >= 0 {
			name := strings.TrimSpace(expr[:idx])
			value := strings.Trim(strings.TrimSpace(expr[idx+len(op):]), `"'`)
			if name == "" {
				return attrCondition{}, fmt.Errorf("empty attribute name in %q", expr)
			}
			return attrCondition{name: name, op: op, value: value}, nil
		}
	}
	if expr == "" {
		return attrCondition{}, fmt.Errorf("empty attribute condition")
	}
	return attrCondition{name: expr}, nil
}

func matchesSelector(node *html.Node, parts []selectorPart) bool {
	if !matchesPart(node, parts[len(parts)-1]) {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	return hasAncestorMatching(node.Parent, parts[:len(parts)-1])
}

func hasAncestorMatching(node *html.Node, parts []selectorPart) bool {
	for cur := node; cur != nil; cur = cur.Parent {
		if cur.Type != html.ElementNode {
			continue
		}
		if !matchesPart(cur, parts[len(parts)-1]) {
			continue
		}
		if len(parts) == 1 {
			return true
		}
		if hasAncestorMatching(cur.Parent, parts[:len(parts)-1]) {
			return true
		}
	}
	return false
}

func matchesPart(node *html.Node, part selectorPart) bool {
	if node == nil || node.Type != html.ElementNode {
		return false
	}
	if part.tag != "" && part.tag != "*" && strings.ToLower(node.Data) != part.tag {
		return false
	}
	if part.id != "" && attr(node, "id") != part.id {
		return false
	}
	classAttr := attr(node, "class")
	for _, class := range part.classes {
		if !hasClass(classAttr, class) {
			return false
		}
	}
	for _, cond := range part.attrs {
		if !matchAttr(attr(node, cond.name), cond) {
			return false
		}
	}
	return true
}

func matchAttr(actual string, cond attrCondition) bool {
	switch cond.op {
	case "":
		return actual != ""
	case "=":
		return actual == cond.value
	case "*=":
		return strings.Contains(actual, cond.value)
	case "^=":
		return strings.HasPrefix(actual, cond.value)
	case "$=":
		return strings.HasSuffix(actual, cond.value)
	}
	return false
}

func hasClass(classAttr, want string) bool {
	for _, class := range strings.Fields(classAttr) {
		if class == want {
			return true
		}
	}
	return false
}

// --- DOM helpers -----------------------------------------------------------------

func attr(node *html.Node, name string) string {
	for _, a := range node.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

func setAttr(node *html.Node, name, value string) {
	for i, a := range node.Attr {
		if strings.EqualFold(a.Key, name) {
			node.Attr[i].Val = value
			return
		}
	}
	node.Attr = append(node.Attr, html.Attribute{Key: name, Val: value})
}

func setText(node *html.Node, value string) {
	for node.FirstChild != nil {
		node.RemoveChild(node.FirstChild)
	}
	node.AppendChild(&html.Node{Type: html.TextNode, Data: value})
}

func textContent(node *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(cur *html.Node) {
		if cur.Type == html.TextNode {
			b.WriteString(cur.Data)
			return
		}
		if cur.Type == html.ElementNode && (cur.Data == "script" || cur.Data == "style") {
			return
		}
		for child := cur.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.TrimSpace(b.String())
}

// elementValue chooses the useful scalar for an extract step: links yield their
// href, images their src, form fields their value, everything else its text.
func elementValue(node *html.Node) string {
	switch strings.ToLower(node.Data) {
	case "a":
		return attr(node, "href")
	case "img":
		return attr(node, "src")
	case "input":
		return attr(node, "value")
	case "textarea":
		if value := attr(node, "value"); value != "" {
			return value
		}
		return textContent(node)
	default:
		return textContent(node)
	}
}

// elementRecord gives a multi-match extract a structured shape: data-* attrs
// become keys, and the first class token of each descendant becomes a field
// key. This maps directly onto list fixtures (id/type/author/content/...).
func elementRecord(node *html.Node) map[string]string {
	record := make(map[string]string)
	for _, a := range node.Attr {
		if strings.HasPrefix(a.Key, "data-") {
			record[strings.TrimPrefix(a.Key, "data-")] = a.Val
		}
	}
	record["text"] = textContent(node)

	var walk func(*html.Node)
	walk = func(cur *html.Node) {
		for child := cur.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != html.ElementNode {
				continue
			}
			if classAttr := attr(child, "class"); classAttr != "" {
				key := strings.Fields(classAttr)[0]
				if _, exists := record[key]; !exists {
					record[key] = textContent(child)
				}
			}
			walk(child)
		}
	}
	walk(node)
	return record
}

func walkElements(node *html.Node, fn func(*html.Node)) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode {
			fn(child)
		}
		walkElements(child, fn)
	}
}
