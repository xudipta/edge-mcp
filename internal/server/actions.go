package server

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xudipta/edge-mcp/internal/edge"
)

// Every action takes these: a dry run is the default, and a reason is recorded in the audit log.
type guard struct {
	DryRun *bool  `json:"dry_run,omitempty" jsonschema:"validate without changing anything; defaults to true, set false only after the user approves"`
	Reason string `json:"reason" jsonschema:"why this action is being taken; written to the audit log"`
}

func (g guard) dryRun() bool { return g.DryRun == nil || *g.DryRun }

type cordonIn struct {
	Name string `json:"name" jsonschema:"node name"`
	guard
}

type drainIn struct {
	Name            string `json:"name" jsonschema:"node name"`
	DeleteLocalData bool   `json:"delete_local_data,omitempty" jsonschema:"allow evicting pods that use emptyDir storage, losing that data; default false"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty" jsonschema:"how long to keep retrying evictions blocked by a PodDisruptionBudget (default 120, max 600)"`
	guard
}

type restartIn struct {
	Kind      string `json:"kind" jsonschema:"Deployment, DaemonSet or StatefulSet"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	guard
}

type scaleIn struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name" jsonschema:"deployment name"`
	Replicas  int32  `json:"replicas" jsonschema:"desired replica count"`
	guard
}

func action(title string, destructive bool) *mcp.ToolAnnotations {
	closed := false
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: &destructive, IdempotentHint: true, OpenWorldHint: &closed}
}

func addActionTools(s *mcp.Server, c *edge.Cluster) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "cordon_node",
		Description: "Mark a node unschedulable so no new pods land on it (running pods stay). Dry run by default.",
		Annotations: action("Cordon node", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cordonIn) (*mcp.CallToolResult, *edge.ActionResult, error) {
		r, err := c.SetCordon(ctx, in.Name, true, in.dryRun(), in.Reason)
		return nil, r, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "uncordon_node",
		Description: "Make a cordoned node schedulable again. Dry run by default.",
		Annotations: action("Uncordon node", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cordonIn) (*mcp.CallToolResult, *edge.ActionResult, error) {
		r, err := c.SetCordon(ctx, in.Name, false, in.dryRun(), in.Reason)
		return nil, r, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "drain_node",
		Description: "Cordon a node and evict its pods through the Eviction API so PodDisruptionBudgets are honoured (like kubectl drain). " +
			"DaemonSet and static pods are left in place. Refuses, changing nothing, if a pod uses emptyDir storage (unless delete_local_data=true) or has no controller. " +
			"Warns if the node is the last Ready one at its site. Dry run by default: it lists exactly which pods would move and which a PodDisruptionBudget blocks. " +
			"This is the riskiest action: show the dry-run result and get explicit approval first.",
		Annotations: action("Drain node", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in drainIn) (*mcp.CallToolResult, *edge.DrainResult, error) {
		r, err := c.DrainNode(ctx, in.Name, in.DeleteLocalData, time.Duration(in.TimeoutSeconds)*time.Second, in.dryRun(), in.Reason)
		return nil, r, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "restart_workload",
		Description: "Rolling restart of a Deployment, DaemonSet or StatefulSet (like kubectl rollout restart). Dry run by default.",
		Annotations: action("Restart workload", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in restartIn) (*mcp.CallToolResult, *edge.ActionResult, error) {
		r, err := c.RestartWorkload(ctx, in.Kind, in.Namespace, in.Name, in.dryRun(), in.Reason)
		return nil, r, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "scale_deployment",
		Description: "Set a Deployment's replica count, capped by the server's --max-replicas. Dry run by default.",
		Annotations: action("Scale deployment", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in scaleIn) (*mcp.CallToolResult, *edge.ActionResult, error) {
		r, err := c.ScaleDeployment(ctx, in.Namespace, in.Name, in.Replicas, in.dryRun(), in.Reason)
		return nil, r, err
	})
}
