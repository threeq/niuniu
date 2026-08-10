package bundle

import "github.com/niuniu-dev/niuniu-desktop/internal/rotatinglog"

// rotatingWriter and newRotatingWriter are shims over the rotatinglog package.
// The real implementation lives in desktop/internal/rotatinglog so that
// cmd/personal can reuse it for its own diagnostic log without importing the
// bundle package (which would be a layering inversion).
type rotatingWriter = rotatinglog.Writer

var newRotatingWriter = rotatinglog.New
