package hotkey

import "testing"

func TestParseAcceleratorValid(t *testing.T) {
	cases := []struct {
		spec  string
		label string
	}{
		{"Ctrl+Shift+Z", "Ctrl+Shift+Z"},
		{"ctrl+shift+z", "Ctrl+Shift+Z"},
		{" Ctrl + Shift + Z ", "Ctrl+Shift+Z"},
		{"Alt+Shift+A", "Alt+Shift+A"},
		{"Ctrl+Shift+Space", "Ctrl+Shift+Space"},
		{"Ctrl+Alt+9", "Ctrl+Alt+9"},
	}
	for _, c := range cases {
		mods, _, label, err := ParseAccelerator(c.spec)
		if err != nil {
			t.Fatalf("ParseAccelerator(%q) unexpected error: %v", c.spec, err)
		}
		if len(mods) == 0 {
			t.Fatalf("ParseAccelerator(%q) returned no modifiers", c.spec)
		}
		if label != c.label {
			t.Errorf("ParseAccelerator(%q) label = %q, want %q", c.spec, label, c.label)
		}
	}
}

func TestParseAcceleratorInvalid(t *testing.T) {
	for _, spec := range []string{
		"",            // empty
		"Z",           // no modifier
		"Ctrl+Shift",  // no key
		"Ctrl+A+B",    // two keys
		"Ctrl+Shift+§", // unknown token
	} {
		if _, _, _, err := ParseAccelerator(spec); err == nil {
			t.Errorf("ParseAccelerator(%q) expected error, got nil", spec)
		}
	}
}
