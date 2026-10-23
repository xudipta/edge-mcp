// Package server exposes an edge.Cluster as Model Context Protocol tools.
package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xudipta/edge-mcp/internal/edge"
)

// Version is set at build time with -ldflags.
var Version = "dev"

const instructions = `edge-mcp gives you read access (and, if enabled, guarded write access) to a Kubernetes cluster that runs at the edge: many small nodes spread across sites, often with GPUs/NPUs.
Start with cluster_overview, then drill down with list_edge_nodes, describe_edge_node, list_workloads, list_events and get_pod_logs.
Action tools default to dry_run=true. Always show the user the dry-run result and get their explicit go-ahead before calling an action again with dry_run=false.`

// New builds the MCP server and registers every tool the configuration allows.
func New(c *edge.Cluster) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "edge-mcp", Title: "Edge MCP", Version: Version},
		&mcp.ServerOptions{Instructions: instructions})
	addReadTools(s, c)
	if c.Config().AllowWrites {
		addActionTools(s, c)
	}
	return s
}

func readOnly(title string) *mcp.ToolAnnotations {
	closed := false
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: &closed}
}

type overviewIn struct{}

type nodesIn struct {
	Site          string `json:"site,omitempty" jsonschema:"only nodes at this site (value of the site label)"`
	UnhealthyOnly bool   `json:"unhealthy_only,omitempty" jsonschema:"only nodes that are NotReady, have a stale heartbeat, are under pressure or are cordoned"`
}

type nodesOut struct {
	Nodes []edge.NodeSummary `json:"nodes"`
}

type nodeIn struct {
	Name string `json:"name" jsonschema:"node name"`
}

type workloadsIn struct {
	Namespace     string `json:"namespace,omitempty" jsonschema:"namespace to inspect; empty means every allowed namespace"`
	UnhealthyOnly bool   `json:"unhealthy_only,omitempty" jsonschema:"only workloads with fewer ready or available replicas than desired"`
}

type workloadsOut struct {
	Workloads []edge.Workload `json:"workloads"`
}

type eventsIn struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"namespace to inspect; empty means every allowed namespace"`
	All       bool   `json:"all,omitempty" jsonschema:"include Normal events too; by default only Warnings are returned"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum events to return (default 30)"`
}

type eventsOut struct {
	Events []edge.EventBrief `json:"events"`
}

type logsIn struct {
	Namespace string `json:"namespace" jsonschema:"pod namespace"`
	Pod       string `json:"pod" jsonschema:"pod name"`
	Container string `json:"container,omitempty" jsonschema:"container name; required only for multi-container pods"`
	TailLines int64  `json:"tail_lines,omitempty" jsonschema:"lines from the end of the log (default 100, max 500)"`
	Previous  bool   `json:"previous,omitempty" jsonschema:"read the previous container instance, useful after a crash"`
}

type logsOut struct {
	Log string `json:"log"`
}

func addReadTools(s *mcp.Server, c *edge.Cluster) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "cluster_overview",
		Description: "One-glance health of the edge fleet: nodes ready per site, nodes needing attention, accelerators available, pod health and the latest warnings.",
		Annotations: readOnly("Cluster overview"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ overviewIn) (*mcp.CallToolResult, *edge.Overview, error) {
		o, err := c.Overview(ctx)
		return nil, o, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_edge_nodes",
		Description: "List edge nodes with their site, readiness, last heartbeat, pressure conditions, taints, OS/kubelet versions and accelerators (GPU, NPU, ...).",
		Annotations: readOnly("List edge nodes"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nodesIn) (*mcp.CallToolResult, nodesOut, error) {
		nodes, err := c.ListNodes(ctx, in.Site, in.UnhealthyOnly)
		return nil, nodesOut{Nodes: nodes}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "describe_edge_node",
		Description: "Everything about one node: its status, the pods running on it (with restarts and failure reasons) and its recent events.",
		Annotations: readOnly("Describe edge node"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nodeIn) (*mcp.CallToolResult, *edge.NodeDetail, error) {
		d, err := c.DescribeNode(ctx, in.Name)
		return nil, d, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_workloads",
		Description: "List Deployments, DaemonSets and StatefulSets with desired, ready and available replicas.",
		Annotations: readOnly("List workloads"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in workloadsIn) (*mcp.CallToolResult, workloadsOut, error) {
		w, err := c.ListWorkloads(ctx, in.Namespace, in.UnhealthyOnly)
		return nil, workloadsOut{Workloads: w}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_events",
		Description: "Recent cluster events, newest first. Warnings only unless all=true.",
		Annotations: readOnly("List events"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in eventsIn) (*mcp.CallToolResult, eventsOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 30
		}
		ev, err := c.ListEvents(ctx, in.Namespace, !in.All, limit)
		return nil, eventsOut{Events: ev}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_pod_logs",
		Description: "Tail of a container's log (at most 500 lines). Use previous=true to see why a crashed container died.",
		Annotations: readOnly("Get pod logs"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in logsIn) (*mcp.CallToolResult, logsOut, error) {
		log, err := c.PodLogs(ctx, in.Namespace, in.Pod, in.Container, in.TailLines, in.Previous)
		return nil, logsOut{Log: log}, err
	})
}
