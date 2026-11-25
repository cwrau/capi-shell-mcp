package cache

import (
	"testing"
	"time"
)

func TestGetReturnsFalseForMissingKey(t *testing.T) {
	c := New[string, string](60 * time.Second)
	if _, ok := c.Get("missing"); ok {
		t.Fatal("expected ok=false for missing key")
	}
}

func TestGetReturnsValueWithinTTL(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[string, string](60 * time.Second)
	c.now = func() time.Time { return now }

	c.Set("k", "v")
	now = now.Add(59 * time.Second)

	v, ok := c.Get("k")
	if !ok || v != "v" {
		t.Fatalf("got (%q, %v), want (\"v\", true)", v, ok)
	}
}

func TestGetReturnsFalseAfterTTLExpires(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[string, string](60 * time.Second)
	c.now = func() time.Time { return now }

	c.Set("k", "v")
	now = now.Add(61 * time.Second)

	if _, ok := c.Get("k"); ok {
		t.Fatal("expected ok=false after TTL expiry")
	}
}

func TestDeleteRemovesEntry(t *testing.T) {
	c := New[string, string](60 * time.Second)
	c.Set("k", "v")
	c.Delete("k")

	if _, ok := c.Get("k"); ok {
		t.Fatal("expected ok=false after delete")
	}
}

func TestClearRemovesAllEntries(t *testing.T) {
	c := New[string, string](60 * time.Second)
	c.Set("a", "1")
	c.Set("b", "2")
	c.Clear()

	if _, ok := c.Get("a"); ok {
		t.Fatal("expected ok=false for a after clear")
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal("expected ok=false for b after clear")
	}
}

func TestSetOverwritesExistingEntryAndResetsTTL(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[string, string](60 * time.Second)
	c.now = func() time.Time { return now }

	c.Set("k", "old")
	now = now.Add(50 * time.Second)
	c.Set("k", "new")
	now = now.Add(50 * time.Second)

	v, ok := c.Get("k")
	if !ok || v != "new" {
		t.Fatalf("got (%q, %v), want (\"new\", true)", v, ok)
	}
}

func TestSetTTLAppliesToLaterSetsOnly(t *testing.T) {
	now := time.Unix(0, 0)
	c := New[string, string](60 * time.Second)
	c.now = func() time.Time { return now }

	c.Set("before", "v")
	c.SetTTL(10 * time.Second)
	c.Set("after", "v")
	now = now.Add(30 * time.Second)

	if _, ok := c.Get("before"); !ok {
		t.Error("entry set before SetTTL expired early, it should keep its 60s TTL")
	}
	if _, ok := c.Get("after"); ok {
		t.Error("entry set after SetTTL(10s) is still there after 30s")
	}
}
