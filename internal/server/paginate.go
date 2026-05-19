package server

import (
	"encoding/json"
	"fmt"
)

// PaginateOpts is the query_execute paginate argument. Auto=true
// enables Relay-style cursor traversal across pages, capped at
// MaxPages. Zero MaxPages defaults to PaginateDefaultMaxPages.
//
// Constraints in v0.7:
//
//   - Single paginatable connection per query. Multi-connection
//     paginate is more product surface than the v0.6 audit
//     justified; refile a separate v0.8 task if/when needed.
//   - Caller's query must include `$after: String` (or equivalent
//     cursor variable) AND must select `pageInfo { hasNextPage
//     endCursor }`. Without those, paginate is a no-op + returns
//     the first page verbatim.
type PaginateOpts struct {
	Auto     bool `json:"auto,omitempty"`
	MaxPages int  `json:"max_pages,omitempty"`
}

// PaginateDefaultMaxPages clamps unbounded loops when paginate is
// requested but max_pages is unset. 10 is a deliberate tradeoff:
// covers most real "give me everything" jobs without letting the
// agent's prompt accidentally enumerate a 100k-row table.
const PaginateDefaultMaxPages = 10

// findPageInfo walks a decoded GraphQL response looking for the
// first pageInfo {hasNextPage,endCursor} object. Returns the
// surrounding parent (so we can locate the sibling `nodes` array)
// + hasNextPage + endCursor. The walker is depth-first; in queries
// with multiple connections it picks the first one it sees.
//
// Returns ok=false when no pageInfo is found (the caller should
// then return the first response verbatim without paginating).
func findPageInfo(body []byte) (parent map[string]any, hasNext bool, endCursor string, ok bool) {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, false, "", false
	}
	return walkForPageInfo(doc)
}

func walkForPageInfo(v any) (map[string]any, bool, string, bool) {
	switch n := v.(type) {
	case map[string]any:
		if pi, has := n["pageInfo"].(map[string]any); has {
			hasNext, _ := pi["hasNextPage"].(bool)
			endCursor, _ := pi["endCursor"].(string)
			// Treat any pageInfo with the two canonical Relay
			// fields as a hit even if hasNextPage is false —
			// the caller short-circuits on that.
			if _, okHN := pi["hasNextPage"]; okHN {
				return n, hasNext, endCursor, true
			}
		}
		// Walk children depth-first.
		for _, child := range n {
			if p, hn, ec, ok := walkForPageInfo(child); ok {
				return p, hn, ec, ok
			}
		}
	case []any:
		for _, c := range n {
			if p, hn, ec, ok := walkForPageInfo(c); ok {
				return p, hn, ec, ok
			}
		}
	}
	return nil, false, "", false
}

// mergePageNodes extracts the `nodes` array from each page's
// pageInfo-parent object and concatenates them into the first
// page's nodes array, leaving every other field of the first page
// untouched. Returns the re-encoded merged document.
//
// pages[0] is the first response (with the original metadata the
// caller expects); subsequent entries contribute only their nodes.
func mergePageNodes(pages [][]byte) ([]byte, error) {
	if len(pages) == 0 {
		return nil, fmt.Errorf("merge: no pages")
	}
	if len(pages) == 1 {
		return pages[0], nil
	}
	var first any
	if err := json.Unmarshal(pages[0], &first); err != nil {
		return nil, fmt.Errorf("merge: decode page 0: %w", err)
	}
	parent, _, _, ok := walkForPageInfoOnDecoded(first)
	if !ok {
		// Caller asked to paginate, but the first response had
		// no pageInfo — nothing to merge across. Return page 0
		// verbatim.
		return pages[0], nil
	}
	nodes, _ := parent["nodes"].([]any)
	for _, p := range pages[1:] {
		var pageDoc any
		if err := json.Unmarshal(p, &pageDoc); err != nil {
			return nil, fmt.Errorf("merge: decode page: %w", err)
		}
		nextParent, _, _, hit := walkForPageInfoOnDecoded(pageDoc)
		if !hit {
			continue
		}
		if extra, ok := nextParent["nodes"].([]any); ok {
			nodes = append(nodes, extra...)
		}
	}
	parent["nodes"] = nodes
	// Flag pageInfo.hasNextPage based on the last page's signal
	// so downstream code sees an accurate cursor state.
	if last := pages[len(pages)-1]; last != nil {
		var lastDoc any
		if err := json.Unmarshal(last, &lastDoc); err == nil {
			if lp, _, lastCursor, ok := walkForPageInfoOnDecoded(lastDoc); ok {
				if pi, _ := parent["pageInfo"].(map[string]any); pi != nil {
					pi["hasNextPage"] = lp["pageInfo"].(map[string]any)["hasNextPage"]
					pi["endCursor"] = lastCursor
				}
			}
		}
	}
	return json.Marshal(first)
}

// walkForPageInfoOnDecoded is the already-decoded sibling of
// walkForPageInfo. Two flavours so the public surface can be
// driven by []byte while internal merge logic stays on the
// decoded tree (avoid round-tripping per page).
func walkForPageInfoOnDecoded(v any) (map[string]any, bool, string, bool) {
	return walkForPageInfo(v)
}
