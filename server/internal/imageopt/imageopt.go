// Package imageopt provides server-side image optimization for chat
// attachments uploaded through /workspaces/:id/attachments. See
// docs/superpowers/specs/2026-05-11-large-image-optimization-design.md.
package imageopt

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Options controls the optimization pipeline. Zero value is not valid;
// callers should start from DefaultOptions().
type Options struct {
	TriggerLongEdgePx int   // resize only if long edge exceeds this
	TriggerSizeBytes  int64 // resize only if file size exceeds this
	TargetLongEdgePx  int   // first-pass target long edge
	TargetMaxBytes    int64 // hard size budget (strictly enforced)
	MinQuality        int   // preferred-stop quality; pipeline keeps iterating below this to meet TargetMaxBytes
	MinLongEdgePx     int   // preferred-stop long edge; pipeline keeps iterating below this to meet TargetMaxBytes
}

// DefaultOptions returns the spec-defined defaults.
//
// TriggerSizeBytes sits slightly above TargetMaxBytes (100KB vs 80KB) so
// uploads in the 80–100KB band are left alone — they're already close
// enough to budget that the JPEG re-encode round-trip is not worth the
// CPU. Anything strictly above 100KB enters the pipeline and is brought
// down to ≤ TargetMaxBytes. The earlier 500KB trigger left a much wider
// 80–500KB gap where users saw "image not optimized" surprises.
func DefaultOptions() Options {
	return Options{
		TriggerLongEdgePx: 1280,
		TriggerSizeBytes:  100 * 1024,
		TargetLongEdgePx:  1280,
		TargetMaxBytes:    80 * 1024,
		MinQuality:        40,
		MinLongEdgePx:     512,
	}
}

// SkipReason enumerates non-error reasons an image was not optimized.
type SkipReason string

const (
	SkipNone              SkipReason = ""
	SkipAlreadySmall      SkipReason = "already_small"
	SkipUnsupportedFormat SkipReason = "unsupported_format"
	SkipAnimated          SkipReason = "animated"
	SkipDecodeFailed      SkipReason = "decode_failed"
	SkipEncodeFailed      SkipReason = "encode_failed"
	SkipIOFailed          SkipReason = "io_failed"
	SkipEncodeBigger      SkipReason = "encode_bigger"
	SkipBusy              SkipReason = "busy"     // concurrency semaphore timeout (could not start)
	SkipCanceled          SkipReason = "canceled" // ctx canceled mid-flight (client gone or shutdown)
)

// Result describes the outcome of Optimize. SkipReason is non-empty iff
// Optimized is false.
type Result struct {
	Optimized    bool
	NewPath      string // empty when Optimized=false
	NewName      string // empty when Optimized=false; basename only
	OrigSize     int64
	NewSize      int64 // equals OrigSize when Optimized=false
	OrigLongEdge int   // 0 when decode never happened
	NewLongEdge  int   // 0 when Optimized=false
	Format       string // "jpeg" | "webp" | "png8" when Optimized=true; "" otherwise
	SkipReason   SkipReason // empty when Optimized=true; otherwise: already_small | unsupported_format | animated | decode_failed | encode_failed | io_failed | encode_bigger | busy | canceled
}

// optSem caps concurrent Optimize() invocations (spec §12.1). Capacity is
// fixed at process start: half of NumCPU, minimum 2. Not configurable in
// the first iteration (YAGNI).
var optSem = func() chan struct{} {
	n := runtime.NumCPU() / 2
	if n < 2 {
		n = 2
	}
	return make(chan struct{}, n)
}()

const optAcquireTimeout = 10 * time.Second

// Optimize never returns a non-nil error in current usage; failures
// surface via Result.SkipReason. The error return is reserved for
// programming-bug paths only.
func Optimize(ctx context.Context, path string, opts Options) (Result, error) {
	// Concurrency limiter: SkipBusy if we can't acquire (spec §12.1).
	origSize := int64(0)
	if st, err := os.Stat(path); err == nil {
		origSize = st.Size()
	}
	timer := time.NewTimer(optAcquireTimeout)
	defer timer.Stop()
	select {
	case optSem <- struct{}{}:
		defer func() { <-optSem }()
	case <-ctx.Done():
		return Result{OrigSize: origSize, NewSize: origSize, SkipReason: SkipCanceled}, nil
	case <-timer.C:
		slog.Warn("imageopt: semaphore timeout", "path", path, "timeout", optAcquireTimeout)
		return Result{OrigSize: origSize, NewSize: origSize, SkipReason: SkipBusy}, nil
	}

	return optimizeLocked(ctx, path, opts)
}

func optimizeLocked(ctx context.Context, path string, opts Options) (Result, error) {
	d, err := detect(path)
	if err != nil {
		slog.Error("imageopt: detect failed", "path", path, "err", err)
		return Result{SkipReason: SkipIOFailed, OrigSize: 0, NewSize: 0}, nil
	}

	res := Result{
		OrigSize: d.SizeBytes,
		NewSize:  d.SizeBytes,
	}

	// Skip judgment
	switch d.Format {
	case "svg", "unknown":
		res.SkipReason = SkipUnsupportedFormat
		return res, nil
	case "gif":
		if d.Animated {
			res.SkipReason = SkipAnimated
			return res, nil
		}
	case "webp":
		if d.Animated {
			res.SkipReason = SkipAnimated
			return res, nil
		}
	}

	// Trigger check requires the long edge — decode now (yes we decode
	// twice, once here and once in runPipeline; for small files decode is
	// fast and the design simplifies). image.DecodeConfig would be lighter
	// but each format's config decoder has a different API.
	long := 0
	{
		img, _, derr := decodeImage(path, d.Format)
		if derr != nil {
			slog.Error("imageopt: decode failed", "path", path, "err", derr)
			res.SkipReason = SkipDecodeFailed
			return res, nil
		}
		b := img.Bounds()
		w, h := b.Dx(), b.Dy()
		long = w
		if h > w {
			long = h
		}
		res.OrigLongEdge = long
	}

	if d.SizeBytes <= opts.TriggerSizeBytes && long <= opts.TriggerLongEdgePx {
		res.SkipReason = SkipAlreadySmall
		return res, nil
	}

	// Run pipeline
	buf, edge, q, format, err := runPipeline(ctx, path, opts)
	if err != nil {
		if errors.Is(err, errEncodeBigger) {
			res.SkipReason = SkipEncodeBigger
			return res, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			res.SkipReason = SkipCanceled
			return res, nil
		}
		slog.Error("imageopt: pipeline failed", "path", path, "err", err)
		res.SkipReason = SkipEncodeFailed
		return res, nil
	}

	// Write out
	dir := filepath.Dir(path)
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	ext := ".jpg"
	switch format {
	case "webp":
		ext = ".webp"
	case "png8":
		ext = ".png"
	}

	// Conflict handling: if extension changed (.png → .jpg), target
	// newPath might collide with an existing file in .attachments/ →
	// add timestamp suffix to avoid silent overwrite (fixes review C2).
	newName, newPath := nonCollidingPath(dir, base, ext, path)

	if err := atomicWrite(newPath, buf.Bytes()); err != nil {
		slog.Error("imageopt: write failed", "path", newPath, "err", err)
		res.SkipReason = SkipIOFailed
		return res, nil
	}
	if newPath != path {
		_ = os.Remove(path) // remove old-extension file; failure non-fatal
	}

	// No os.Stat after write — buf.Len() is the final size (atomicWrite
	// is WriteFile + Rename, no one truncates between). Keeps spec §4.2
	// "Optimize never returns nil error" contract.
	res.Optimized = true
	res.NewPath = newPath
	res.NewName = newName
	res.NewSize = int64(buf.Len())
	res.NewLongEdge = edge
	res.Format = format
	res.SkipReason = SkipNone

	slog.Info("imageopt: optimized",
		"path", newPath,
		"orig_size", res.OrigSize, "new_size", res.NewSize,
		"orig_edge", res.OrigLongEdge, "new_edge", res.NewLongEdge,
		"format", format, "quality", q)

	return res, nil
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// nonCollidingPath returns (name, fullPath) guaranteed not to collide
// with any existing file in dir, EXCEPT for srcPath (which we are about
// to delete or overwrite as the original). Reuses the handler's
// timestamp-suffix rule.
func nonCollidingPath(dir, base, ext, srcPath string) (string, string) {
	name := base + ext
	full := filepath.Join(dir, name)
	if full == srcPath {
		return name, full
	}
	if _, err := os.Stat(full); err != nil {
		return name, full // doesn't exist → OK
	}
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	name = base + "_" + ts + ext
	return name, filepath.Join(dir, name)
}
