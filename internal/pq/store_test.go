package pq

import (
	"context"
	"errors"
	"testing"
)

func TestHashStableAcrossRuns(t *testing.T) {
	a := Hash(`{ ping }`)
	b := Hash(`{ ping }`)
	if a != b {
		t.Errorf("hash non-deterministic: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Errorf("hash length = %d, want 64 (SHA-256 hex)", len(a))
	}
}

func TestPutRoundtrip(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()

	q := `{ allFilms { films { title } } }`
	e, err := s.Put(ctx, "all-films", q)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if e.Hash != Hash(q) {
		t.Errorf("returned hash mismatch")
	}

	gotByHash, err := s.GetByHash(ctx, e.Hash)
	if err != nil || gotByHash.Query != q {
		t.Errorf("GetByHash: %+v %v", gotByHash, err)
	}
	gotByName, err := s.GetByName(ctx, "all-films")
	if err != nil || gotByName.Query != q {
		t.Errorf("GetByName: %+v %v", gotByName, err)
	}
}

func TestGetNotFound(t *testing.T) {
	ctx := context.Background()
	s, _ := OpenStore(":memory:")
	defer func() { _ = s.Close() }()

	if _, err := s.GetByHash(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if _, err := s.GetByName(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestPutOverwriteByName(t *testing.T) {
	ctx := context.Background()
	s, _ := OpenStore(":memory:")
	defer func() { _ = s.Close() }()

	e1, _ := s.Put(ctx, "x", `{ a }`)
	e2, _ := s.Put(ctx, "x", `{ b }`)
	if e1.Hash == e2.Hash {
		t.Errorf("overwriting query bytes should change hash")
	}
	// Old hash must no longer resolve — agents holding it get a
	// clean ErrNotFound rather than a stale query.
	if _, err := s.GetByHash(ctx, e1.Hash); !errors.Is(err, ErrNotFound) {
		t.Errorf("old hash should be evicted, got %v", err)
	}
	if _, err := s.GetByName(ctx, "x"); err != nil {
		t.Errorf("name should still resolve to new entry: %v", err)
	}
}

func TestDeleteByHashOrName(t *testing.T) {
	ctx := context.Background()
	s, _ := OpenStore(":memory:")
	defer func() { _ = s.Close() }()

	e1, _ := s.Put(ctx, "a", `{ q1 }`)
	e2, _ := s.Put(ctx, "b", `{ q2 }`)

	if err := s.Delete(ctx, e1.Hash); err != nil {
		t.Errorf("delete by hash: %v", err)
	}
	if _, err := s.GetByName(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("entry 'a' should be gone")
	}
	if err := s.Delete(ctx, "b"); err != nil {
		t.Errorf("delete by name: %v", err)
	}
	if _, err := s.GetByHash(ctx, e2.Hash); !errors.Is(err, ErrNotFound) {
		t.Errorf("entry 'b' should be gone")
	}
	if err := s.Delete(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete of missing entry should return ErrNotFound, got %v", err)
	}
}

func TestListSortedByName(t *testing.T) {
	ctx := context.Background()
	s, _ := OpenStore(":memory:")
	defer func() { _ = s.Close() }()

	_, _ = s.Put(ctx, "zeta", `{ z }`)
	_, _ = s.Put(ctx, "alpha", `{ a }`)
	_, _ = s.Put(ctx, "mu", `{ m }`)

	entries, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	want := []string{"alpha", "mu", "zeta"}
	for i, n := range want {
		if names[i] != n {
			t.Errorf("List[%d] = %q, want %q", i, names[i], n)
		}
	}
}
