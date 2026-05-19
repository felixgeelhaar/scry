# baseline runner

Loads `../fixtures/github-v4.sdl`, embeds it in Claude's system
prompt, lets Claude emit a GraphQL query via tool-use, executes
against GitHub v4, scores against `../fixtures/expected.json`.

Run via `make bench-baseline` from the parent directory. See
parent README.md for env vars.

Implementation lands in `v06-bench-baseline-runner`.
