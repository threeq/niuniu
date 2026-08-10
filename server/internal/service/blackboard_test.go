package service

import "testing"

func TestBlackboardWriteAndRead(t *testing.T) {
	bb := NewBlackboard(0, nil, nil)
	bb.Write("test-key", "design", "architect", "content here", "")

	entry, ok := bb.Read("test-key")
	if !ok {
		t.Fatal("expected entry to exist")
	}
	if entry.Content != "content here" {
		t.Errorf("expected 'content here', got '%s'", entry.Content)
	}
	if entry.ProducerAgent != "architect" {
		t.Errorf("expected producer 'architect', got '%s'", entry.ProducerAgent)
	}
}

func TestBlackboardListWithFilter(t *testing.T) {
	bb := NewBlackboard(0, nil, nil)
	bb.Write("k1", "design", "a", "c1", "")
	bb.Write("k2", "code", "b", "c2", "")
	bb.Write("k3", "design", "c", "c3", "")

	all := bb.List("")
	if len(all) != 3 {
		t.Errorf("expected 3, got %d", len(all))
	}
	designs := bb.List("design")
	if len(designs) != 2 {
		t.Errorf("expected 2 designs, got %d", len(designs))
	}
}

func TestBlackboardDelete(t *testing.T) {
	bb := NewBlackboard(0, nil, nil)
	bb.Write("key1", "design", "a", "content", "")
	bb.Delete("key1")
	_, ok := bb.Read("key1")
	if ok {
		t.Error("expected entry to be deleted")
	}
}

func TestBlackboardClear(t *testing.T) {
	bb := NewBlackboard(0, nil, nil)
	bb.Write("k1", "design", "a", "c1", "")
	bb.Write("k2", "code", "b", "c2", "")
	bb.Clear()
	all := bb.List("")
	if len(all) != 0 {
		t.Errorf("expected 0 after clear, got %d", len(all))
	}
}
