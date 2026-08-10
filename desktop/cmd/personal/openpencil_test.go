package main

import "testing"

// A non-OpenPencil message must be ignored (no panic, no dialog). With a nil
// sender window and nil wailsApp both the result-post and install-prompt are
// safe no-ops, so this also exercises the app-missing branch without a real
// Wails app.
func TestHandleOpenPencilMessageIgnoresUnrelated(t *testing.T) {
	a := &App{}
	// Runner bridge messages and arbitrary noise must not be treated as launches.
	a.HandleOpenPencilMessage(nil, `{"type":"niuniu-runner-config","workspaceId":"7"}`)
	a.HandleOpenPencilMessage(nil, `not json at all`)
	// A well-formed open-pencil message with no app installed must not panic
	// (nil window + nil wailsApp → prompt & result post are no-ops).
	a.HandleOpenPencilMessage(nil, `{"type":"niuniu-open-pencil","filePath":""}`)
}

// openPencilLaunchSpec must report "not found" (ok=false) when no OpenPencil
// binary/app is present, rather than returning a bogus argv. On CI no OpenPencil
// is installed, so this is the expected path and guards against a launch of a
// non-existent binary.
func TestOpenPencilLaunchSpecAbsent(t *testing.T) {
	if argv, ok := openPencilLaunchSpec(""); ok {
		// If a dev machine happens to have OpenPencil installed the spec is
		// allowed to be found; only assert the argv is well-formed then.
		if len(argv) == 0 {
			t.Fatalf("found=true but empty argv")
		}
		t.Skipf("OpenPencil present on this host: %v", argv)
	}
}
