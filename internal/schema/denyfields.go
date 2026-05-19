package schema

import (
	"github.com/vektah/gqlparser/v2/ast"
)

// FieldSelection is one (parentType, fieldName) tuple from a query's
// resolved AST. Used by deny-field authz: the gqlparser walker emits
// every selection an executed query would actually read, including
// those reached via fragments + inline fragments. Aliases are
// ignored — what matters for authz is the real field name, not the
// renamed JSON key on the response.
type FieldSelection struct {
	ParentType string
	FieldName  string
}

// WalkFieldSelections returns every (parentType, fieldName) pair the
// query selects, including those reached transitively through
// fragment spreads + inline fragments. Aliases do NOT bypass — a
// query `{ user { e: email } }` reports ("User", "email"), not
// ("User", "e").
//
// Pass the *resolved* query document — gqlparser's LoadQuery wires
// up the .ObjectDefinition + .Definition pointers the walker depends
// on. An unresolved document (raw parser output) will report empty
// ParentType for every field.
func WalkFieldSelections(doc *ast.QueryDocument) []FieldSelection {
	if doc == nil {
		return nil
	}
	w := walker{
		out:     []FieldSelection{},
		fragmap: map[string]*ast.FragmentDefinition{},
		visited: map[string]bool{},
	}
	for _, f := range doc.Fragments {
		w.fragmap[f.Name] = f
	}
	for _, op := range doc.Operations {
		w.walkSet(op.SelectionSet)
	}
	return w.out
}

type walker struct {
	out     []FieldSelection
	fragmap map[string]*ast.FragmentDefinition
	visited map[string]bool // guards against recursive fragment cycles
}

func (w *walker) walkSet(sel ast.SelectionSet) {
	for _, s := range sel {
		switch n := s.(type) {
		case *ast.Field:
			parent := ""
			if n.ObjectDefinition != nil {
				parent = n.ObjectDefinition.Name
			}
			// Skip introspection meta-fields (__schema, __type,
			// __typename). They have no real upstream backing
			// and can't carry sensitive data the deny rules
			// would want to block.
			if len(n.Name) >= 2 && n.Name[:2] == "__" {
				w.walkSet(n.SelectionSet)
				continue
			}
			w.out = append(w.out, FieldSelection{ParentType: parent, FieldName: n.Name})
			w.walkSet(n.SelectionSet)
		case *ast.InlineFragment:
			// Inline fragment narrows the parent type. The
			// nested fields' ObjectDefinition will reflect the
			// fragment's TypeCondition.
			w.walkSet(n.SelectionSet)
		case *ast.FragmentSpread:
			frag, ok := w.fragmap[n.Name]
			if !ok {
				continue
			}
			// Guard against recursive fragment refs — gqlparser
			// usually rejects these at validation, but the
			// walker's job is to be safe even if it sees one.
			if w.visited[n.Name] {
				continue
			}
			w.visited[n.Name] = true
			w.walkSet(frag.SelectionSet)
			delete(w.visited, n.Name)
		}
	}
}
