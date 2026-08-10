// Package pet will host the desktop mascot / floating-window effect
// system for the personal edition. No code ships in this iteration.
//
// Before implementing, a follow-up spec must verify Wails v3 alpha.74
// cross-platform support for AlwaysOnTop + Frameless + Transparent
// windows on Windows, macOS, and Linux (X11 + Wayland). Without that
// validation, the pet's separate-window architecture may be unworkable.
package pet
