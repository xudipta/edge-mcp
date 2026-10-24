# Contributing to edge-mcp

Thanks for helping. Bug reports, docs fixes and new tools are all welcome.

## Build, test, run

You need the Go version in `go.mod`.

```sh
make build    # ./bin/edge-mcp
make test     # go test -race ./... (fake clientset, no cluster needed)
make lint     # gofmt + go vet
make demo     # run against the built-in sample fleet
```

Try the server by hand without a cluster: `./bin/edge-mcp --demo` speaks MCP over stdio, or add `--http 127.0.0.1:8080` for Streamable HTTP.

## Adding a tool

1. **Edge method**: add the logic to `internal/edge` (`nodes.go`, `workloads.go`, or `actions.go` for writes). It should return plain structs, honour `checkNamespace`, and never talk to MCP.
2. **MCP registration**: register the tool in `internal/server/server.go` (reads) or `internal/server/actions.go` (writes, only when `AllowWrites`). Give every input field a `jsonschema` description; mark read tools with `readOnly(...)`.
3. **Test**: cover the edge method in `internal/edge/edge_test.go` against `edgetest.Fleet()`, and the tool end to end in `internal/server/server_test.go`. Extend `internal/edgetest/fleet.go` if you need new fixtures.
4. **Docs**: add the tool to the README tool list.

Write tools must dry-run by default, respect the namespace allowlist, and log through `Cluster.audit` in `internal/edge/actions.go`.

## Pull requests

- One logical change per PR; link the issue it closes.
- `make lint test` must pass.
- Describe what you changed and how you verified it.

By contributing you agree that your work is licensed under the Apache 2.0 license.
