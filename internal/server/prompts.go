package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xudipta/edge-mcp/internal/edge"
)

func userPrompt(description, text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{
		Description: description,
		Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
	}
}

func addPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "triage_fleet",
		Title:       "Triage my fleet",
		Description: "Walk the whole fleet from overview to logs and summarise problems by site with next steps.",
	}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return userPrompt("Triage the edge fleet", `Triage my edge fleet. Work through these steps, using the edge-mcp tools:
1. Call cluster_overview for the big picture.
2. Call list_edge_nodes with unhealthy_only=true, then describe_edge_node for each node that needs attention.
3. Call list_workloads with unhealthy_only=true and, for each degraded workload, list_events and get_pod_logs (use previous=true for crash-looping pods) to find the cause.
4. Summarise by site: what is wrong, how sure you are of the cause, and whether the site can still serve traffic.
5. Suggest concrete next steps in priority order. Do not run any action tool; just propose actions.`), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "site_report",
		Title:       "Site status report",
		Description: "A status report for one site.",
		Arguments: []*mcp.PromptArgument{
			{Name: "site", Title: "Site", Description: "value of the site label, e.g. store-blr (see cluster_overview for the list)", Required: true},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		site := strings.TrimSpace(req.Params.Arguments["site"])
		if site == "" {
			return nil, errors.New("argument \"site\" is required")
		}
		return userPrompt("Status report for "+site, fmt.Sprintf(`Write a status report for the edge site %q.
Use list_edge_nodes with site=%q and describe_edge_node for each node. Cover: node health and heartbeat age, accelerators present, workloads running on the site and any that are unhealthy, and recent warning events.
End with a one-line verdict (healthy / degraded / down) and any recommended next steps. Do not run any action tool.`, site, site)), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name:        "incident_summary",
		Title:       "Incident summary for a namespace",
		Description: "Recent warnings and crash reasons for one team's namespace.",
		Arguments: []*mcp.PromptArgument{
			{Name: "namespace", Title: "Namespace", Description: "the team's Kubernetes namespace, e.g. vision", Required: true},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		ns := strings.TrimSpace(req.Params.Arguments["namespace"])
		if ns == "" {
			return nil, errors.New("argument \"namespace\" is required")
		}
		return userPrompt("Incident summary for "+ns, fmt.Sprintf(`Summarise the current incident picture for namespace %q.
Use list_workloads (namespace=%q, unhealthy_only=true) and list_events (namespace=%q). For each failing pod, read get_pod_logs (previous=true if it is crash-looping) to find the crash reason.
Report: what is failing, since when, the likely cause with the evidence you saw, which sites are affected, and suggested next steps. Do not run any action tool.`, ns, ns, ns)), nil
	})
}

func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(b)}}}, nil
}

func addResources(s *mcp.Server, c *edge.Cluster) {
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "node",
		Title:       "Edge node snapshot",
		Description: "JSON snapshot of one node: health, heartbeat, accelerators, pods and recent events.",
		MIMEType:    "application/json",
		URITemplate: "edge://nodes/{name}",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := req.Params.URI
		name, ok := strings.CutPrefix(uri, "edge://nodes/")
		if !ok || name == "" || strings.Contains(name, "/") {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		d, err := c.DescribeNode(ctx, name)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return jsonResource(uri, d)
	})

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "site",
		Title:       "Edge site snapshot",
		Description: "JSON snapshot of every node at one site, with pods and recent events.",
		MIMEType:    "application/json",
		URITemplate: "edge://sites/{site}",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		uri := req.Params.URI
		site, ok := strings.CutPrefix(uri, "edge://sites/")
		if !ok || site == "" || strings.Contains(site, "/") {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		snap, err := c.SiteSnapshot(ctx, site)
		if err != nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return jsonResource(uri, snap)
	})
}
