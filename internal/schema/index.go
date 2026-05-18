package schema

import (
	"fmt"
	"strings"
)

// SearchUnit is one searchable record in the schema index. Granularity
// is per-field on object/interface types, and per-named-type for
// scalars/enums/inputs/unions. Each unit carries its own SDL fragment
// so the schema_get tool can return ready-to-paste output without
// touching the upstream again.
type SearchUnit struct {
	// Kind: "type" | "field" | "input" | "enum".
	Kind string
	// Name is the unit's identifier in the index.
	//   - "type" / "input" / "enum": the type name (e.g. "Customer").
	//   - "field": "ParentType.fieldName" (e.g. "Query.customer").
	Name string
	// ParentType is empty for "type" units; set for "field" units.
	ParentType string
	// Description is the introspected docstring (may be empty).
	Description string
	// Signature is the GraphQL-rendered signature for fields and
	// enums; the type body for object/input units.
	Signature string
	// SDL is the full pretty-printed SDL fragment for the unit,
	// returned verbatim by schema_get.
	SDL string
	// Composed is the BM25-indexed text: name + description + arg
	// names + return type tokens. Built once at index time.
	Composed string
}

// BuildSDL renders the full schema as one concatenated SDL document.
// Used to feed gqlparser for query_validate and query_cost without
// re-running introspection.
func BuildSDL(s *Schema) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	// Root operation types come first so gqlparser can resolve
	// `query { ... }` regardless of the upstream's naming.
	if s.QueryType != nil || s.MutationType != nil || s.SubscriptionType != nil {
		b.WriteString("schema {\n")
		if s.QueryType != nil {
			fmt.Fprintf(&b, "  query: %s\n", s.QueryType.Name)
		}
		if s.MutationType != nil {
			fmt.Fprintf(&b, "  mutation: %s\n", s.MutationType.Name)
		}
		if s.SubscriptionType != nil {
			fmt.Fprintf(&b, "  subscription: %s\n", s.SubscriptionType.Name)
		}
		b.WriteString("}\n\n")
	}
	for _, t := range s.Types {
		if strings.HasPrefix(t.Name, "__") {
			continue
		}
		// Skip the built-in scalars; gqlparser injects them already.
		switch t.Name {
		case "String", "Int", "Float", "Boolean", "ID":
			continue
		}
		b.WriteString(renderTypeSDL(t))
		b.WriteString("\n\n")
	}
	return b.String()
}

// LoadSDLFile reads an SDL document from disk and returns a Schema
// shaped like an introspection result. Used by the `--sdl-file`
// operator escape hatch when an upstream rejects both the full and
// shallow introspection queries.
//
// Implementation: parse the SDL with gqlparser, then translate the
// resulting ast.Schema into our Schema structs. We re-use the same
// types so downstream code (BuildUnits / BuildSDL / Store.Replace)
// is unchanged.
func LoadSDLFile(path string) (*Schema, error) {
	b, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sdl file %s: %w", path, err)
	}
	return ParseSDL(string(b), path)
}

// ParseSDL converts an SDL document into our Schema representation.
// Kept package-public so tests can drive it from an inline string.
func ParseSDL(sdl, sourceName string) (*Schema, error) {
	parsed, err := loadSchemaFromSDL(sdl, sourceName)
	if err != nil {
		return nil, err
	}
	out := &Schema{}
	if parsed.Query != nil {
		out.QueryType = &TypeRef{Kind: "OBJECT", Name: parsed.Query.Name}
	}
	if parsed.Mutation != nil {
		out.MutationType = &TypeRef{Kind: "OBJECT", Name: parsed.Mutation.Name}
	}
	if parsed.Subscription != nil {
		out.SubscriptionType = &TypeRef{Kind: "OBJECT", Name: parsed.Subscription.Name}
	}
	for _, def := range parsed.Types {
		// Skip the introspection meta-types gqlparser injects (the
		// `__Schema` family). Upstream-relevant types only.
		if strings.HasPrefix(def.Name, "__") {
			continue
		}
		// Skip GraphQL's built-in scalars; BuildSDL re-injects
		// them by convention and BuildUnits filters them anyway.
		switch def.Name {
		case "String", "Int", "Float", "Boolean", "ID":
			continue
		}
		out.Types = append(out.Types, typeFromAST(def))
	}
	return out, nil
}

// BuildUnits flattens a Schema into a slice of SearchUnits. Skips
// GraphQL internal types ("__Schema", "__Type", etc.) which the spec
// reserves for introspection itself — agents don't query them.
func BuildUnits(s *Schema) []SearchUnit {
	if s == nil {
		return nil
	}
	var units []SearchUnit
	for _, t := range s.Types {
		if strings.HasPrefix(t.Name, "__") {
			continue
		}
		units = append(units, typeUnit(t))
		if t.Kind == "OBJECT" || t.Kind == "INTERFACE" {
			for _, f := range t.Fields {
				units = append(units, fieldUnit(t, f))
			}
		}
	}
	return units
}

// typeUnit renders one top-level Type into a SearchUnit.
func typeUnit(t Type) SearchUnit {
	sdl := renderTypeSDL(t)
	var sig strings.Builder
	fmt.Fprintf(&sig, "%s %s", strings.ToLower(t.Kind), t.Name)
	composed := strings.Join([]string{
		t.Name,
		strings.ToLower(t.Kind),
		t.Description,
		fieldNames(t),
	}, " ")
	return SearchUnit{
		Kind:        kindLabel(t.Kind),
		Name:        t.Name,
		Description: t.Description,
		Signature:   sig.String(),
		SDL:         sdl,
		Composed:    composed,
	}
}

// fieldUnit renders one field of an OBJECT/INTERFACE type into a
// SearchUnit. The Name is `Parent.field` so schema_get can resolve
// either the full type or a specific field.
func fieldUnit(parent Type, f Field) SearchUnit {
	sig := renderFieldSDL(f)
	composed := strings.Join([]string{
		parent.Name + "." + f.Name,
		f.Name,
		f.Description,
		f.Type.String(),
		argNames(f.Args),
	}, " ")
	return SearchUnit{
		Kind:        "field",
		Name:        parent.Name + "." + f.Name,
		ParentType:  parent.Name,
		Description: f.Description,
		Signature:   sig,
		SDL:         sig,
		Composed:    composed,
	}
}

// kindLabel maps GraphQL __Type.kind into the index's "kind" facet.
// Compresses OBJECT/INTERFACE into "type" (the consumer doesn't care
// for ranking) but keeps input/enum distinct.
func kindLabel(k string) string {
	switch k {
	case "INPUT_OBJECT":
		return "input"
	case "ENUM":
		return "enum"
	case "SCALAR":
		return "scalar"
	case "UNION":
		return "union"
	default:
		return "type"
	}
}

// renderTypeSDL emits a multi-line SDL block for a Type. Mirrors what
// `gql-tools introspect-to-sdl` would produce minus deprecation
// directives.
func renderTypeSDL(t Type) string {
	var b strings.Builder
	if t.Description != "" {
		fmt.Fprintf(&b, "\"\"\"%s\"\"\"\n", t.Description)
	}
	switch t.Kind {
	case "OBJECT", "INTERFACE":
		kw := "type"
		if t.Kind == "INTERFACE" {
			kw = "interface"
		}
		fmt.Fprintf(&b, "%s %s", kw, t.Name)
		if len(t.Interfaces) > 0 {
			ifaces := make([]string, 0, len(t.Interfaces))
			for _, i := range t.Interfaces {
				ifaces = append(ifaces, i.Name)
			}
			fmt.Fprintf(&b, " implements %s", strings.Join(ifaces, " & "))
		}
		b.WriteString(" {\n")
		for _, f := range t.Fields {
			b.WriteString("  ")
			b.WriteString(renderFieldSDL(f))
			b.WriteString("\n")
		}
		b.WriteString("}")
	case "INPUT_OBJECT":
		fmt.Fprintf(&b, "input %s {\n", t.Name)
		for _, f := range t.InputFields {
			fmt.Fprintf(&b, "  %s: %s", f.Name, f.Type.String())
			if f.DefaultValue != "" {
				fmt.Fprintf(&b, " = %s", f.DefaultValue)
			}
			b.WriteString("\n")
		}
		b.WriteString("}")
	case "ENUM":
		fmt.Fprintf(&b, "enum %s {\n", t.Name)
		for _, v := range t.EnumValues {
			fmt.Fprintf(&b, "  %s\n", v.Name)
		}
		b.WriteString("}")
	case "UNION":
		members := make([]string, 0, len(t.PossibleTypes))
		for _, m := range t.PossibleTypes {
			members = append(members, m.Name)
		}
		fmt.Fprintf(&b, "union %s = %s", t.Name, strings.Join(members, " | "))
	case "SCALAR":
		fmt.Fprintf(&b, "scalar %s", t.Name)
	}
	return b.String()
}

// renderFieldSDL emits one field's SDL line (name + args + return).
func renderFieldSDL(f Field) string {
	var b strings.Builder
	b.WriteString(f.Name)
	if len(f.Args) > 0 {
		b.WriteString("(")
		parts := make([]string, 0, len(f.Args))
		for _, a := range f.Args {
			p := fmt.Sprintf("%s: %s", a.Name, a.Type.String())
			if a.DefaultValue != "" {
				p += " = " + a.DefaultValue
			}
			parts = append(parts, p)
		}
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString(")")
	}
	fmt.Fprintf(&b, ": %s", f.Type.String())
	return b.String()
}

func fieldNames(t Type) string {
	names := make([]string, 0, len(t.Fields)+len(t.InputFields)+len(t.EnumValues))
	for _, f := range t.Fields {
		names = append(names, f.Name)
	}
	for _, f := range t.InputFields {
		names = append(names, f.Name)
	}
	for _, e := range t.EnumValues {
		names = append(names, e.Name)
	}
	return strings.Join(names, " ")
}

func argNames(args []InputField) string {
	if len(args) == 0 {
		return ""
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		out = append(out, a.Name)
	}
	return strings.Join(out, " ")
}
