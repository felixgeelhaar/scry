package schema

// Edge represents a directed reference from one type to another via
// a named field. Computed once per introspection and persisted in
// the store's `neighbors` table; schema_neighbors reads it back at
// query time.
//
// Kind values:
//   - "field"     — Type Src has a field that returns Type Dst
//   - "interface" — Type Src implements interface Dst
//   - "union"     — Type Src is a possible-type of union Dst
//   - "input"     — Input Src has a field of input-type Dst
type Edge struct {
	Src   string
	Dst   string
	Field string // field/arg name carrying the reference; empty for interface/union edges
	Kind  string
}

// BuildEdges walks a Schema and returns every type-to-type reference
// the bench's schema_neighbors tool needs.
//
// Skips: built-in scalars (String, Int, Float, Boolean, ID),
// introspection-private types (anything starting with __), and any
// edge where the destination is empty (unnamed types via deep
// LIST/NON_NULL wrapping that scry's introspection couldn't resolve).
//
// Returns an unsorted slice — caller sorts before persisting or
// before rendering for the agent.
func BuildEdges(s *Schema) []Edge {
	if s == nil {
		return nil
	}
	out := make([]Edge, 0, len(s.Types)*4)
	for _, t := range s.Types {
		if skipType(t.Name) {
			continue
		}
		// Field edges: every field's return type produces one
		// Src→Dst edge.
		for _, f := range t.Fields {
			dst := namedTypeOf(&f.Type)
			if dst == "" || skipType(dst) {
				continue
			}
			out = append(out, Edge{Src: t.Name, Dst: dst, Field: f.Name, Kind: "field"})
		}
		// Interface edges: Type implements Interface.
		for _, iref := range t.Interfaces {
			if iref.Name == "" || skipType(iref.Name) {
				continue
			}
			out = append(out, Edge{Src: t.Name, Dst: iref.Name, Kind: "interface"})
		}
		// Union/possible-types edges: PossibleType is a member of Union.
		// Edge points Member→Union so "what unions include me?"
		// queries surface naturally.
		for _, pref := range t.PossibleTypes {
			if pref.Name == "" || skipType(pref.Name) {
				continue
			}
			out = append(out, Edge{Src: pref.Name, Dst: t.Name, Kind: "union"})
		}
		// Input fields (input objects).
		for _, in := range t.InputFields {
			dst := namedTypeOf(&in.Type)
			if dst == "" || skipType(dst) {
				continue
			}
			out = append(out, Edge{Src: t.Name, Dst: dst, Field: in.Name, Kind: "input"})
		}
	}
	return out
}

// namedTypeOf strips LIST + NON_NULL wrappers and returns the leaf
// type's name. "[User!]!" → "User". Empty when the chain doesn't
// terminate in a named type (shouldn't happen on well-formed
// introspection output but the walker tolerates it).
func namedTypeOf(t *TypeRef) string {
	for t != nil {
		if t.Name != "" {
			return t.Name
		}
		t = t.OfType
	}
	return ""
}

// skipType filters built-in scalars + introspection internals. Edges
// pointing at these add noise without helping schema navigation.
func skipType(name string) bool {
	if name == "" {
		return true
	}
	if len(name) >= 2 && name[:2] == "__" {
		return true
	}
	switch name {
	case "String", "Int", "Float", "Boolean", "ID":
		return true
	}
	return false
}
