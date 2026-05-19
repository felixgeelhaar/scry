// Embedding-backed schema search is the v0.7 wedge upgrade flagged
// by the v0.6 audit: BM25 alone misses agents that paraphrase
// ("customer contact info" vs the literal `email`). The full
// implementation needs sqlite-vec (loadable extension) + an on-disk
// ONNX model (all-MiniLM-L6-v2, ~90MB) for query + unit embedding.
//
// v0.7 ships the seam — Embedder interface + nil-default — so the
// rest of the codebase doesn't need to change when v0.8 lands the
// real model. With Embedder == nil, Store.Search keeps the BM25
// path unchanged.
//
// Real production implementations should:
//
//  1. Load sqlite-vec via db.Exec("SELECT load_extension('vec0')")
//     after open, with a degrade-to-BM25 fallback when the extension
//     binary isn't present.
//  2. Embed each SearchUnit's `composed` field at Replace time +
//     persist the vector to an `embeddings(name, vec)` virtual
//     table.
//  3. At Search time: embed the query, run a vec0 KNN lookup,
//     combine with BM25 via hybrid score α·BM25 + (1−α)·cosine.

package schema

// Embedder is the optional plug-in that produces a fixed-dimension
// embedding for a piece of text. Production implementations wrap an
// ONNX model (all-MiniLM-L6-v2 → 384-dim) loaded once at boot.
//
// A nil Embedder leaves Store.Search on its existing BM25-only
// path — every embedding-related branch checks for nil before
// invocation.
type Embedder interface {
	// Embed returns the unit-norm embedding for s. Implementations
	// may return an error for OOM / model-load failure; callers
	// MUST fall back to BM25 rather than failing the search.
	Embed(s string) ([]float32, error)
	// Dimension is the fixed vector size the implementation
	// produces. Exposed so the store can allocate vec0 column
	// types accordingly.
	Dimension() int
}

// CosineSimilarity returns the cosine similarity between two
// embeddings. Returns 0 for any dimension mismatch + for empty
// vectors. Pure-Go so the bench can use it without sqlite-vec.
//
// Both vectors are assumed unit-norm (the Embedder contract
// guarantees this) — otherwise this function still computes a
// valid dot product but the result won't be in the [-1, 1] range.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}

// HybridScore blends BM25 + cosine similarity per the wedge design.
// alpha=1 → pure BM25; alpha=0 → pure cosine. Default 0.5.
//
// BM25 in sqlite-fts5 is NEGATIVE (smaller = better match) so the
// caller MUST normalise to a positive scale (1/(1+|bm25|)) before
// blending. We do that here so call sites stay simple.
func HybridScore(bm25, cosine, alpha float32) float32 {
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}
	normBM25 := float32(1) / (1 + absF(bm25))
	return alpha*normBM25 + (1-alpha)*cosine
}

func absF(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
