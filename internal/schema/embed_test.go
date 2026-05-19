package schema

import "testing"

func TestCosineSimilarityIdentical(t *testing.T) {
	a := []float32{0.6, 0.8}
	if got := CosineSimilarity(a, a); got < 0.999 || got > 1.001 {
		t.Errorf("self-similarity should be ~1, got %v", got)
	}
}

func TestCosineSimilarityOrthogonal(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	if got := CosineSimilarity(a, b); got != 0 {
		t.Errorf("orthogonal unit vectors should be 0, got %v", got)
	}
}

func TestCosineSimilarityDimensionMismatchReturnsZero(t *testing.T) {
	if got := CosineSimilarity([]float32{1, 2, 3}, []float32{1, 2}); got != 0 {
		t.Errorf("dimension mismatch should be 0, got %v", got)
	}
	if got := CosineSimilarity(nil, []float32{1}); got != 0 {
		t.Errorf("nil should be 0")
	}
}

func TestHybridScoreClampsAlpha(t *testing.T) {
	// alpha=2 → clamped to 1 (pure BM25).
	got := HybridScore(-1, 0.5, 2)
	if got < 0.499 || got > 0.501 {
		t.Errorf("alpha>1 should clamp to 1; got %v", got)
	}
	// alpha=-1 → clamped to 0 (pure cosine).
	got = HybridScore(-1, 0.7, -1)
	if got < 0.699 || got > 0.701 {
		t.Errorf("alpha<0 should clamp to 0; got %v", got)
	}
}

func TestHybridScoreBlendsBothSignals(t *testing.T) {
	// BM25 = -1 → norm = 1/(1+1) = 0.5. Cosine = 0.6. alpha=0.5.
	// Result = 0.5*0.5 + 0.5*0.6 = 0.55.
	got := HybridScore(-1, 0.6, 0.5)
	if got < 0.549 || got > 0.551 {
		t.Errorf("hybrid score = %v, want ~0.55", got)
	}
}
