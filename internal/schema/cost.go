package schema

import (
	"github.com/vektah/gqlparser/v2/ast"
)

// CostReport summarises the static complexity of a parsed query.
// Numbers are heuristics — not exact node counts at runtime — but
// they're directionally accurate and good enough to gate "too
// expensive" before query_execute spends real upstream quota.
type CostReport struct {
	// Complexity is the headline number agents budget against.
	// Computed as sum of per-field weights with list fan-out
	// multipliers applied recursively.
	Complexity int `json:"complexity"`
	// Depth is the maximum selection-set nesting reached.
	Depth int `json:"depth"`
	// Fields is the total count of leaf+composite field selections.
	Fields int `json:"fields"`
	// Lists is the number of list-typed fields encountered (each
	// one multiplies its subtree by the listFanout heuristic).
	Lists int `json:"lists"`
}

// Heuristics. Tuned conservatively — the goal is "gate the obvious
// disasters", not micro-precision.
//
//   - leafWeight: each scalar/enum field costs 1.
//   - compositeWeight: each object/interface/union field costs 2
//     (one for itself, one for the cost of resolving the type).
//   - listFanout: list-typed fields multiply their subtree by this.
//     10 is the typical default in production complexity limiters
//     (GitHub, Shopify, Hasura all use a similar number).
const (
	leafWeight      = 1
	compositeWeight = 2
	listFanout      = 10
)

// EstimateCost parses + validates the query against the SDL and
// returns a CostReport. If the query is invalid, returns a zeroed
// report and the validation errors so callers can surface both.
func EstimateCost(sdl, query string) (CostReport, []ValidationError) {
	doc, errs := LoadQuery(sdl, query)
	if errs != nil {
		return CostReport{}, errs
	}
	var rpt CostReport
	for _, op := range doc.Operations {
		c, d, f, l := walkSelectionSet(op.SelectionSet, 1)
		rpt.Complexity += c
		if d > rpt.Depth {
			rpt.Depth = d
		}
		rpt.Fields += f
		rpt.Lists += l
	}
	return rpt, nil
}

// walkSelectionSet recursively scores one selection set. Returns
// (complexity, max depth from this set, total fields, total lists).
func walkSelectionSet(set ast.SelectionSet, depth int) (cost, maxDepth, fields, lists int) {
	maxDepth = depth
	for _, sel := range set {
		switch s := sel.(type) {
		case *ast.Field:
			fields++
			isList := isListType(s.Definition)
			if isList {
				lists++
			}
			childCost, childDepth, childFields, childLists := walkSelectionSet(s.SelectionSet, depth+1)
			fields += childFields
			lists += childLists
			if childDepth > maxDepth {
				maxDepth = childDepth
			}
			weight := leafWeight
			if len(s.SelectionSet) > 0 {
				weight = compositeWeight
			}
			subtree := childCost
			if isList {
				subtree *= listFanout
			}
			cost += weight + subtree
		case *ast.InlineFragment:
			c, d, f, l := walkSelectionSet(s.SelectionSet, depth)
			cost += c
			if d > maxDepth {
				maxDepth = d
			}
			fields += f
			lists += l
		case *ast.FragmentSpread:
			if s.Definition != nil {
				c, d, f, l := walkSelectionSet(s.Definition.SelectionSet, depth)
				cost += c
				if d > maxDepth {
					maxDepth = d
				}
				fields += f
				lists += l
			}
		}
	}
	return
}

// isListType reports whether a field's definition resolves to a list
// type (potentially wrapped in NON_NULL). Walks the gqlparser type
// chain until either LIST or the leaf is found.
func isListType(def *ast.FieldDefinition) bool {
	if def == nil || def.Type == nil {
		return false
	}
	// gqlparser flattens NON_NULL into a flag on the same Type
	// node, so a single check is sufficient — no recursion.
	return def.Type.Elem != nil
}
