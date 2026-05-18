package schema

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// federationProbeQuery is the Apollo Federation v1/v2 metadata
// query. Subgraphs expose their own SDL (with @key + @external
// directives) under `_service.sdl`. A federated gateway proxies
// the union of its subgraphs' SDLs.
//
// Returns "" when the upstream isn't federated — non-federated
// GraphQL servers respond with an error or null, both of which
// scry treats as "skip the federation overlay".
const federationProbeQuery = `{ _service { sdl } }`

// ProbeFederation issues the federation metadata query against the
// upstream. Returns the subgraph SDL string on success, "" + nil
// when the upstream doesn't speak federation. Real transport
// errors propagate so the caller can decide whether to surface
// them (introspection success but federation probe failure is
// rare + actionable).
func ProbeFederation(ctx context.Context, endpoint string, auth func() string, httpClient *http.Client) (string, error) {
	body, _ := json.Marshal(map[string]string{"query": federationProbeQuery})
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build federation probe: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if auth != nil {
		if tok := auth(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("federation probe: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
	if err != nil {
		return "", fmt.Errorf("federation probe read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		// Non-2xx is treated as "not federated" rather than an
		// error — many GraphQL servers 400 unknown fields,
		// which is the spec-compliant response to a
		// non-federated upstream seeing `_service`.
		return "", nil
	}
	var env struct {
		Data struct {
			Service *struct {
				SDL string `json:"sdl"`
			} `json:"_service"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		// Malformed payload → not a federation server.
		return "", nil
	}
	if len(env.Errors) > 0 || env.Data.Service == nil {
		return "", nil
	}
	return env.Data.Service.SDL, nil
}

// SubgraphMap parses a federation SDL (the value returned by
// `_service.sdl` on a gateway or subgraph) and extracts the
// subgraph ownership map per type.
//
// Behaviour:
//
//   - Apollo Federation v2 SDL marks subgraph ownership via the
//     `@join__type(graph: NAME)` directive injected by the gateway
//     composition step. Scan declarations for that directive +
//     map the type name → first NAME we see.
//   - Federation v1 SDL (subgraph-side) doesn't carry that info,
//     so this returns an empty map. Operators of v1 deployments
//     don't get the overlay, but their search results stay
//     correct.
//
// Returns nil + nil when the SDL is empty or contains no
// join__type directives. Failure to parse is intentionally
// non-fatal — federation metadata is enrichment, not load-bearing.
func SubgraphMap(sdl string) map[string]string {
	if sdl == "" {
		return nil
	}
	out := map[string]string{}
	// Cheap scanner: walk lines looking for `type X ... @join__type(graph: NAME)`.
	// gqlparser doesn't expose @join__type natively; the
	// federation v2 spec defines it as a synthetic directive
	// emitted by the composer.
	for _, line := range strings.Split(sdl, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "type ") && !strings.HasPrefix(line, "interface ") {
			continue
		}
		// Pull `type X ...` name.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		typeName := fields[1]
		// Strip implements / curly.
		typeName = strings.TrimRight(typeName, "{")
		typeName = strings.TrimSpace(typeName)
		if typeName == "" {
			continue
		}
		// Find @join__type(graph: NAME) on the same line.
		idx := strings.Index(line, "@join__type(graph:")
		if idx < 0 {
			continue
		}
		rest := line[idx+len("@join__type(graph:"):]
		rest = strings.TrimSpace(rest)
		// Drop leading punctuation; pull the bare identifier.
		end := strings.IndexAny(rest, ",)")
		if end < 0 {
			continue
		}
		name := strings.TrimSpace(rest[:end])
		name = strings.Trim(name, "\"")
		if name == "" {
			continue
		}
		// Keep the first subgraph we see for the type. Federation
		// v2 lists every owner; the first one is the canonical
		// resolver for the type's @key fields.
		if _, ok := out[typeName]; !ok {
			out[typeName] = name
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ApplySubgraphTags walks a slice of SearchUnits + populates the
// Subgraph field from the map. Field-level units (Type.field)
// inherit their parent type's subgraph; that matches the agent's
// mental model — "which subgraph owns this thing".
//
// No-ops on an empty map so non-federated upstreams skip the work.
func ApplySubgraphTags(units []SearchUnit, subgraphs map[string]string) {
	if len(subgraphs) == 0 {
		return
	}
	for i, u := range units {
		var parent string
		switch u.Kind {
		case "field":
			parent = u.ParentType
		default:
			parent = u.Name
		}
		if sg, ok := subgraphs[parent]; ok {
			units[i].Subgraph = sg
		}
	}
}
