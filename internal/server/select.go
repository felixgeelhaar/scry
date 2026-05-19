package server

import (
	"encoding/json"
	"fmt"

	"github.com/jmespath/go-jmespath"
)

// applyJMESPath projects body through the given JMESPath expression
// and returns the JSON-encoded result. body is expected to be a JSON
// document (typical upstream response shape). Errors:
//
//   - body fails to decode (not JSON) → "response is not JSON"
//   - expr fails to compile → "syntax: …"
//   - expr applies but returns null → "expression matched nothing"
//     (intentional — distinguish typo'd path from upstream null)
//
// The result is JSON-encoded with default settings (compact, no
// trailing newline) so cache-key + response-bytes accounting stays
// deterministic across runs.
func applyJMESPath(expr string, body []byte) ([]byte, error) {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("response is not JSON: %w", err)
	}
	compiled, err := jmespath.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("syntax: %w", err)
	}
	result, err := compiled.Search(data)
	if err != nil {
		return nil, fmt.Errorf("evaluation: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("expression matched nothing (typo'd field path?)")
	}
	out, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("re-encode: %w", err)
	}
	return out, nil
}
