//go:build !ffmpeg_bundled

package ffmpegbin

// payload returns no binaries: a build without the ffmpeg_bundled tag carries
// no embedded ffmpeg, so Fingerprint reports "" and Resolve skips the
// extracted-copy step, relying on $NIUNIU_FFMPEG / $NIUNIU_FFPROBE or PATH.
// This keeps development builds (and every `go test` run) working without a
// staged payload.
func payload() map[string][]byte { return nil }
