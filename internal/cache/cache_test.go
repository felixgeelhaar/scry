package cache

import (
	"testing"
	"time"
)

func TestKeyDeterministicAcrossVariableOrder(t *testing.T) {
	a := Key(`{ ping }`, map[string]any{"x": 1, "y": "two"}, "P")
	b := Key(`{ ping }`, map[string]any{"y": "two", "x": 1}, "P")
	if a != b {
		t.Errorf("key should not depend on map iteration order: %q vs %q", a, b)
	}
}

func TestKeyDiffersByEveryInput(t *testing.T) {
	base := Key(`{ a }`, nil, "")
	cases := map[string]string{
		"different query":     Key(`{ b }`, nil, ""),
		"different vars":      Key(`{ a }`, map[string]any{"x": 1}, ""),
		"different operation": Key(`{ a }`, nil, "Op"),
	}
	for name, v := range cases {
		if v == base {
			t.Errorf("%s: key collided with base", name)
		}
	}
}

func TestGetSetRoundtrip(t *testing.T) {
	c := New(time.Minute, 10)
	c.Set("k1", []byte("v1"))
	got, ok := c.Get("k1")
	if !ok {
		t.Fatalf("expected hit")
	}
	if string(got) != "v1" {
		t.Errorf("value = %q, want v1", got)
	}
}

func TestMissForUnknownKey(t *testing.T) {
	c := New(time.Minute, 10)
	if _, ok := c.Get("nope"); ok {
		t.Errorf("expected miss")
	}
}

func TestTTLExpiry(t *testing.T) {
	c := New(50*time.Millisecond, 10)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	c.Set("k", []byte("v"))
	if _, ok := c.Get("k"); !ok {
		t.Fatalf("expected hit immediately after Set")
	}
	now = now.Add(60 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Errorf("expected miss after TTL elapsed")
	}
	if c.Len() != 0 {
		t.Errorf("expired entry should be evicted on access, Len()=%d", c.Len())
	}
}

func TestZeroTTLDisablesCache(t *testing.T) {
	c := New(0, 10)
	c.Set("k", []byte("v"))
	if _, ok := c.Get("k"); ok {
		t.Errorf("ttl=0 should disable the cache")
	}
	if c.Len() != 0 {
		t.Errorf("Set should be a no-op when ttl=0, Len()=%d", c.Len())
	}
}

func TestMaxEntriesLRUEviction(t *testing.T) {
	c := New(time.Minute, 3)
	c.Set("a", []byte("1"))
	c.Set("b", []byte("2"))
	c.Set("c", []byte("3"))
	// Touch 'a' so 'b' becomes the LRU.
	_, _ = c.Get("a")
	c.Set("d", []byte("4"))
	if _, ok := c.Get("b"); ok {
		t.Errorf("expected LRU 'b' evicted on overflow")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("expected %q to survive eviction", k)
		}
	}
}

func TestMaxZeroDisablesEviction(t *testing.T) {
	c := New(time.Minute, 0)
	for i := 0; i < 100; i++ {
		c.Set(string(rune(i)), []byte("x"))
	}
	if c.Len() != 100 {
		t.Errorf("max=0 should retain everything, Len()=%d", c.Len())
	}
}
