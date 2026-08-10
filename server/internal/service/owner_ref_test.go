package service

import (
	"path/filepath"
	"testing"
)

func TestOwnerRefPaths(t *testing.T) {
	dataDir := filepath.FromSlash("/data/niuniu")

	user := OwnerRef{Type: "user", ID: 42}
	org := OwnerRef{Type: "org", ID: 7}

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"user root", user.Root(dataDir), filepath.FromSlash("/data/niuniu/users/42")},
		{"org root", org.Root(dataDir), filepath.FromSlash("/data/niuniu/orgs/7")},
		{"user ws dir", user.WorkspacesDir(dataDir), filepath.FromSlash("/data/niuniu/users/42/workspaces")},
		{"org repo dir", org.RepositoriesDir(dataDir), filepath.FromSlash("/data/niuniu/orgs/7/repositories")},
		{"user ws path", user.WorkspacePath(dataDir, 9), filepath.FromSlash("/data/niuniu/users/42/workspaces/9")},
		{"org repo path", org.RepositoryPath(dataDir, 3), filepath.FromSlash("/data/niuniu/orgs/7/repositories/3")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q want %q", tc.got, tc.want)
			}
		})
	}
}

func TestOwnerRefValidate(t *testing.T) {
	if err := (OwnerRef{Type: "user", ID: 1}).Validate(); err != nil {
		t.Errorf("valid user rejected: %v", err)
	}
	if err := (OwnerRef{Type: "org", ID: 1}).Validate(); err != nil {
		t.Errorf("valid org rejected: %v", err)
	}
	if err := (OwnerRef{Type: "nope", ID: 1}).Validate(); err == nil {
		t.Error("invalid type accepted")
	}
	if err := (OwnerRef{Type: "user", ID: 0}).Validate(); err == nil {
		t.Error("zero id accepted")
	}
}
