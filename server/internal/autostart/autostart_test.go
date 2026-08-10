package autostart

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWindowsRunValue(t *testing.T) {
	got := windowsRunValue(`C:\Program Files\Niuniu\niuniu-desktop.exe`, false)
	want := `"C:\Program Files\Niuniu\niuniu-desktop.exe" --autostart`
	if got != want {
		t.Fatalf("windowsRunValue = %q, want %q", got, want)
	}
	gotMin := windowsRunValue(`C:\Program Files\Niuniu\niuniu-desktop.exe`, true)
	wantMin := want + ` --minimized`
	if gotMin != wantMin {
		t.Fatalf("windowsRunValue minimized = %q, want %q", gotMin, wantMin)
	}
}

func TestDarwinPlist(t *testing.T) {
	p := darwinPlist("/Applications/Niuniu.app/Contents/MacOS/niuniu-desktop", false)
	for _, sub := range []string{
		"com.niuniu.personal",
		"<key>RunAtLoad</key>",
		"--autostart",
		"niuniu-desktop",
	} {
		if !strings.Contains(p, sub) {
			t.Errorf("darwinPlist missing %q\n%s", sub, p)
		}
	}
	if strings.Contains(p, "--minimized") {
		t.Errorf("darwinPlist(false) should not contain --minimized\n%s", p)
	}
	if !strings.Contains(darwinPlist("/x/niuniu-desktop", true), "<string>--minimized</string>") {
		t.Errorf("darwinPlist(true) missing --minimized arg")
	}
}

func TestLinuxDesktopEntry(t *testing.T) {
	d := linuxDesktopEntry("/opt/niuniu/niuniu-desktop", false)
	for _, sub := range []string{
		"[Desktop Entry]",
		`Exec="/opt/niuniu/niuniu-desktop" --autostart`,
		"X-GNOME-Autostart-enabled=true",
	} {
		if !strings.Contains(d, sub) {
			t.Errorf("linuxDesktopEntry missing %q\n%s", sub, d)
		}
	}
	if strings.Contains(d, "--minimized") {
		t.Errorf("linuxDesktopEntry(false) should not contain --minimized")
	}
	if !strings.Contains(linuxDesktopEntry("/opt/niuniu/niuniu-desktop", true),
		`Exec="/opt/niuniu/niuniu-desktop" --autostart --minimized`) {
		t.Errorf("linuxDesktopEntry(true) missing --minimized in Exec")
	}
}

func TestClassify(t *testing.T) {
	render := func(min bool) string {
		if min {
			return "X --minimized"
		}
		return "X"
	}
	if en, min := classify("X", render, exactEq); !en || min {
		t.Errorf("classify(X) = (%v,%v), want (true,false)", en, min)
	}
	if en, min := classify("X --minimized", render, exactEq); !en || !min {
		t.Errorf("classify(X --minimized) = (%v,%v), want (true,true)", en, min)
	}
	if en, min := classify("other", render, exactEq); en || min {
		t.Errorf("classify(other) = (%v,%v), want (false,false)", en, min)
	}
}

// TestManagerRoundTrip exercises the file-based platforms (darwin/linux) end to
// end in an isolated temp dir. It is skipped on Windows to avoid mutating the
// real HKCU Run key during tests; the Windows value rendering is covered by
// TestWindowsRunValue and the registry calls are thin standard-library wrappers.
func TestManagerRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skip registry mutation on windows host")
	}
	tmp := t.TempDir()
	// Linux resolves via XDG_CONFIG_HOME; darwin via the home seam below.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))

	m := New(filepath.Join(tmp, "bin", "niuniu-desktop"))
	m.home = func() (string, error) { return tmp, nil }

	assert := func(stage string, wantEn, wantMin bool) {
		en, min, err := m.Status()
		if err != nil {
			t.Fatalf("%s: Status err: %v", stage, err)
		}
		if en != wantEn || min != wantMin {
			t.Fatalf("%s: Status = (%v,%v), want (%v,%v)", stage, en, min, wantEn, wantMin)
		}
	}

	assert("initial", false, false)

	if err := m.Enable(false); err != nil {
		t.Fatalf("Enable(false): %v", err)
	}
	assert("after Enable(false)", true, false)

	// Re-enabling with minimized=true flips the flag in place.
	if err := m.Enable(true); err != nil {
		t.Fatalf("Enable(true): %v", err)
	}
	assert("after Enable(true)", true, true)

	if err := m.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	assert("after Disable", false, false)

	// Double-disable is a no-op, not an error.
	if err := m.Disable(); err != nil {
		t.Fatalf("second Disable: %v", err)
	}
}
