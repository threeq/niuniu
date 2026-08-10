package webviewreset_test

import (
	"testing"

	"github.com/niuniu-dev/niuniu-desktop/internal/webviewreset"
	"github.com/stretchr/testify/assert"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type fakeWindow struct {
	forceReloadCalls int
	closeCalls       int
	size             [2]int
	position         [2]int
}

func (f *fakeWindow) ForceReload()           { f.forceReloadCalls++ }
func (f *fakeWindow) Close()                 { f.closeCalls++ }
func (f *fakeWindow) Size() (int, int)       { return f.size[0], f.size[1] }
func (f *fakeWindow) Position() (int, int)   { return f.position[0], f.position[1] }

func TestReload_NilWindow_NoPanic(t *testing.T) {
	assert.NotPanics(t, func() { webviewreset.Reload(nil) })
}

func TestReload_CallsForceReloadOnce(t *testing.T) {
	fw := &fakeWindow{}
	webviewreset.Reload(fw)
	assert.Equal(t, 1, fw.forceReloadCalls)
}

func TestRebuild_CapturesGeometryAndRecreates(t *testing.T) {
	fw := &fakeWindow{size: [2]int{1400, 820}, position: [2]int{120, 60}}
	var capturedOpts application.WebviewWindowOptions
	fakeNew := func(opts application.WebviewWindowOptions) *application.WebviewWindow {
		capturedOpts = opts
		return nil
	}

	got := webviewreset.Rebuild(fw, webviewreset.RebuildSpec{
		Name:      "main",
		Title:     "Niuniu",
		URL:       "http://127.0.0.1:3000/",
		NewWindow: fakeNew,
	})

	assert.Equal(t, 0, fw.closeCalls, "Rebuild should NOT close the old window; callers must do it after wiring")
	assert.Equal(t, "main", capturedOpts.Name)
	assert.Equal(t, "Niuniu", capturedOpts.Title)
	assert.Equal(t, "http://127.0.0.1:3000/", capturedOpts.URL)
	assert.Equal(t, 1400, capturedOpts.Width)
	assert.Equal(t, 820, capturedOpts.Height)
	assert.Equal(t, 120, capturedOpts.X)
	assert.Equal(t, 60, capturedOpts.Y)
	assert.True(t, capturedOpts.Hidden)
	assert.Nil(t, got)
}

func TestRebuild_ZeroSizeFallsBackToDefault(t *testing.T) {
	fw := &fakeWindow{}
	var capturedOpts application.WebviewWindowOptions
	fakeNew := func(opts application.WebviewWindowOptions) *application.WebviewWindow {
		capturedOpts = opts
		return nil
	}

	webviewreset.Rebuild(fw, webviewreset.RebuildSpec{
		Name:      "main",
		Title:     "Niuniu",
		URL:       "http://127.0.0.1:3000/",
		NewWindow: fakeNew,
	})

	assert.Equal(t, 1280, capturedOpts.Width)
	assert.Equal(t, 800, capturedOpts.Height)
}

func TestRebuild_NilWindow_NoPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		webviewreset.Rebuild(nil, webviewreset.RebuildSpec{Name: "x", URL: "u"})
	})
}
