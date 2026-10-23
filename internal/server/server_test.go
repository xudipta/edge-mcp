package server_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xudipta/edge-mcp/internal/edge"
	"github.com/xudipta/edge-mcp/internal/edgetest"
	"github.com/xudipta/edge-mcp/internal/server"
)

// connect starts edge-mcp over an in-memory transport and returns a connected MCP client.
func connect(t *testing.T, cfg edge.Config) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	c := edge.New(edgetest.Fleet(), cfg).WithClock(func() time.Time { return edgetest.Now })
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.New(c).Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	var names []string
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil && !res.IsError {
		raw, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: decoding structured output: %v", name, err)
		}
	}
	return res
}

func TestReadOnlyServerExposesOnlyReadTools(t *testing.T) {
	cs := connect(t, edge.DefaultConfig())
	want := []string{"cluster_overview", "describe_edge_node", "get_pod_logs", "list_edge_nodes", "list_events", "list_workloads", "rollout_status"}
	if got := toolNames(t, cs); !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s should be annotated read-only", tool.Name)
		}
	}
}

func TestOverviewOverMCP(t *testing.T) {
	cs := connect(t, edge.DefaultConfig())
	var o edge.Overview
	res := call(t, cs, "cluster_overview", nil, &o)
	if res.IsError {
		t.Fatalf("cluster_overview failed: %+v", res.Content)
	}
	if o.Nodes != 5 || o.NodesReady != 4 || !slices.Equal(o.NodesAttention, []string{"blr-02"}) {
		t.Errorf("overview = %+v", o)
	}
	// Text content is also filled in, for clients that don't read structured output.
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "blr-02") {
		t.Errorf("text content should mention blr-02: %+v", res.Content)
	}
}

func TestUnhealthyNodesOverMCP(t *testing.T) {
	cs := connect(t, edge.DefaultConfig())
	var out struct{ Nodes []edge.NodeSummary }
	call(t, cs, "list_edge_nodes", map[string]any{"unhealthy_only": true}, &out)
	if len(out.Nodes) != 1 || out.Nodes[0].Name != "blr-02" || out.Nodes[0].LastHeartbeat != "9m" {
		t.Errorf("nodes = %+v", out.Nodes)
	}
}

func TestAllowlistErrorsReachTheModel(t *testing.T) {
	cfg := edge.DefaultConfig()
	cfg.Namespaces = []string{"retail"}
	cs := connect(t, cfg)
	res := call(t, cs, "list_workloads", map[string]any{"namespace": "vision"}, nil)
	if !res.IsError {
		t.Fatal("expected a tool error for a namespace outside the allowlist")
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "allowlist") {
		t.Errorf("error text = %q", text)
	}
}

func TestActionsDefaultToDryRun(t *testing.T) {
	cfg := edge.DefaultConfig()
	cfg.AllowWrites = true
	cs := connect(t, cfg)
	if got := toolNames(t, cs); !slices.Contains(got, "cordon_node") || !slices.Contains(got, "scale_deployment") {
		t.Fatalf("action tools missing with --allow-writes: %v", got)
	}

	var r edge.ActionResult
	call(t, cs, "cordon_node", map[string]any{"name": "blr-02", "reason": "offline"}, &r)
	if !r.DryRun || !strings.Contains(r.Message, "dry run") {
		t.Errorf("cordon without dry_run should be a dry run: %+v", r)
	}
	call(t, cs, "cordon_node", map[string]any{"name": "blr-02", "reason": "offline", "dry_run": false}, &r)
	if r.DryRun || r.Message != "cordoned" {
		t.Errorf("cordon with dry_run=false: %+v", r)
	}
}
