package credstore

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestCredstore_RoundTrip(t *testing.T) {
	keyring.MockInit()

	if _, err := Load("alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load on empty store: want ErrNotFound, got %v", err)
	}

	in := &Creds{
		URL:          "https://relay.example.com",
		Email:        "alice@example.com",
		RefreshToken: "rt-xyz",
		DesktopID:    "d-1",
		DesktopToken: "dt-1",
	}
	if err := Save("alice", in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load("alice")
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if got.URL != in.URL || got.Email != in.Email ||
		got.RefreshToken != in.RefreshToken ||
		got.DesktopID != in.DesktopID || got.DesktopToken != in.DesktopToken {
		t.Fatalf("roundtrip mismatch: got %+v want %+v", got, in)
	}

	if err := Clear("alice"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := Load("alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after Clear: want ErrNotFound, got %v", err)
	}
	if err := Clear("alice"); err != nil {
		t.Fatalf("idempotent Clear returned %v", err)
	}
}

func TestCredstore_ScopeIsolation(t *testing.T) {
	// Separate scopes must not see each other's entries.
	keyring.MockInit()
	if err := Save("alice", &Creds{URL: "a", Email: "a@a", RefreshToken: "rt-a"}); err != nil {
		t.Fatalf("Save alice: %v", err)
	}
	if err := Save("bob", &Creds{URL: "b", Email: "b@b", RefreshToken: "rt-b"}); err != nil {
		t.Fatalf("Save bob: %v", err)
	}

	aliceGot, err := Load("alice")
	if err != nil {
		t.Fatalf("Load alice: %v", err)
	}
	if aliceGot.Email != "a@a" {
		t.Fatalf("alice saw %q, want a@a", aliceGot.Email)
	}
	bobGot, err := Load("bob")
	if err != nil {
		t.Fatalf("Load bob: %v", err)
	}
	if bobGot.Email != "b@b" {
		t.Fatalf("bob saw %q, want b@b", bobGot.Email)
	}

	// Clearing one scope must not touch the other.
	if err := Clear("alice"); err != nil {
		t.Fatalf("Clear alice: %v", err)
	}
	if _, err := Load("alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("alice after Clear: want ErrNotFound, got %v", err)
	}
	if _, err := Load("bob"); err != nil {
		t.Fatalf("bob after alice-Clear still loadable: got %v", err)
	}
}

func TestCredstore_SaveOverwrites(t *testing.T) {
	keyring.MockInit()
	_ = Save("u", &Creds{URL: "a", Email: "a@a", RefreshToken: "rt-a"})
	if err := Save("u", &Creds{URL: "b", Email: "b@b", RefreshToken: "rt-b"}); err != nil {
		t.Fatalf("overwrite Save: %v", err)
	}
	got, err := Load("u")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Email != "b@b" {
		t.Fatalf("overwrite did not stick: email=%q", got.Email)
	}
}

func TestCredstore_EmptyScopeFallsBackToDefault(t *testing.T) {
	// "" and "default" must alias to the same entry for smooth migration
	// from the old single-scope API to the new multi-user one.
	keyring.MockInit()
	if err := Save("", &Creds{URL: "u", Email: "e@e", RefreshToken: "rt-e"}); err != nil {
		t.Fatalf("Save empty: %v", err)
	}
	got, err := Load(DefaultScope)
	if err != nil {
		t.Fatalf("Load default: %v", err)
	}
	if got.Email != "e@e" {
		t.Fatalf("default saw %q", got.Email)
	}
}
