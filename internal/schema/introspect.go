// Package schema introspects an upstream GraphQL endpoint, normalises
// the result into searchable units, and persists them to a local
// SQLite FTS5 store. The store is the read-side for the schema_search
// + schema_get MCP tools.
package schema

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// introspectionQuery is the standard GraphQL introspection. Trimmed to
// the fields scry actually consumes: type names, descriptions, kind,
// fields (with args + types), enums, input objects. Skips deprecation
// flags + AST locations (not used in v0 search relevance).
//
// Type wrappers (NonNull / List) are unwrapped through 4 `ofType`
// levels, which handles `NonNull(List(NonNull(NonNull(X))))` — the
// deepest wrap the spec allows in practice. Total selection depth is
// ~9, which trips some CDN-fronted upstreams; see
// introspectionQueryShallow + tryIntrospect for the fallback path.
const introspectionQuery = `{
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types {
      kind
      name
      description
      fields(includeDeprecated: false) {
        name
        description
        args {
          name
          description
          type { kind name ofType { kind name ofType { kind name ofType { kind name } } } }
          defaultValue
        }
        type { kind name ofType { kind name ofType { kind name ofType { kind name } } } }
      }
      inputFields {
        name
        description
        type { kind name ofType { kind name ofType { kind name ofType { kind name } } } }
        defaultValue
      }
      interfaces { name }
      enumValues(includeDeprecated: false) { name description }
      possibleTypes { name }
    }
  }
}`

// introspectionQueryShallow halves the ofType nesting (2 levels
// instead of 4). Selection depth drops from ~9 to ~5, which passes
// Graph CDN / Stellate's default depth limiter. Trade-off: wrapper
// fidelity is reduced — `NonNull(List(NonNull(X)))` resolves to
// `[X]` instead of `[X!]!`. The inner `!` annotations are lost so
// SDL rendering is slightly less precise, but:
//
//   - schema_search results are unaffected (named types still match)
//   - query_validate still catches field-name and arg-name errors
//   - query_cost still detects list fields and computes complexity
//
// Operators get full-fidelity SDL on upstreams that allow the deep
// query; shallower SDL on CDN-fronted upstreams. The meta key
// `introspection_mode` records which path produced the cached index.
const introspectionQueryShallow = `{
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types {
      kind
      name
      description
      fields(includeDeprecated: false) {
        name
        description
        args {
          name
          description
          type { kind name ofType { kind name } }
          defaultValue
        }
        type { kind name ofType { kind name } }
      }
      inputFields {
        name
        description
        type { kind name ofType { kind name } }
        defaultValue
      }
      interfaces { name }
      enumValues(includeDeprecated: false) { name description }
      possibleTypes { name }
    }
  }
}`

// Client wraps the upstream HTTP plumbing. Stateless; one Client per
// upstream endpoint. Auth header is set from cfg and re-read on each
// call so token rotation (servers.yml hot-reload) takes effect
// without re-constructing the client.
type Client struct {
	upstream string
	auth     func() string
	http     *http.Client
}

// NewClient returns a Client that POSTs introspection queries to
// upstream. The auth function is called once per request so callers
// can rotate the bearer without rebuilding the client. Pass a
// fortify-wrapped *http.Client for circuit-breaker + retry; the
// stdlib client works for tests.
func NewClient(upstream string, auth func() string, c *http.Client) *Client {
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{upstream: upstream, auth: auth, http: c}
}

// IntrospectionMode reports which query variant produced the result.
// Stored as a meta value so operators can inspect what was indexed
// (full = wrapper fidelity preserved, shallow = inner NonNull
// annotations lost on doubly-wrapped types).
type IntrospectionMode string

const (
	IntrospectionFull    IntrospectionMode = "full"
	IntrospectionShallow IntrospectionMode = "shallow"
)

// Introspect runs the introspection query against the upstream and
// returns the parsed __schema block + the mode that succeeded.
//
// Strategy: try the full-depth query first (preserves wrapper
// fidelity). If the upstream rejects it with a depth-limit signal
// (HTTP 413 from Graph CDN / Stellate, or a GraphQL error whose
// message contains "depth"), retry with the shallow variant. Other
// errors (auth, transport, malformed JSON) propagate immediately —
// no point retrying a 401 with a shorter query.
func (c *Client) Introspect(ctx context.Context) (*Schema, IntrospectionMode, error) {
	s, err := c.tryIntrospect(ctx, introspectionQuery)
	if err == nil {
		return s, IntrospectionFull, nil
	}
	if !isDepthLimitError(err) {
		return nil, "", err
	}
	s, err = c.tryIntrospect(ctx, introspectionQueryShallow)
	if err != nil {
		return nil, "", fmt.Errorf("introspect (shallow fallback): %w", err)
	}
	return s, IntrospectionShallow, nil
}

// tryIntrospect runs one introspection query and parses the response.
// Returned errors are wrapped — callers detect depth-limit failures
// via isDepthLimitError so the public Introspect can pick the
// fallback path.
func (c *Client) tryIntrospect(ctx context.Context, query string) (*Schema, error) {
	body, _ := json.Marshal(map[string]string{"query": query})
	req, err := http.NewRequestWithContext(ctx, "POST", c.upstream, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.auth != nil {
		if tok := c.auth(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("introspect: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &httpError{status: resp.StatusCode, body: string(preview)}
	}
	var env struct {
		Data struct {
			Schema Schema `json:"__schema"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(env.Errors) > 0 {
		return nil, &graphqlError{message: env.Errors[0].Message}
	}
	return &env.Data.Schema, nil
}

// httpError is the typed transport error returned by tryIntrospect.
// Carries the upstream's status + response body preview so
// isDepthLimitError can pattern-match without re-parsing.
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("upstream returned %d: %s", e.status, e.body)
}

// graphqlError is the typed application-level error returned when the
// upstream succeeds at the HTTP layer but returns a `errors` array.
type graphqlError struct{ message string }

func (e *graphqlError) Error() string { return "graphql error: " + e.message }

// isDepthLimitError returns true when err looks like a query-depth
// rejection from a CDN or in-app limiter. Signals checked:
//
//   - HTTP 413 with any body (Graph CDN's signature response).
//   - HTTP 400 with a body mentioning depth/complexity (some
//     Apollo Router and Hasura deployments).
//   - GraphQL errors containing "depth" or "complexity".
//
// Conservative on purpose: matching too aggressively would mask
// real errors (auth, schema disabled, network) behind a silent
// fallback. Only the explicit signals above qualify.
func isDepthLimitError(err error) bool {
	var he *httpError
	if errors.As(err, &he) {
		if he.status == 413 {
			return true
		}
		if he.status == 400 && (strings.Contains(strings.ToLower(he.body), "depth") ||
			strings.Contains(strings.ToLower(he.body), "complexity")) {
			return true
		}
	}
	var ge *graphqlError
	if errors.As(err, &ge) {
		m := strings.ToLower(ge.message)
		if strings.Contains(m, "depth") || strings.Contains(m, "complexity") {
			return true
		}
	}
	return false
}

// Schema is the subset of the introspection result scry consumes.
// Mirrors the GraphQL spec's __Schema shape but kept lean — no AST
// locations, no deprecation flags (the introspection query above
// already strips them at source).
type Schema struct {
	QueryType        *TypeRef `json:"queryType"`
	MutationType     *TypeRef `json:"mutationType"`
	SubscriptionType *TypeRef `json:"subscriptionType"`
	Types            []Type   `json:"types"`
}

// Type is one node of the introspection — object, interface, enum,
// input, scalar, union. Kind drives downstream rendering: input
// types appear in argument signatures; object types contain fields.
type Type struct {
	Kind          string       `json:"kind"`
	Name          string       `json:"name"`
	Description   string       `json:"description"`
	Fields        []Field      `json:"fields"`
	InputFields   []InputField `json:"inputFields"`
	Interfaces    []TypeRef    `json:"interfaces"`
	EnumValues    []EnumValue  `json:"enumValues"`
	PossibleTypes []TypeRef    `json:"possibleTypes"`
}

// Field is one selectable field on an object/interface type.
type Field struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Args        []InputField `json:"args"`
	Type        TypeRef      `json:"type"`
}

// InputField is a field on an input type OR an argument on a Field.
type InputField struct {
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	Type         TypeRef `json:"type"`
	DefaultValue string  `json:"defaultValue"`
}

// EnumValue is one allowed value in an enum.
type EnumValue struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// TypeRef is a recursive reference to a type — supports the
// LIST/NON_NULL wrappers GraphQL uses. Walk OfType to resolve to a
// named type.
type TypeRef struct {
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	OfType *TypeRef `json:"ofType"`
}

// String renders a TypeRef into the canonical GraphQL signature:
// `[Foo!]!`, `String`, etc. Used by index.go to build the searchable
// text for each unit.
func (t *TypeRef) String() string {
	if t == nil {
		return ""
	}
	switch t.Kind {
	case "NON_NULL":
		return t.OfType.String() + "!"
	case "LIST":
		return "[" + t.OfType.String() + "]"
	default:
		return t.Name
	}
}
