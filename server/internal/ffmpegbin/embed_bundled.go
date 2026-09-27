//go:build ffmpeg_bundled

package ffmpegbin

import (
	"embed"
	"runtime"
)

// embeddedDist carries the static ffmpeg/ffprobe binaries staged by
// `make ffmpeg-stage` into server/internal/ffmpegbin/dist/<goos>-<goarch>/.
// dist/ is gitignored — the payload is downloaded at build time, never
// committed — and the whole tree is embedded because go:embed patterns are
// static: they cannot be parameterised by runtime.GOOS/GOARCH. payload()
// below selects the running platform's subdirectory at runtime, so the
// embedded bytes come from dist/<goos>-<goarch>/ exactly as the interface
// contract requires. Stage only the target you build (the ffmpeg-stage
// default) — anything else under dist/ is embedded but unused, at the cost of
// binary size.
//
// A build with this tag REQUIRES dist/ to exist and contain at least one file:
// otherwise the compiler fails with `pattern all:dist: no matching files
// found`. That is deliberate — a release build must run `make ffmpeg-stage`
// first, and a silent fallback to PATH would ship a module without ffmpeg.
//
//go:embed all:dist
var embeddedDist embed.FS

// payload returns the embedded binaries for the running platform, keyed by
// file name ("ffmpeg" / "ffprobe", plus the .exe suffix on Windows). Missing
// entries are tolerated by design: a platform may stage only ffmpeg, and
// ffprobe then falls back to PATH. Only the two known tool names count as
// payload — anything else a staged dist/ dir happens to contain (a stray
// download temp file, a README) is ignored.
func payload() map[string][]byte {
	dir := "dist/" + platformDir(runtime.GOOS, runtime.GOARCH)
	var out map[string][]byte
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		name := binNameFor(runtime.GOOS, tool)
		b, err := embeddedDist.ReadFile(dir + "/" + name)
		if err != nil {
			continue
		}
		if out == nil {
			out = make(map[string][]byte, 2)
		}
		out[name] = b
	}
	return out
}
