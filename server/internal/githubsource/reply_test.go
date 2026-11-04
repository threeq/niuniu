package githubsource

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPostComment_PostsToTheThreadWithCredential(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		var payload struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotBody = payload.Body
		_, _ = w.Write([]byte(`{"id":7,"html_url":"https://github.com/acme/foo/pull/42#issuecomment-7"}`))
	}))
	defer srv.Close()

	comment, err := PostComment(context.Background(), srv.Client(), srv.URL, ReplyConfig{
		Repo: "acme/foo", Number: "42", Token: "ghp_x", Body: "  fixed\n",
	})
	if err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if gotPath != "/repos/acme/foo/issues/42/comments" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer ghp_x" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotBody != "fixed" {
		t.Fatalf("body = %q", gotBody)
	}
	if comment.ID != 7 || comment.URL != "https://github.com/acme/foo/pull/42#issuecomment-7" {
		t.Fatalf("comment = %+v", comment)
	}
}

func TestPostComment_SurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	defer srv.Close()

	_, err := PostComment(context.Background(), srv.Client(), srv.URL, ReplyConfig{
		Repo: "acme/foo", Number: "1", Token: "ghp_x", Body: "hi",
	})
	if err == nil {
		t.Fatal("403 must be an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
}

func TestTruncateReply_ByteCappedAtRuneBoundary(t *testing.T) {
	short := "hello"
	if got := truncateReply(short); got != short {
		t.Fatalf("short changed: %q", got)
	}

	long := strings.Repeat("好", MaxReplyLen) // 3 bytes per rune
	out := truncateReply(long)
	if !utf8.ValidString(out) {
		t.Fatal("truncation split a multi-byte character")
	}
	if len(out) <= MaxReplyLen {
		t.Fatalf("expected a marker after the cap, len=%d", len(out))
	}
	if !strings.HasSuffix(out, "…（回复过长，已截断）") {
		t.Fatalf("missing truncation marker: %q", out[len(out)-20:])
	}
	// The marker sits past the cap, but must stay well under GitHub's 65536.
	if len(out) > 65536 {
		t.Fatalf("truncated body still too large: %d", len(out))
	}
}
