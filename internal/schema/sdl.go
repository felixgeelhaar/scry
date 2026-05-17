package schema

import (
	"fmt"
	"os"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// readFile is a small wrapper around os.ReadFile so tests can inject
// a fake without touching the real filesystem. Left non-exported on
// purpose; if more callers want it, promote and add tests.
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// loadSchemaFromSDL parses one SDL document into a gqlparser ast.Schema.
// Surfaces a clear error when the input isn't valid SDL — the
// operator who pointed --sdl at the wrong file gets pointed back.
func loadSchemaFromSDL(sdl, sourceName string) (*ast.Schema, error) {
	if sourceName == "" {
		sourceName = "scry.graphql"
	}
	s, err := gqlparser.LoadSchema(&ast.Source{Name: sourceName, Input: sdl})
	if err != nil {
		return nil, fmt.Errorf("parse sdl: %w", err)
	}
	return s, nil
}

// typeFromAST translates one gqlparser ast.Definition into our
// introspection-shaped Type. Kept narrow on purpose: only the
// fields BuildUnits + BuildSDL consume are populated. Directives,
// extensions, and AST positions are dropped — they aren't surfaced
// in the index and would just add noise to the snapshot.
func typeFromAST(def *ast.Definition) Type {
	t := Type{
		Kind:        kindFromAST(def.Kind),
		Name:        def.Name,
		Description: def.Description,
	}
	for _, f := range def.Fields {
		// gqlparser auto-injects the introspection meta fields
		// (`__schema`, `__type`) onto the Query type. They're not
		// part of the operator's SDL — keeping them would leak
		// into BuildSDL output and reject on re-parse.
		if strings.HasPrefix(f.Name, "__") {
			continue
		}
		t.Fields = append(t.Fields, fieldFromAST(f))
	}
	// gqlparser uses Fields for both object fields AND input-object
	// fields. We map the latter into InputFields so BuildSDL emits
	// `input X { ... }` correctly.
	if def.Kind == ast.InputObject {
		t.InputFields = make([]InputField, 0, len(def.Fields))
		for _, f := range def.Fields {
			t.InputFields = append(t.InputFields, InputField{
				Name:         f.Name,
				Description:  f.Description,
				Type:         typeRefFromAST(f.Type),
				DefaultValue: defaultValueString(f.DefaultValue),
			})
		}
		t.Fields = nil // input objects don't carry the field-shape we use elsewhere
	}
	for _, ifaceName := range def.Interfaces {
		t.Interfaces = append(t.Interfaces, TypeRef{Kind: "INTERFACE", Name: ifaceName})
	}
	for _, ev := range def.EnumValues {
		t.EnumValues = append(t.EnumValues, EnumValue{Name: ev.Name, Description: ev.Description})
	}
	for _, member := range def.Types {
		t.PossibleTypes = append(t.PossibleTypes, TypeRef{Kind: "OBJECT", Name: member})
	}
	return t
}

func kindFromAST(k ast.DefinitionKind) string {
	switch k {
	case ast.Object:
		return "OBJECT"
	case ast.Interface:
		return "INTERFACE"
	case ast.Union:
		return "UNION"
	case ast.Enum:
		return "ENUM"
	case ast.InputObject:
		return "INPUT_OBJECT"
	case ast.Scalar:
		return "SCALAR"
	}
	return "OBJECT"
}

func fieldFromAST(f *ast.FieldDefinition) Field {
	out := Field{
		Name:        f.Name,
		Description: f.Description,
		Type:        typeRefFromAST(f.Type),
	}
	for _, a := range f.Arguments {
		out.Args = append(out.Args, InputField{
			Name:         a.Name,
			Description:  a.Description,
			Type:         typeRefFromAST(a.Type),
			DefaultValue: defaultValueString(a.DefaultValue),
		})
	}
	return out
}

// typeRefFromAST translates gqlparser's NonNull/List representation
// (single flag + Elem pointer) into our nested ofType chain. Always
// emits the most canonical form — NON_NULL wraps LIST wraps the
// named leaf — so downstream String() matches introspection output.
func typeRefFromAST(t *ast.Type) TypeRef {
	if t == nil {
		return TypeRef{}
	}
	var inner TypeRef
	if t.Elem != nil {
		child := typeRefFromAST(t.Elem)
		inner = TypeRef{Kind: "LIST", OfType: &child}
	} else {
		inner = TypeRef{Kind: "OBJECT", Name: t.NamedType}
	}
	if t.NonNull {
		return TypeRef{Kind: "NON_NULL", OfType: &inner}
	}
	return inner
}

func defaultValueString(v *ast.Value) string {
	if v == nil {
		return ""
	}
	return v.String()
}
