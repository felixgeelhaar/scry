package auth

import (
	"fmt"
	"strings"
)

// FieldMatcher tests one compiled deny-field pattern against a
// (typeName, fieldName) selection. Three shapes:
//
//   - Exact:    typeName == Type && fieldName == Field
//   - AnyType:  fieldName == Field (Type == "*")
//   - AnyField: typeName == Type (Field == "*")
//
// "*.*" is rejected at compile time — that'd lock the client out of
// every query, which is what an empty Tools list already expresses
// more clearly. If the operator wants total denial they should
// remove query_execute from Tools instead.
type FieldMatcher struct {
	Pattern  string // original pattern, kept for error rendering
	Type     string // type name; "" iff AnyType
	Field    string // field name; "" iff AnyField
	AnyType  bool
	AnyField bool
}

// Match returns true when the (typeName, fieldName) selection
// violates this matcher.
func (m *FieldMatcher) Match(typeName, fieldName string) bool {
	switch {
	case m.AnyType && m.AnyField:
		// Compile-time rejected, but defensive: never match
		// rather than block everything silently.
		return false
	case m.AnyType:
		return fieldName == m.Field
	case m.AnyField:
		return typeName == m.Type
	default:
		return typeName == m.Type && fieldName == m.Field
	}
}

// CompileFieldMatchers parses every pattern in the list. Empty input
// returns a nil slice (no deny rules in play). Invalid patterns
// return an error naming the offending string so the operator can
// fix one at a time.
func CompileFieldMatchers(patterns []string) ([]*FieldMatcher, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	out := make([]*FieldMatcher, 0, len(patterns))
	for _, p := range patterns {
		m, err := compileFieldMatcher(p)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func compileFieldMatcher(p string) (*FieldMatcher, error) {
	pat := strings.TrimSpace(p)
	if pat == "" {
		return nil, fmt.Errorf("deny_fields: empty pattern")
	}
	// Exactly one dot — nested paths (Type.field.subfield) are
	// not supported. The deny check operates on a single
	// (parent-type, field-name) pair at gqlparser walk time.
	if strings.Count(pat, ".") != 1 {
		return nil, fmt.Errorf("deny_fields: %q must be Type.field, *.field, or Type.* (exactly one dot)", p)
	}
	parts := strings.SplitN(pat, ".", 2)
	typeName := strings.TrimSpace(parts[0])
	fieldName := strings.TrimSpace(parts[1])
	if typeName == "" || fieldName == "" {
		return nil, fmt.Errorf("deny_fields: %q has empty type or field part", p)
	}
	anyType := typeName == "*"
	anyField := fieldName == "*"
	if anyType && anyField {
		return nil, fmt.Errorf("deny_fields: %q matches every field; remove query_execute from tools instead", p)
	}
	// Reject embedded wildcards beyond the bare "*" form — keep
	// the surface small and predictable. "Cust*.email" is not
	// supported; if needed, list every type explicitly.
	if !anyType && strings.Contains(typeName, "*") {
		return nil, fmt.Errorf("deny_fields: %q — partial wildcards in type part not supported", p)
	}
	if !anyField && strings.Contains(fieldName, "*") {
		return nil, fmt.Errorf("deny_fields: %q — partial wildcards in field part not supported", p)
	}
	return &FieldMatcher{
		Pattern:  pat,
		Type:     typeName,
		Field:    fieldName,
		AnyType:  anyType,
		AnyField: anyField,
	}, nil
}

// MatchAny returns the first matcher that fires for the selection,
// or nil if no rule denies it. The caller renders the matcher's
// Pattern into the permission_denied envelope so the operator can
// trace the rule that fired.
func MatchAny(matchers []*FieldMatcher, typeName, fieldName string) *FieldMatcher {
	for _, m := range matchers {
		if m.Match(typeName, fieldName) {
			return m
		}
	}
	return nil
}
