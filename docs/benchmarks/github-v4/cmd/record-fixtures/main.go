// record-fixtures runs the canonical task against GitHub's GraphQL
// API directly (hand-written query, no agent in the loop) and
// writes the deterministic ground truth to fixtures/expected.json.
//
// Re-recording the fixture invalidates every prior bench result —
// gate this behind `make bench-fixtures` and commit the output so
// the bench remains reproducible from a clean checkout.
//
// Usage:
//
//	GITHUB_TOKEN=ghp_... go run ./cmd/record-fixtures -out fixtures/expected.json
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/felixgeelhaar/scry/docs/benchmarks/github-v4/internal/task"
)

func main() {
	out := flag.String("out", "fixtures/expected.json", "path to write ground-truth JSON")
	flag.Parse()

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "GITHUB_TOKEN required")
		os.Exit(2)
	}

	p := task.Default
	prs, err := fetchPRs(token, p)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch: %v\n", err)
		os.Exit(1)
	}
	sort.Slice(prs, func(i, j int) bool { return prs[i].Number < prs[j].Number })

	expected := task.Expected{Params: p, PullRequests: prs}
	buf, err := json.MarshalIndent(expected, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	buf = append(buf, '\n')
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d PRs to %s\n", len(prs), *out)
}

// fetchPRs runs a single GitHub GraphQL search query, filters to PRs
// whose files include the target path, and projects each PR into
// the task.PR shape. Search has a 100-result page cap — the bench
// window deliberately produces single-digit hits so no pagination
// is needed.
func fetchPRs(token string, p task.Params) ([]task.PR, error) {
	// GitHub search query syntax. is:pr + author + repo + merged
	// date range. File filter applied client-side below since the
	// search API doesn't take a path filter for PRs.
	q := fmt.Sprintf("repo:%s author:%s is:pr merged:%s..%s",
		p.Repo, p.Author, p.Since, p.Until)

	type fileNode struct {
		Path string `json:"path"`
	}
	type reviewNode struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
	}
	type prNode struct {
		Number   int    `json:"number"`
		Title    string `json:"title"`
		MergedAt string `json:"mergedAt"`
		Files    struct {
			Nodes []fileNode `json:"nodes"`
		} `json:"files"`
		Reviews struct {
			Nodes []reviewNode `json:"nodes"`
		} `json:"reviews"`
	}
	var resp struct {
		Data struct {
			Search struct {
				Nodes []prNode `json:"nodes"`
			} `json:"search"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	query := `
query($q: String!) {
  search(query: $q, type: ISSUE, first: 100) {
    nodes {
      ... on PullRequest {
        number
        title
        mergedAt
        files(first: 50) { nodes { path } }
        reviews(first: 20) { nodes { author { login } } }
      }
    }
  }
}`
	body, err := postGraphQL(token, query, map[string]any{"q": q})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("graphql errors: %v", resp.Errors)
	}

	var out []task.PR
	for _, n := range resp.Data.Search.Nodes {
		// Client-side file filter — search API can't do it.
		matched := false
		for _, f := range n.Files.Nodes {
			if f.Path == p.File {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		reviewers := map[string]struct{}{}
		for _, rv := range n.Reviews.Nodes {
			if rv.Author.Login != "" {
				reviewers[rv.Author.Login] = struct{}{}
			}
		}
		revs := make([]string, 0, len(reviewers))
		for r := range reviewers {
			revs = append(revs, r)
		}
		sort.Strings(revs)
		out = append(out, task.PR{
			Number:    n.Number,
			Title:     n.Title,
			Merged:    n.MergedAt != "",
			Reviewers: revs,
		})
	}
	return out, nil
}

// postGraphQL is a thin wrapper around api.github.com/graphql.
func postGraphQL(token, query string, vars map[string]any) ([]byte, error) {
	payload, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": vars,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "scry-bench/0.6")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
