package server

import (
	"context"
	"encoding/json"

	"github.com/felixgeelhaar/scry/internal/auth"
	"github.com/felixgeelhaar/scry/internal/schema"
)

// checkDeniedFields is the post-validate authz hook used by
// query_validate + query_execute. Returns "" when the current
// caller's scope permits every field the query touches, or a
// JSON permission_denied envelope when one or more denied fields
// were selected.
//
// Callers MUST run schema.ValidateQuery first. An invalid query
// won't resolve type references, so the walker would emit empty
// parent types and miss real violations.
//
// When no clients.yml scope is in play (scopeFor returns nil) or
// the scope has no deny rules, returns "" — fast-path through.
//
// The envelope shape:
//
//	{
//	  "error": "permission_denied",
//	  "hint":  "clients.yml deny_fields rule blocks this query",
//	  "denied_fields": [
//	    {"type": "User", "field": "email", "pattern": "User.email"},
//	    ...
//	  ],
//	  "presented_client": "<name>"
//	}
func checkDeniedFields(ctx context.Context, sdl, query string) string {
	scope := scopeFor(ctx)
	if scope == nil || len(scope.DeniedFieldMatchers) == 0 {
		return ""
	}
	doc, errs := schema.LoadQuery(sdl, query)
	if len(errs) > 0 || doc == nil {
		// Caller already ran ValidateQuery and got past it; this
		// is defensive. A failed re-parse here means we couldn't
		// resolve fields against the schema — fail closed to
		// avoid surprising the operator with a permitted leak.
		return renderDenyEnvelope(scope.Name, []denyHit{{Type: "<unresolved>", Field: "<unresolved>", Pattern: "load_failed"}})
	}
	selections := schema.WalkFieldSelections(doc)

	hits := make([]denyHit, 0)
	seen := map[string]bool{} // dedupe duplicate (type, field) selections
	for _, sel := range selections {
		key := sel.ParentType + "|" + sel.FieldName
		if seen[key] {
			continue
		}
		seen[key] = true
		if m := auth.MatchAny(scope.DeniedFieldMatchers, sel.ParentType, sel.FieldName); m != nil {
			hits = append(hits, denyHit{
				Type:    sel.ParentType,
				Field:   sel.FieldName,
				Pattern: m.Pattern,
			})
		}
	}
	if len(hits) == 0 {
		return ""
	}
	return renderDenyEnvelope(scope.Name, hits)
}

// denyHit is one (type, field, matching-pattern) tuple rendered in
// the permission_denied envelope's denied_fields array.
type denyHit struct {
	Type    string `json:"type"`
	Field   string `json:"field"`
	Pattern string `json:"pattern"`
}

func renderDenyEnvelope(clientName string, hits []denyHit) string {
	enc, _ := json.Marshal(map[string]any{
		"error":            "permission_denied",
		"hint":             "clients.yml deny_fields rule blocks this query — pick different fields or ask the operator to broaden the policy",
		"denied_fields":    hits,
		"presented_client": clientName,
	})
	return string(enc)
}
