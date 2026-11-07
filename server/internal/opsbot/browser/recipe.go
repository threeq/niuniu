package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// A Platform Pack owns the Recipe schema; the runner only interprets it.
type (
	Recipe     = packs.Recipe
	RecipeStep = packs.RecipeStep
)

// Input is one recipe execution request. Values fill {{placeholders}} in the
// recipe's selectors and values.
type Input struct {
	Session string
	Values  map[string]string
}

// Output is what a completed recipe extracted.
type Output struct {
	// Values holds one entry per extract step, keyed by the step's value.
	Values map[string]string
	// Screenshots holds one entry per screenshot step, keyed by the step's
	// value.
	Screenshots map[string][]byte
}

// FailureClass identifies why a recipe step failed so callers can distinguish
// a changed platform UI from a transient network blip or a risk-control block.
type FailureClass string

const (
	ClassUnknownAction    FailureClass = "unknown_action"
	ClassHostNotAllowed   FailureClass = "host_not_allowed"
	ClassSelectorNotFound FailureClass = "selector_not_found"
	ClassAssertFailed     FailureClass = "assert_failed"
	ClassTimeout          FailureClass = "timeout"
	ClassBlocked          FailureClass = "blocked"
	ClassLoopLimit        FailureClass = "loop_limit"
	ClassInvalidRecipe    FailureClass = "invalid_recipe"
	ClassExecutionFailed  FailureClass = "execution_failed"
)

// Recipe-level sentinels. Selector and assertion failures also satisfy
// errors.Is(err, ErrRecipeBroken) (declared in worker.go) because they usually
// mean the platform UI changed and should degrade to a manual package.
var (
	ErrUnknownAction    = errors.New("opsbot/browser: unknown recipe action")
	ErrHostNotAllowed   = errors.New("opsbot/browser: navigation host is not in the allowlist")
	ErrSelectorNotFound = errors.New("opsbot/browser: selector not found")
	ErrAssertFailed     = errors.New("opsbot/browser: assert failed")
	ErrLoopLimit        = errors.New("opsbot/browser: loop iteration limit reached")
	ErrInvalidRecipe    = errors.New("opsbot/browser: invalid recipe")
	ErrMissingInput     = errors.New("opsbot/browser: missing recipe input")
	ErrNoPage           = errors.New("opsbot/browser: no page opener configured")
)

// StepError annotates a failure with the step that produced it and its class.
type StepError struct {
	Step   int
	Action string
	Class  FailureClass
	Err    error
}

func (e *StepError) Error() string {
	return fmt.Sprintf("opsbot/browser: step %d (%s): %s: %v", e.Step, e.Action, e.Class, e.Err)
}

func (e *StepError) Unwrap() error { return e.Err }

func (e *StepError) Is(target error) bool {
	switch target {
	case ErrRecipeBroken:
		return e.Class == ClassSelectorNotFound ||
			e.Class == ClassAssertFailed ||
			e.Class == ClassInvalidRecipe
	case ErrTimeout:
		return e.Class == ClassTimeout
	case ErrBlocked:
		return e.Class == ClassBlocked
	}
	return errors.Is(e.Err, target)
}

// FailureClassOf reports the class of a recipe failure, or "" when err did not
// come from the recipe runner.
func FailureClassOf(err error) FailureClass {
	var se *StepError
	if errors.As(err, &se) {
		return se.Class
	}
	return ""
}

// Page is the browser surface a recipe runs against. The sandbox implements it
// with a local DOM; a real backend implements it with Playwright/CDP.
//
// Find checks for the first matching selector immediately. WaitAny polls until
// one matches or ctx is done.
type Page interface {
	Goto(ctx context.Context, rawURL string) error
	Find(ctx context.Context, selectors []string) (string, error)
	WaitAny(ctx context.Context, selectors []string) (string, error)
	Click(ctx context.Context, selector string) error
	Fill(ctx context.Context, selector, value string) error
	Upload(ctx context.Context, selector string, files []string) error
	Extract(ctx context.Context, selector string) (string, error)
	Count(ctx context.Context, selector string) (int, error)
	Scroll(ctx context.Context, selector string) error
	Screenshot(ctx context.Context) ([]byte, error)
	// BlockReason reports a risk-control signal on the current page (captcha,
	// rate limit, ban notice). The runner turns any failure on such a page
	// into a blocked classification.
	BlockReason(ctx context.Context) (string, bool)
}

// recipeActions is the DSL whitelist. A recipe using anything else is rejected
// before the page is opened.
var recipeActions = map[string]bool{
	"goto":       true,
	"wait":       true,
	"click":      true,
	"fill":       true,
	"upload":     true,
	"extract":    true,
	"screenshot": true,
	"assert":     true,
	"loop":       true,
	"branch":     true,
}

const (
	defaultStepTimeout  = 15 * time.Second
	defaultLoopCap      = 100
	blockedCheckTimeout = 2 * time.Second
)

// RecipeRunner executes the declarative Browser Recipe DSL.
type RecipeRunner struct {
	// Open returns the page a recipe runs on. It is called once per run.
	Open func(ctx context.Context, in Input) (Page, error)
	// AllowedHosts is the egress allowlist (Pack manifest allowed_hosts). An
	// empty list denies every navigation.
	AllowedHosts []string
	// DefaultStepTimeout is used when a step declares no timeout_ms.
	DefaultStepTimeout time.Duration
	// MaxLoopIterations hard-caps loop steps. Zero means 100.
	MaxLoopIterations int
}

// Run executes every step in order and returns the extracted values.
func (r *RecipeRunner) Run(ctx context.Context, recipe Recipe, in Input) (Output, error) {
	out := Output{Values: map[string]string{}, Screenshots: map[string][]byte{}}
	if len(recipe.Steps) == 0 {
		return out, fmt.Errorf("%w: recipe has no steps", ErrInvalidRecipe)
	}
	if err := r.validate(recipe, in); err != nil {
		return out, err
	}
	if r.Open == nil {
		return out, ErrNoPage
	}

	page, err := r.Open(ctx, in)
	if err != nil {
		return out, fmt.Errorf("opsbot/browser: open page: %w", err)
	}

	for i := range recipe.Steps {
		step := recipe.Steps[i]
		if err := r.runStep(ctx, page, i+1, step, in, &out); err != nil {
			return out, r.classify(ctx, page, i+1, step.Action, err)
		}
	}
	return out, nil
}

// validate rejects unknown actions and forbidden navigation before a browser
// session is ever started.
func (r *RecipeRunner) validate(recipe Recipe, in Input) error {
	for i, step := range recipe.Steps {
		if !recipeActions[step.Action] {
			return &StepError{Step: i + 1, Action: step.Action, Class: ClassUnknownAction,
				Err: fmt.Errorf("%w: %q", ErrUnknownAction, step.Action)}
		}
		if step.Action != "goto" {
			continue
		}
		value, err := substitute(step.Value, in.Values)
		if err != nil {
			continue // reported when the step runs
		}
		if err := r.checkHost(value); err != nil {
			class := ClassHostNotAllowed
			if errors.Is(err, ErrInvalidRecipe) {
				class = ClassInvalidRecipe
			}
			return &StepError{Step: i + 1, Action: step.Action, Class: class, Err: err}
		}
	}
	return nil
}

func (r *RecipeRunner) runStep(ctx context.Context, page Page, index int, raw RecipeStep, in Input, out *Output) error {
	step, err := substituteStep(raw, in)
	if err != nil {
		return &StepError{Step: index, Action: raw.Action, Class: ClassInvalidRecipe, Err: err}
	}

	if step.Action == "loop" {
		err := r.runLoop(ctx, page, step)
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return wrapStepError(index, step.Action, err)
	}

	stepCtx, cancel := r.withStepTimeout(ctx, step)
	defer cancel()

	err = r.exec(stepCtx, page, index, step, out)
	if err == nil {
		return nil
	}
	// Caller cancellation must never be reported as a platform fault.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &StepError{Step: index, Action: step.Action, Class: ClassTimeout,
			Err: fmt.Errorf("%w: %v", ErrTimeout, err)}
	}
	var se *StepError
	if errors.As(err, &se) {
		return se
	}
	return wrapStepError(index, step.Action, err)
}

func (r *RecipeRunner) withStepTimeout(ctx context.Context, step RecipeStep) (context.Context, context.CancelFunc) {
	d := r.DefaultStepTimeout
	if d <= 0 {
		d = defaultStepTimeout
	}
	if step.Timeout > 0 {
		d = time.Duration(step.Timeout) * time.Millisecond
	}
	return context.WithTimeout(ctx, d)
}

func (r *RecipeRunner) exec(ctx context.Context, page Page, index int, step RecipeStep, out *Output) error {
	switch step.Action {
	case "goto":
		return r.navigate(ctx, page, step)
	case "wait":
		_, err := page.WaitAny(ctx, selectors(step))
		return err
	case "click":
		sel, err := findSelector(ctx, page, step)
		if err != nil {
			return err
		}
		return page.Click(ctx, sel)
	case "fill":
		sel, err := findSelector(ctx, page, step)
		if err != nil {
			return err
		}
		return page.Fill(ctx, sel, step.Value)
	case "upload":
		sel, err := findSelector(ctx, page, step)
		if err != nil {
			return err
		}
		return page.Upload(ctx, sel, splitList(step.Value))
	case "extract":
		sel, err := findSelector(ctx, page, step)
		if err != nil {
			return err
		}
		if strings.TrimSpace(step.Value) == "" {
			return fmt.Errorf("%w: extract needs an output name", ErrInvalidRecipe)
		}
		value, err := page.Extract(ctx, sel)
		if err != nil {
			return err
		}
		out.Values[step.Value] = value
		return nil
	case "screenshot":
		if strings.TrimSpace(step.Value) == "" {
			return fmt.Errorf("%w: screenshot needs an output name", ErrInvalidRecipe)
		}
		data, err := page.Screenshot(ctx)
		if err != nil {
			return err
		}
		out.Screenshots[step.Value] = data
		return nil
	case "assert":
		if _, err := findSelector(ctx, page, step); err != nil {
			return fmt.Errorf("%w: %v", ErrAssertFailed, err)
		}
		return nil
	case "branch":
		return r.runBranch(ctx, page, index, step, out)
	}
	return fmt.Errorf("%w: %q", ErrUnknownAction, step.Action)
}

func (r *RecipeRunner) navigate(ctx context.Context, page Page, step RecipeStep) error {
	if err := r.checkHost(step.Value); err != nil {
		return err
	}
	return page.Goto(ctx, step.Value)
}

func (r *RecipeRunner) checkHost(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%w: %q must be an absolute URL", ErrInvalidRecipe, raw)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("%w: scheme %q", ErrHostNotAllowed, u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range r.AllowedHosts {
		if strings.ToLower(strings.TrimSpace(allowed)) == host {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrHostNotAllowed, host)
}

func (r *RecipeRunner) runLoop(ctx context.Context, page Page, step RecipeStep) error {
	limit := r.MaxLoopIterations
	if limit <= 0 {
		limit = defaultLoopCap
	}

	until := step.Args["until"]
	if until != "" && until != "no_new_items" {
		return fmt.Errorf("%w: unsupported loop condition %q", ErrInvalidRecipe, until)
	}

	if raw, ok := step.Args["max_iterations"]; ok && strings.TrimSpace(raw) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n <= 0 {
			return fmt.Errorf("%w: max_iterations %q", ErrInvalidRecipe, raw)
		}
		if n < limit {
			limit = n
		}
	}

	prev := -1
	for i := 0; i < limit; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := page.Count(ctx, step.Selector)
		if err != nil {
			return err
		}
		if until == "no_new_items" && i > 0 && count <= prev {
			return nil
		}
		prev = count
		if err := page.Scroll(ctx, step.Selector); err != nil {
			return err
		}
	}
	if until == "no_new_items" {
		return fmt.Errorf("%w: %q never stabilised after %d iterations", ErrLoopLimit, step.Selector, limit)
	}
	return nil
}

// Branch args form a tiny conditional action:
//
//	condition:      selector_exists | selector_missing | value_equals | value_not_equals
//	selector:       selector for selector_* conditions
//	variable:       output name for value_* conditions
//	expected:       expected value for value_* conditions
//	then/else:      action to run; loop/branch cannot be nested
//	then_/else_     selector, value, fallback (comma separated), timeout_ms
func (r *RecipeRunner) runBranch(ctx context.Context, page Page, index int, step RecipeStep, out *Output) error {
	ok, err := r.evalCondition(ctx, page, step, out)
	if err != nil {
		return err
	}
	prefix := "else"
	if ok {
		prefix = "then"
	}
	action := step.Args[prefix]
	if action == "" {
		return nil
	}
	if action == "loop" || action == "branch" {
		return fmt.Errorf("%w: branch cannot nest %q", ErrInvalidRecipe, action)
	}
	if !recipeActions[action] {
		return fmt.Errorf("%w: branch %s action %q", ErrUnknownAction, prefix, action)
	}

	sub := RecipeStep{
		Action:   action,
		Selector: step.Args[prefix+"_selector"],
		Value:    step.Args[prefix+"_value"],
		Fallback: splitList(step.Args[prefix+"_fallback"]),
	}
	if raw := step.Args[prefix+"_timeout_ms"]; strings.TrimSpace(raw) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n <= 0 {
			return fmt.Errorf("%w: %s_timeout_ms %q", ErrInvalidRecipe, prefix, raw)
		}
		sub.Timeout = n
	}
	return r.exec(ctx, page, index, sub, out)
}

func (r *RecipeRunner) evalCondition(ctx context.Context, page Page, step RecipeStep, out *Output) (bool, error) {
	switch step.Args["condition"] {
	case "selector_exists":
		sel := append([]string{step.Args["selector"]}, splitList(step.Args["fallback"])...)
		_, err := page.Find(ctx, sel)
		return err == nil, ignoreNotFound(err)
	case "selector_missing":
		sel := append([]string{step.Args["selector"]}, splitList(step.Args["fallback"])...)
		_, err := page.Find(ctx, sel)
		if errors.Is(err, ErrSelectorNotFound) {
			return true, nil
		}
		return false, err
	case "value_equals":
		return out.Values[step.Args["variable"]] == step.Args["expected"], nil
	case "value_not_equals":
		return out.Values[step.Args["variable"]] != step.Args["expected"], nil
	default:
		return false, fmt.Errorf("%w: unsupported branch condition %q", ErrInvalidRecipe, step.Args["condition"])
	}
}

func ignoreNotFound(err error) error {
	if err == nil || errors.Is(err, ErrSelectorNotFound) {
		return nil
	}
	return err
}

func (r *RecipeRunner) classify(ctx context.Context, page Page, index int, action string, err error) error {
	// Caller cancellation is not a platform fault; propagate it untouched.
	if ctx.Err() != nil {
		return err
	}

	var se *StepError
	alreadyClassified := errors.As(err, &se)

	// A risk-control signal outranks whatever selector happened to fail: the
	// account must pause, not retry.
	if page != nil && !errors.Is(err, context.Canceled) {
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), blockedCheckTimeout)
		reason, blocked := page.BlockReason(checkCtx)
		cancel()
		if blocked {
			return &StepError{Step: index, Action: action, Class: ClassBlocked,
				Err: fmt.Errorf("%w: %s", ErrBlocked, reason)}
		}
	}

	if alreadyClassified {
		return err
	}
	return wrapStepError(index, action, err)
}

func wrapStepError(index int, action string, err error) error {
	if err == nil {
		return nil
	}
	var se *StepError
	if errors.As(err, &se) {
		return se
	}
	class := ClassExecutionFailed
	switch {
	case errors.Is(err, ErrUnknownAction):
		class = ClassUnknownAction
	case errors.Is(err, ErrHostNotAllowed):
		class = ClassHostNotAllowed
	case errors.Is(err, ErrSelectorNotFound):
		class = ClassSelectorNotFound
	case errors.Is(err, ErrAssertFailed):
		class = ClassAssertFailed
	case errors.Is(err, ErrLoopLimit):
		class = ClassLoopLimit
	case errors.Is(err, ErrInvalidRecipe), errors.Is(err, ErrMissingInput):
		class = ClassInvalidRecipe
	}
	return &StepError{Step: index, Action: action, Class: class, Err: err}
}

func findSelector(ctx context.Context, page Page, step RecipeStep) (string, error) {
	selectors := selectors(step)
	if len(selectors) == 0 {
		return "", fmt.Errorf("%w: %s needs a selector", ErrInvalidRecipe, step.Action)
	}
	return page.Find(ctx, selectors)
}

func selectors(step RecipeStep) []string {
	out := make([]string, 0, 1+len(step.Fallback))
	if strings.TrimSpace(step.Selector) != "" {
		out = append(out, step.Selector)
	}
	for _, fallback := range step.Fallback {
		if strings.TrimSpace(fallback) != "" {
			out = append(out, fallback)
		}
	}
	return out
}

func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

var placeholderRE = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)

func substitute(raw string, values map[string]string) (string, error) {
	if raw == "" {
		return raw, nil
	}
	var missing string
	out := placeholderRE.ReplaceAllStringFunc(raw, func(match string) string {
		key := placeholderRE.FindStringSubmatch(match)[1]
		value, ok := values[key]
		if !ok {
			if missing == "" {
				missing = key
			}
			return match
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("%w: %q", ErrMissingInput, missing)
	}
	return out, nil
}

func substituteStep(step RecipeStep, in Input) (RecipeStep, error) {
	out := step
	var err error
	if out.Selector, err = substitute(step.Selector, in.Values); err != nil {
		return step, err
	}
	if out.Value, err = substitute(step.Value, in.Values); err != nil {
		return step, err
	}
	if len(step.Fallback) > 0 {
		out.Fallback = make([]string, len(step.Fallback))
		for i, fallback := range step.Fallback {
			if out.Fallback[i], err = substitute(fallback, in.Values); err != nil {
				return step, err
			}
		}
	}
	if len(step.Args) > 0 {
		out.Args = make(map[string]string, len(step.Args))
		for key, value := range step.Args {
			if out.Args[key], err = substitute(value, in.Values); err != nil {
				return step, err
			}
		}
	}
	return out, nil
}
