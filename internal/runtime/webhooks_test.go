package runtime

import (
	"context"
	"errors"
	"testing"
)

func TestWebhookRegisterAndList(t *testing.T) {
	s, err := OpenWebhookStore(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	w1, err := s.Register(ctx, "https://hooks.example.com/scry-a")
	if err != nil {
		t.Fatalf("register a: %v", err)
	}
	w2, err := s.Register(ctx, "https://hooks.example.com/scry-b")
	if err != nil {
		t.Fatalf("register b: %v", err)
	}
	if w1.ID == w2.ID {
		t.Errorf("ids should be distinct: %d == %d", w1.ID, w2.ID)
	}
	if w1.Secret == "" || w1.Secret == w2.Secret {
		t.Errorf("secrets must be present + distinct; w1=%q w2=%q", w1.Secret, w2.Secret)
	}
	if len(w1.Secret) != 64 {
		t.Errorf("secret should be 32-byte hex (64 chars); got %d", len(w1.Secret))
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("list len = %d, want 2", len(list))
	}
	for _, w := range list {
		if w.Secret != "" {
			t.Errorf("List must NOT expose secret; got %q", w.Secret)
		}
	}
}

func TestWebhookForwardIncludesSecret(t *testing.T) {
	s, err := OpenWebhookStore(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	_, _ = s.Register(ctx, "https://hooks.example.com/scry")
	forward, err := s.Forward(ctx)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if len(forward) != 1 || forward[0].Secret == "" {
		t.Errorf("Forward must expose secret to dispatcher; got %+v", forward)
	}
}

func TestWebhookRemove(t *testing.T) {
	s, err := OpenWebhookStore(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	w, _ := s.Register(ctx, "https://hooks.example.com/scry")
	if err := s.Remove(ctx, w.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := s.Remove(ctx, w.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Errorf("re-removing must surface ErrWebhookNotFound, got %v", err)
	}
}

func TestWebhookRegisterRejectsEmptyURL(t *testing.T) {
	s, err := OpenWebhookStore(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Register(context.Background(), ""); err == nil {
		t.Errorf("empty URL must be rejected")
	}
}

func TestWebhookSurvivesReopen(t *testing.T) {
	// Use a file path so the row survives close + reopen.
	dir := t.TempDir()
	path := dir + "/webhooks.db"
	s1, err := OpenWebhookStore(path)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	_, _ = s1.Register(context.Background(), "https://hooks.example.com/scry")
	_ = s1.Close()

	s2, err := OpenWebhookStore(path)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer func() { _ = s2.Close() }()
	list, err := s2.List(context.Background())
	if err != nil {
		t.Fatalf("list 2: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("registration didn't survive reopen; got %d entries", len(list))
	}
}
