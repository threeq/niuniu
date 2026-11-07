package channel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestManualAdapterReportsUnavailableCapabilities(t *testing.T) {
	var a ChannelAdapter = NewManualAdapter(t.TempDir())
	if !a.Capabilities()[CapPublish] {
		t.Fatal("manual adapter must support publish package")
	}
	if _, err := a.ListEngagements(context.Background(), ""); !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("want ErrCapabilityUnavailable, got %v", err)
	}
}

func TestAdapterErrorCarriesRetryability(t *testing.T) {
	err := &AdapterError{Capability: CapPublish, Code: "rate_limited", Retryable: true}
	if !err.Retryable || !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatal("adapter error must expose retryability")
	}
}

func TestManualAdapterPublishesPackageToDisk(t *testing.T) {
	root := t.TempDir()
	a := NewManualAdapter(root)

	receipt, err := a.Publish(context.Background(), ManualPublishPayload{
		Bot:            "acct-1",
		IdempotencyKey: "idem-123",
		Platform:       "xiaohongshu",
		Title:          "标题",
		Body:           "正文",
		MediaPaths:     []string{"/tmp/a.png"},
		Params:         map[string]string{"tags": "运营"},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	dir := filepath.Join(root, "manual", "acct-1", "idem-123")
	if receipt.ExternalID != "manual:"+dir {
		t.Fatalf("receipt ExternalID = %q, want %q", receipt.ExternalID, "manual:"+dir)
	}

	data, err := os.ReadFile(filepath.Join(dir, "payload.json"))
	if err != nil {
		t.Fatalf("read package: %v", err)
	}
	var got ManualPublishPayload
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode package: %v", err)
	}
	if got.Title != "标题" || got.Body != "正文" || len(got.MediaPaths) != 1 || got.Params["tags"] != "运营" {
		t.Fatalf("package lost fields: %+v", got)
	}
}

func TestManualAdapterRejectsUnknownPayload(t *testing.T) {
	a := NewManualAdapter(t.TempDir())
	if _, err := a.Publish(context.Background(), "not-a-payload"); !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("want adapter error, got %v", err)
	}
}

func TestManualAdapterKeepsPackageInsideRoot(t *testing.T) {
	root := t.TempDir()
	a := NewManualAdapter(root)

	receipt, err := a.Publish(context.Background(), ManualPublishPayload{
		Bot:            "../../escape",
		IdempotencyKey: "../../etc",
		Title:          "x",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	want := filepath.Join(root, "manual", ".._.._escape", ".._.._etc")
	if receipt.ExternalID != "manual:"+want {
		t.Fatalf("receipt ExternalID = %q, want %q", receipt.ExternalID, "manual:"+want)
	}
}
