package schema

import (
	"sort"
	"strings"
)

// ChangeKind labels one schema diff entry. Three buckets so
// operators can wire alerting at different thresholds: additive
// changes are usually safe, breaking changes warrant a page.
type ChangeKind string

const (
	// ChangeAdded is a new type or field. Safe for existing
	// agents — they keep working; new capability is exposed.
	ChangeAdded ChangeKind = "added"
	// ChangeRemoved is a deletion. Breaks any cached query that
	// referenced the deleted name.
	ChangeRemoved ChangeKind = "removed"
	// ChangeBreaking covers signature changes — argument type
	// changes, required-arg additions, return type changes on a
	// retained type. Anything that would invalidate an existing
	// query against the previous schema.
	ChangeBreaking ChangeKind = "breaking"
)

// Change is one diff entry. Path is dotted: "Customer" for a type,
// "Customer.email" for a field.
type Change struct {
	Kind ChangeKind `json:"kind"`
	Path string     `json:"path"`
	Note string     `json:"note,omitempty"`
}

// DiffReport is the operator-facing summary returned by Diff.
type DiffReport struct {
	Added    []Change `json:"added,omitempty"`
	Removed  []Change `json:"removed,omitempty"`
	Breaking []Change `json:"breaking,omitempty"`
}

// Empty reports whether the diff contains zero changes. Cheap check
// for the refresh hot path so we skip emitting events on no-op
// reloads.
func (r DiffReport) Empty() bool {
	return len(r.Added) == 0 && len(r.Removed) == 0 && len(r.Breaking) == 0
}

// Total returns the count of changes across all buckets.
func (r DiffReport) Total() int {
	return len(r.Added) + len(r.Removed) + len(r.Breaking)
}

// Diff compares the previous + next Schema and returns the change
// summary. Both nil returns an empty report. One nil + one non-nil
// treats the nil side as empty (all types in the other side count
// as added or removed accordingly).
//
// Granularity is type + field. Argument-signature changes on
// retained fields produce a breaking entry; describing exactly
// which argument changed is intentionally out of scope (the Note
// field carries a short human summary).
func Diff(prev, next *Schema) DiffReport {
	prevTypes := indexTypes(prev)
	nextTypes := indexTypes(next)

	var r DiffReport

	// Find added + retained types.
	for name, nt := range nextTypes {
		pt, ok := prevTypes[name]
		if !ok {
			r.Added = append(r.Added, Change{Kind: ChangeAdded, Path: name})
			continue
		}
		// Type retained → walk fields.
		diffTypeFields(name, pt, nt, &r)
	}
	// Find removed types.
	for name := range prevTypes {
		if _, ok := nextTypes[name]; !ok {
			r.Removed = append(r.Removed, Change{Kind: ChangeRemoved, Path: name})
		}
	}

	sortChanges(r.Added)
	sortChanges(r.Removed)
	sortChanges(r.Breaking)
	return r
}

// indexTypes returns a name → Type map skipping the GraphQL
// internal types (`__Schema`, etc.) since they're never user-
// visible and would otherwise show up as no-ops on every diff.
func indexTypes(s *Schema) map[string]Type {
	out := map[string]Type{}
	if s == nil {
		return out
	}
	for _, t := range s.Types {
		if strings.HasPrefix(t.Name, "__") {
			continue
		}
		out[t.Name] = t
	}
	return out
}

// diffTypeFields walks two same-named types + records field-level
// changes. Field added → ChangeAdded; field removed → ChangeRemoved;
// argument signature change or return-type change → ChangeBreaking.
func diffTypeFields(typeName string, prev, next Type, r *DiffReport) {
	prevFields := map[string]Field{}
	for _, f := range prev.Fields {
		prevFields[f.Name] = f
	}
	nextFields := map[string]Field{}
	for _, f := range next.Fields {
		nextFields[f.Name] = f
	}

	for name, nf := range nextFields {
		pf, ok := prevFields[name]
		path := typeName + "." + name
		if !ok {
			r.Added = append(r.Added, Change{Kind: ChangeAdded, Path: path})
			continue
		}
		if pf.Type.String() != nf.Type.String() {
			r.Breaking = append(r.Breaking, Change{
				Kind: ChangeBreaking, Path: path,
				Note: "return type " + pf.Type.String() + " → " + nf.Type.String(),
			})
			continue
		}
		if argsDiffer(pf.Args, nf.Args) {
			r.Breaking = append(r.Breaking, Change{
				Kind: ChangeBreaking, Path: path,
				Note: "argument signature changed",
			})
		}
	}
	for name := range prevFields {
		if _, ok := nextFields[name]; !ok {
			r.Removed = append(r.Removed, Change{Kind: ChangeRemoved, Path: typeName + "." + name})
		}
	}
}

// argsDiffer reports whether two argument lists differ in a way
// that breaks a previously-valid query. v0.3 is conservative: any
// add / remove / type change on an argument counts as breaking.
// Future refinement could split "new optional arg" (additive) from
// "new required arg" (breaking).
func argsDiffer(prev, next []InputField) bool {
	if len(prev) != len(next) {
		return true
	}
	prevByName := map[string]InputField{}
	for _, a := range prev {
		prevByName[a.Name] = a
	}
	for _, na := range next {
		pa, ok := prevByName[na.Name]
		if !ok {
			return true
		}
		if pa.Type.String() != na.Type.String() {
			return true
		}
	}
	return false
}

func sortChanges(cs []Change) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Path < cs[j].Path })
}
