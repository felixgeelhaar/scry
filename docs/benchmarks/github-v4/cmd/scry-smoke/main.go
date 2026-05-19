package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/felixgeelhaar/scry/docs/benchmarks/github-v4/internal/mcpclient"
)

func main() {
	scryBin := "/tmp/scry-bench"
	cmd := exec.Command(scryBin, "serve",
		"--upstream", "https://api.github.com/graphql",
		"--auth", "env://GITHUB_TOKEN",
	)
	cmd.Env = os.Environ()

	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		fmt.Printf("start: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	go func() {
		s := bufio.NewScanner(stderr)
		s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for s.Scan() {
			fmt.Fprintf(os.Stderr, "[scry] %s\n", s.Text())
		}
	}()

	cli := mcpclient.New(stdout, stdin)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	t0 := time.Now()
	if err := cli.Initialize(ctx, "smoke", "0.0"); err != nil {
		fmt.Printf("initialize: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("initialize ok (%dms)\n", time.Since(t0).Milliseconds())

	tools, err := cli.ToolsList(ctx)
	if err != nil {
		fmt.Printf("tools/list: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("got %d tools:\n", len(tools))
	for _, t := range tools {
		fmt.Printf("  - %s\n", t.Name)
	}
}
