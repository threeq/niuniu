package webviewreset

import "github.com/wailsapp/wails/v3/pkg/application"

// windowHandle is the narrow subset of application.WebviewWindow used by
// this package. Exists so tests can supply a fake.
type windowHandle interface {
	ForceReload()
	Close()
	Size() (int, int)
	Position() (int, int)
}

// Compile-time check: *WebviewWindow satisfies windowHandle.
var _ windowHandle = (*application.WebviewWindow)(nil)

// Reload triggers a hard reload of the page bypassing cache. Safe on nil.
func Reload(win windowHandle) {
	if win == nil {
		return
	}
	win.ForceReload()
}

// RebuildSpec describes the window to recreate after closing the old one.
// NewWindow is optional; if nil, Rebuild uses application.Get().Window.NewWithOptions.
type RebuildSpec struct {
	Name      string
	Title     string
	URL       string
	NewWindow func(application.WebviewWindowOptions) *application.WebviewWindow
}

// Rebuild captures the geometry from win and creates a new WebviewWindow
// with the same size/position and the URL/title/name in spec. The new
// window starts Hidden so the caller has time to register close/event
// hooks before Show(). Safe on nil win.
//
// IMPORTANT: Rebuild does NOT close the old window. Callers MUST close
// the old window AFTER creating and wiring up the new one. Closing the
// old window before the new one exists causes Wails to quit the app
// (zero-window moment).
//
// Callers are responsible for:
//   - stopping listeners tied to the old window
//   - calling Rebuild to get the new window
//   - registering hooks on the new window
//   - assigning the new window to their state
//   - Close()-ing the old window only AFTER the above steps
//   - Show()-ing the new window
func Rebuild(win windowHandle, spec RebuildSpec) *application.WebviewWindow {
	if win == nil {
		return nil
	}
	width, height := win.Size()
	x, y := win.Position()
	if width <= 0 {
		width = 1280
	}
	if height <= 0 {
		height = 800
	}

	newFn := spec.NewWindow
	if newFn == nil {
		newFn = func(opts application.WebviewWindowOptions) *application.WebviewWindow {
			return application.Get().Window.NewWithOptions(opts)
		}
	}
	return newFn(application.WebviewWindowOptions{
		Name:   spec.Name,
		Title:  spec.Title,
		URL:    spec.URL,
		Width:  width,
		Height: height,
		X:      x,
		Y:      y,
		Hidden: true,
	})
}
