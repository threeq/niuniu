package webhooksource

import (
	"net/http"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

func TestNormalizeCIGitLabFailure(t *testing.T) {
	body := []byte(`{
	  "object_kind":"pipeline",
	  "object_attributes":{"id":123,"status":"failed","ref":"main","sha":"abc"},
	  "project":{"path_with_namespace":"acme/api"},
	  "merge_request":{"iid":42},
	  "user":{"id":7,"name":"Alice"}
	}`)
	trig, ok, err := NormalizeCI(1, "gitlab/main", "gitlab", body)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !ok {
		t.Fatal("event was filtered")
	}
	if trig.SourceKind != intake.SourceKindCI {
		t.Fatalf("source kind = %q", trig.SourceKind)
	}
	if trig.EventKind != intake.EventKindCIFailed {
		t.Fatalf("event kind = %q", trig.EventKind)
	}
	if trig.EventID != "gitlab:pipeline:123:failed:abc" {
		t.Fatalf("event id = %q", trig.EventID)
	}
	if trig.Thread.ThreadExtID != "acme/api#pr:42" {
		t.Fatalf("thread = %q", trig.Thread.ThreadExtID)
	}
	if trig.Actor.ExtID != "7" || trig.Actor.Name != "Alice" {
		t.Fatalf("actor = %+v", trig.Actor)
	}
	if !trig.ReceiveAll || trig.Explicit {
		t.Fatalf("flags = receiveAll:%v explicit:%v", trig.ReceiveAll, trig.Explicit)
	}
	if trig.SourceRef != "gitlab/main" {
		t.Fatalf("source ref = %q", trig.SourceRef)
	}
}

func TestNormalizeCIIgnoresFilteredStatus(t *testing.T) {
	for _, status := range []string{"running", "pending", "created", "skipped", "manual"} {
		body := []byte(`{"object_kind":"pipeline","object_attributes":{"id":1,"status":"` + status + `"}}`)
		_, ok, err := NormalizeCI(1, "gitlab", "gitlab", body)
		if err != nil {
			t.Fatalf("normalize %s: %v", status, err)
		}
		if ok {
			t.Fatalf("status %q must be ignored", status)
		}
	}
	if _, ok, err := NormalizeCI(1, "jenkins", "jenkins", []byte(`{"status":"running"}`)); err != nil || ok {
		t.Fatalf("jenkins running = ok:%v err:%v", ok, err)
	}
}

func TestNormalizeCISuccessAndRecovery(t *testing.T) {
	success := []byte(`{
	  "object_kind":"pipeline",
	  "object_attributes":{"id":123,"status":"success","ref":"main","sha":"abc"},
	  "project":{"path_with_namespace":"acme/api"}
	}`)
	trig, ok, err := NormalizeCI(1, "ref", "gitlab", success)
	if err != nil || !ok {
		t.Fatalf("success = ok:%v err:%v", ok, err)
	}
	if trig.EventKind != intake.EventKindCISucceeded {
		t.Fatalf("event kind = %q", trig.EventKind)
	}
	// Without a PR the thread falls back to repo#branch#commit.
	if trig.Thread.ThreadExtID != "acme/api#main#abc" {
		t.Fatalf("thread = %q", trig.Thread.ThreadExtID)
	}

	recovered := []byte(`{
	  "object_kind":"pipeline",
	  "object_attributes":{"id":123,"status":"success","previous_status":"failed","ref":"main","sha":"abc"},
	  "project":{"path_with_namespace":"acme/api"}
	}`)
	trig, ok, err = NormalizeCI(1, "ref", "gitlab", recovered)
	if err != nil || !ok {
		t.Fatalf("recovered = ok:%v err:%v", ok, err)
	}
	if trig.EventKind != intake.EventKindCIRecovered {
		t.Fatalf("event kind = %q, want recovered", trig.EventKind)
	}

	canceled := []byte(`{
	  "object_kind":"pipeline",
	  "object_attributes":{"id":123,"status":"canceled","ref":"main","sha":"abc"},
	  "project":{"path_with_namespace":"acme/api"}
	}`)
	trig, ok, err = NormalizeCI(1, "ref", "gitlab", canceled)
	if err != nil || !ok {
		t.Fatalf("canceled = ok:%v err:%v", ok, err)
	}
	if trig.EventKind != intake.EventKindCICanceled {
		t.Fatalf("event kind = %q", trig.EventKind)
	}
}

func TestNormalizeCIGeneric(t *testing.T) {
	body := []byte(`{
	  "id":"evt-9",
	  "event":"ci_failed",
	  "repository":"acme/api",
	  "branch":"dev",
	  "commit":"deadbeef",
	  "pipeline":{"id":"p1","name":"build","url":"https://ci/p1"},
	  "job":{"id":"j1","name":"test","url":"https://ci/j1"},
	  "status":"failed",
	  "summary":"2 tests failed",
	  "log_excerpt":"boom",
	  "actor":{"id":"u1","name":"Alice"},
	  "url":"https://ci/j1"
	}`)
	trig, ok, err := NormalizeCI(3, "hooks", "generic", body)
	if err != nil || !ok {
		t.Fatalf("normalize = ok:%v err:%v", ok, err)
	}
	if trig.EventID != "evt-9" || trig.EventKind != intake.EventKindCIFailed {
		t.Fatalf("event = %s/%s", trig.EventID, trig.EventKind)
	}
	if trig.Thread.ThreadExtID != "acme/api#dev#deadbeef" {
		t.Fatalf("thread = %q", trig.Thread.ThreadExtID)
	}
	if trig.Actor.ExtID != "u1" || trig.Actor.Name != "Alice" || trig.Actor.IsBot {
		t.Fatalf("actor = %+v", trig.Actor)
	}
	if trig.SourceURL != "https://ci/j1" {
		t.Fatalf("source url = %q", trig.SourceURL)
	}
	for _, want := range []string{"2 tests failed", "pipeline: build", "job: test", "branch: dev", "commit: deadbeef", "boom"} {
		if !strings.Contains(trig.Text, want) {
			t.Fatalf("text %q missing %q", trig.Text, want)
		}
	}
}

func TestNormalizeCIGenericRecoveredAndBotActor(t *testing.T) {
	body := []byte(`{"event":"ci_succeeded","previous_status":"failed","repository":"acme/api","branch":"main","commit":"c1"}`)
	trig, ok, err := NormalizeCI(1, "hooks", "generic", body)
	if err != nil || !ok {
		t.Fatalf("normalize = ok:%v err:%v", ok, err)
	}
	if trig.EventKind != intake.EventKindCIRecovered {
		t.Fatalf("event kind = %q", trig.EventKind)
	}
	if trig.Actor.ExtID != "ci:generic" || !trig.Actor.IsBot {
		t.Fatalf("actor = %+v", trig.Actor)
	}
}

func TestNormalizeCIJenkins(t *testing.T) {
	body := []byte(`{"id":"jenkins-1","status":"SUCCESS","repository":"acme/api","branch":"main","commit":"c1"}`)
	trig, ok, err := NormalizeCI(1, "jenkins", "jenkins", body)
	if err != nil || !ok {
		t.Fatalf("normalize = ok:%v err:%v", ok, err)
	}
	if trig.EventKind != intake.EventKindCISucceeded {
		t.Fatalf("event kind = %q", trig.EventKind)
	}

	aborted := []byte(`{"id":"jenkins-2","status":"ABORTED","repository":"acme/api"}`)
	trig, ok, err = NormalizeCI(1, "jenkins", "jenkins", aborted)
	if err != nil || !ok {
		t.Fatalf("aborted = ok:%v err:%v", ok, err)
	}
	if trig.EventKind != intake.EventKindCICanceled {
		t.Fatalf("event kind = %q", trig.EventKind)
	}
}

func TestNormalizeCIBuildkite(t *testing.T) {
	body := []byte(`{
	  "event":"build.finished",
	  "build":{"id":"b1","number":12,"state":"passed","branch":"main","commit":"abc",
	           "message":"Fix flaky test","web_url":"https://buildkite.com/acme/api/builds/12"},
	  "pipeline":{"id":"p1","name":"ci","repository":"git@github.com:acme/api.git"},
	  "sender":{"id":"u9","name":"Bob"}
	}`)
	trig, ok, err := NormalizeCI(1, "bk", "buildkite", body)
	if err != nil || !ok {
		t.Fatalf("normalize = ok:%v err:%v", ok, err)
	}
	if trig.EventKind != intake.EventKindCISucceeded {
		t.Fatalf("event kind = %q", trig.EventKind)
	}
	if trig.Thread.ThreadExtID != "acme/api#main#abc" {
		t.Fatalf("thread = %q", trig.Thread.ThreadExtID)
	}
	if trig.Actor.ExtID != "u9" || trig.Actor.Name != "Bob" {
		t.Fatalf("actor = %+v", trig.Actor)
	}
	if trig.SourceURL != "https://buildkite.com/acme/api/builds/12" {
		t.Fatalf("source url = %q", trig.SourceURL)
	}
	if !strings.Contains(trig.Text, "Fix flaky test") {
		t.Fatalf("text = %q", trig.Text)
	}

	failed := []byte(`{"event":"build.finished","build":{"state":"failed","branch":"main"},"pipeline":{"repository":"acme/api"}}`)
	trig, ok, err = NormalizeCI(1, "bk", "buildkite", failed)
	if err != nil || !ok {
		t.Fatalf("failed = ok:%v err:%v", ok, err)
	}
	if trig.EventKind != intake.EventKindCIFailed {
		t.Fatalf("event kind = %q", trig.EventKind)
	}
}

func TestNormalizeCICircleCI(t *testing.T) {
	body := []byte(`{
	  "type":"workflow-completed",
	  "id":"wf-evt-1",
	  "happened_at":"2026-11-06T10:00:00Z",
	  "workflow":{"id":"wf1","name":"build","status":"failed","url":"https://circleci.com/wf1",
	    "pipeline":{"id":"p1","vcs":{"branch":"main","revision":"abc"},
	                "repository":{"full_name":"acme/api"}}}
	}`)
	trig, ok, err := NormalizeCI(1, "circle", "circleci", body)
	if err != nil || !ok {
		t.Fatalf("normalize = ok:%v err:%v", ok, err)
	}
	if trig.EventID != "wf-evt-1" || trig.EventKind != intake.EventKindCIFailed {
		t.Fatalf("event = %s/%s", trig.EventID, trig.EventKind)
	}
	if trig.Thread.ThreadExtID != "acme/api#main#abc" {
		t.Fatalf("thread = %q", trig.Thread.ThreadExtID)
	}
	if trig.Actor.ExtID != "ci:circleci" || !trig.Actor.IsBot {
		t.Fatalf("actor = %+v", trig.Actor)
	}
	if trig.ReceivedAt.IsZero() {
		t.Fatal("happened_at was not parsed")
	}

	canceled := []byte(`{"type":"workflow-completed","workflow":{"status":"canceled","pipeline":{"id":"p1"}}}`)
	trig, ok, err = NormalizeCI(1, "circle", "circleci", canceled)
	if err != nil || !ok {
		t.Fatalf("canceled = ok:%v err:%v", ok, err)
	}
	if trig.EventKind != intake.EventKindCICanceled {
		t.Fatalf("event kind = %q", trig.EventKind)
	}
}

func TestNormalizeCITruncatesLogExcerpt(t *testing.T) {
	log := strings.Repeat("a", CIMaxLogExcerpt+1024)
	body := []byte(`{"status":"failed","log_excerpt":"` + log + `"}`)
	trig, ok, err := NormalizeCI(1, "hooks", "generic", body)
	if err != nil || !ok {
		t.Fatalf("normalize = ok:%v err:%v", ok, err)
	}
	if len(trig.Text) != CIMaxLogExcerpt {
		t.Fatalf("text length = %d, want %d", len(trig.Text), CIMaxLogExcerpt)
	}
}

func TestNormalizeCIUnknownProvider(t *testing.T) {
	if _, _, err := NormalizeCI(1, "x", "travis", []byte(`{}`)); err == nil {
		t.Fatal("unknown provider must error")
	}
}

func TestNormalizeCIThreadPreference(t *testing.T) {
	body := []byte(`{"status":"failed","repository":"acme/api","branch":"main","commit":"abc","thread_id":"custom-thread"}`)
	trig, ok, err := NormalizeCI(1, "hooks", "generic", body)
	if err != nil || !ok {
		t.Fatalf("normalize = ok:%v err:%v", ok, err)
	}
	if trig.Thread.ThreadExtID != "custom-thread" {
		t.Fatalf("thread = %q", trig.Thread.ThreadExtID)
	}
}

func TestCIProviderPresetCredential(t *testing.T) {
	tests := []struct {
		provider string
		header   string
		value    string
		wantMode string
		want     string
	}{
		{"gitlab", "X-Gitlab-Token", "s3cret", CIAuthToken, "s3cret"},
		{"buildkite", "X-Buildkite-Token", "bk-token", CIAuthToken, "bk-token"},
		{"generic", "X-Niuniu-Signature-256", "sha256=ABCDEF", CIAuthHMAC, "sha256=ABCDEF"},
		{"generic", "Authorization", "Bearer bearer-token", CIAuthBearer, "bearer-token"},
		{"jenkins", "X-Niuniu-Token", "jenkins-token", CIAuthToken, "jenkins-token"},
		{"jenkins", "Authorization", "Bearer jenkins-bearer", CIAuthBearer, "jenkins-bearer"},
		{"circleci", "Circleci-Signature", "deadbeef", CIAuthHMAC, "deadbeef"},
	}
	for _, tc := range tests {
		preset, ok := CIProviderPresetFor(tc.provider)
		if !ok {
			t.Fatalf("no preset for %q", tc.provider)
		}
		h := http.Header{}
		h.Set(tc.header, tc.value)
		mode, value, found := preset.Credential(h)
		if !found || mode != tc.wantMode || value != tc.want {
			t.Fatalf("%s %s = (%q,%q,%v), want (%q,%q,true)",
				tc.provider, tc.header, mode, value, found, tc.wantMode, tc.want)
		}
	}

	if _, ok := CIProviderPresetFor("travis"); ok {
		t.Fatal("unknown provider must not resolve")
	}
	// An empty provider name resolves to the generic contract.
	if preset, ok := CIProviderPresetFor(""); !ok || preset.Provider != CIProviderGeneric {
		t.Fatalf("empty provider = %+v, ok=%v", preset, ok)
	}
	if got := CISignatureHex("sha256=ABC"); got != "abc" {
		t.Fatalf("signature normalization = %q", got)
	}
}
