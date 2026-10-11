package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/niuniu-dev/niuniu/internal/sandbox"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// bridge owns the box's side of the stdio protocol. Every outbound call the
// function makes becomes one frame to the host and one frame back; the box
// itself never opens a socket, which is why a direct connection attempt is
// impossible rather than merely forbidden.
type bridge struct {
	out      *bufio.Writer
	in       *bufio.Reader
	nextID   int64
	calls    int
	maxCalls int
}

func (b *bridge) request(req *sandbox.HTTPRequestMessage) (*sandbox.HTTPResponseMessage, error) {
	b.nextID++
	id := b.nextID
	req.Kind = sandbox.KindHTTPRequest
	req.ID = id
	if err := sandbox.WriteFrame(b.out, req); err != nil {
		return nil, fmt.Errorf("write egress frame: %w", err)
	}
	if err := b.out.Flush(); err != nil {
		return nil, fmt.Errorf("flush egress frame: %w", err)
	}
	for {
		kind, body, err := sandbox.ReadFrame(b.in)
		if err != nil {
			return nil, fmt.Errorf("the host closed the channel: %w", err)
		}
		if kind != sandbox.KindHTTPResponse {
			continue
		}
		var resp sandbox.HTTPResponseMessage
		if err := json.Unmarshal(body, &resp); err != nil {
			continue
		}
		if resp.ID != id {
			continue
		}
		return &resp, nil
	}
}

// runFunction evaluates the function and returns its exported output. Every
// failure comes back as a structured BoxError so the supervisor, the audit row
// and the agent all see the same code.
func runFunction(invoke sandbox.InvokeMessage, br *bridge, logs *logSink) (any, *sandbox.BoxError) {
	vm := goja.New()

	// Dynamic code construction is refused here as well as in the static lint:
	// a draft that reached execution anyway still cannot build code from data.
	_ = vm.Set("eval", goja.Undefined())
	_ = vm.Set("Function", goja.Undefined())
	_ = vm.Set("input", invoke.Input)

	console := vm.NewObject()
	logFn := func(call goja.FunctionCall) goja.Value {
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			parts = append(parts, a.String())
		}
		logs.add(strings.Join(parts, " "))
		return goja.Undefined()
	}
	_ = console.Set("log", logFn)
	_ = console.Set("warn", logFn)
	_ = console.Set("error", logFn)
	_ = console.Set("info", logFn)
	_ = vm.Set("console", console)

	httpRequest := func(call goja.FunctionCall) goja.Value {
		throw := func(msg string) {
			panic(vm.ToValue(msg))
		}
		if len(call.Arguments) == 0 {
			throw("http_request(options) requires an options object")
		}
		obj := call.Arguments[0].ToObject(vm)
		// Headers are deliberately NOT settable: the proxy owns authentication
		// (the credential is injected at egress and never enters the box), so a
		// function that could set an Authorization header would be able to
		// reach past it. Rejecting the key loudly beats silently dropping it.
		allowed := map[string]bool{
			"operation": true, "params": true, "query": true, "body": true,
		}
		for _, k := range obj.Keys() {
			if !allowed[k] {
				throw(fmt.Sprintf("http_request: unknown option %q — egress is authorized by operation name, and url/method/path/headers cannot be supplied", k))
			}
		}
		op := strings.TrimSpace(obj.Get("operation").String())
		if op == "" {
			throw("http_request: `operation` is required and must name an operation declared in the provider contract")
		}
		if br.calls >= br.maxCalls {
			// Returned, not thrown: the function can decide how to degrade, and
			// the supervisor records the exhausted budget either way.
			return failValue(vm, fmt.Sprintf("the outbound call budget of %d is exhausted", br.maxCalls))
		}
		br.calls++

		out, err := br.request(&sandbox.HTTPRequestMessage{
			Operation: op,
			Params:    exportMap(obj.Get("params")),
			Query:     exportMap(obj.Get("query")),
			Body:      exportMap(obj.Get("body")),
		})
		if err != nil {
			return failValue(vm, err.Error())
		}
		res := vm.NewObject()
		if !out.OK {
			_ = res.Set("ok", false)
			_ = res.Set("status", out.Status)
			_ = res.Set("error", out.Error)
			return res
		}
		_ = res.Set("ok", true)
		_ = res.Set("status", out.Status)
		_ = res.Set("headers", out.Headers)
		// Hand the function parsed JSON when that is what came back, and the
		// raw text otherwise, so a text/plain body is still usable.
		var parsed any
		if len(out.Body) > 0 && json.Unmarshal(out.Body, &parsed) == nil {
			_ = res.Set("body", parsed)
		} else {
			_ = res.Set("body", string(out.Body))
		}
		return res
	}
	_ = vm.Set("http_request", httpRequest)

	if _, err := vm.RunString(invoke.Code); err != nil {
		return nil, boxErrorFrom(err)
	}
	entry := vm.Get(invoke.Entry)
	callable, ok := goja.AssertFunction(entry)
	if !ok {
		return nil, &sandbox.BoxError{
			Code:    sandbox.ErrRuntime,
			Message: fmt.Sprintf("the code does not define a function named %q", invoke.Entry),
		}
	}

	// The in-box deadline is the graceful half of the CPU guard: it unwinds the
	// interpreter at the same instant the supervisor's wall clock expires, so a
	// runaway loop is interrupted rather than only killed.
	limit := invoke.Limits.TimeoutMS
	if limit <= 0 {
		limit = 10_000
	}
	timer := time.AfterFunc(time.Duration(limit)*time.Millisecond, func() {
		vm.Interrupt("operation function exceeded its time budget")
	})
	defer timer.Stop()

	val, err := callable(goja.Undefined(), vm.ToValue(invoke.Input))
	if err != nil {
		return nil, boxErrorFrom(err)
	}
	return val.Export(), nil
}

// failValue builds the {ok:false,...} shape http_request returns on a handled
// failure, so callers can branch instead of crashing.
func failValue(vm *goja.Runtime, msg string) goja.Value {
	o := vm.NewObject()
	_ = o.Set("ok", false)
	_ = o.Set("status", 0)
	_ = o.Set("error", msg)
	return o
}

func boxErrorFrom(err error) *sandbox.BoxError {
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		reason := "the function exceeded its time budget"
		if v, isStr := ie.Value().(string); isStr && v != "" {
			reason = v
		}
		return &sandbox.BoxError{Code: sandbox.ErrTimeout, Message: reason}
	}
	var exc *goja.Exception
	if errors.As(err, &exc) {
		return &sandbox.BoxError{Code: sandbox.ErrRuntime, Message: exc.Value().String()}
	}
	return &sandbox.BoxError{Code: sandbox.ErrRuntime, Message: err.Error()}
}

func exportMap(v goja.Value) map[string]any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	exported, ok := v.Export().(map[string]any)
	if !ok {
		return nil
	}
	return exported
}
