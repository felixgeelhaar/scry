package schema

import (
	"errors"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// ValidationError is one gqlparser diagnostic flattened for return to
// MCP callers. Line/column are 1-indexed in gqlparser, matching most
// editor conventions.
type ValidationError struct {
	Message string `json:"message"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

// LoadSchema parses an SDL document and returns the gqlparser AST.
// Used internally by query_validate and query_cost; exposed so tests
// can build a schema from fixtures directly.
func LoadSchema(sdl string) (*ast.Schema, error) {
	if sdl == "" {
		return nil, errors.New("empty schema SDL — run introspection first")
	}
	s, err := gqlparser.LoadSchema(&ast.Source{Name: "scry.graphql", Input: sdl})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// ValidateQuery returns a list of validation errors for `query`
// against the cached schema. An empty slice means the query is valid.
// Returns a single synthetic error if the schema fails to load.
func ValidateQuery(sdl, query string) []ValidationError {
	s, err := LoadSchema(sdl)
	if err != nil {
		return []ValidationError{{Message: "schema unavailable: " + err.Error()}}
	}
	_, qerr := gqlparser.LoadQueryWithRules(s, query, nil)
	return flattenErrors(qerr)
}

// LoadQuery parses + validates and returns the AST when successful.
// Cost analysis calls this directly to avoid double-validating.
func LoadQuery(sdl, query string) (*ast.QueryDocument, []ValidationError) {
	s, err := LoadSchema(sdl)
	if err != nil {
		return nil, []ValidationError{{Message: "schema unavailable: " + err.Error()}}
	}
	doc, qerr := gqlparser.LoadQueryWithRules(s, query, nil)
	if qerr != nil {
		return nil, flattenErrors(qerr)
	}
	return doc, nil
}

// flattenErrors turns gqlparser's typed error into the JSON-friendly
// ValidationError shape. gqlparser returns either a *gqlerror.List or
// a single error wrapped — handle both.
func flattenErrors(err error) []ValidationError {
	if err == nil {
		return nil
	}
	var list gqlerror.List
	if errors.As(err, &list) {
		out := make([]ValidationError, 0, len(list))
		for _, e := range list {
			out = append(out, fromGQLError(e))
		}
		return out
	}
	var single *gqlerror.Error
	if errors.As(err, &single) {
		return []ValidationError{fromGQLError(single)}
	}
	return []ValidationError{{Message: err.Error()}}
}

func fromGQLError(e *gqlerror.Error) ValidationError {
	v := ValidationError{Message: e.Message}
	if len(e.Locations) > 0 {
		v.Line = e.Locations[0].Line
		v.Column = e.Locations[0].Column
	}
	return v
}
