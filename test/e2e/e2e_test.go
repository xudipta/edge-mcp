//go:build e2e

// Package e2e runs edge-mcp against a real cluster (kind or k3d) prepared by setup.sh.
// It talks to the built binary over stdio with a real MCP client, using only the
// service account from deploy/rbac-readonly.yaml, then deploy/rbac-operator.yaml for writes.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func env(t *testing.T, key, def string) string {
	t.Helper()
	if v := os.Getenv(key); v != "" {
		return v
	}
	if def == "" {
		t.Fatalf("%s must be set (see test/e2e/setup.sh)", key)
	}
	return def
}

func sh(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// nodeWith returns the node carrying a label like e2e/solo=true.
func nodeWith(t *testing.T, selector string) string {
	t.Helper()
	return sh(t, "kubectl", "get", "nodes", "-l", selector, "-o", "jsonpath={.items[0].metadata.name}")
}

// start runs edge-mcp against the read-only kubeconfig and returns a connected MCP client.
func start(t *testing.T, args ...string) *mcp.ClientSession {
	t.Helper()
	bin := env(t, "E2E_BIN", "../../bin/edge-mcp")
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+env(t, "E2E_READONLY_KUBECONFIG", ""))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t.Cleanup(cancel)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// call invokes a tool and decodes its structured result into out (when not nil).
func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if out != nil && !res.IsError {
		raw, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: decoding result: %v\n%s", tool, err, raw)
		}
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func eventually(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

type node struct {
	Name          string `json:"name"`
	Site          string `json:"site"`
	Ready         bool   `json:"ready"`
	Status        string `json:"status"`
	Unschedulable bool   `json:"unschedulable"`
	Stale         bool   `json:"stale_heartbeat"`
	LastHeartbeat string `json:"last_heartbeat"`
}

func listNodes(t *testing.T, cs *mcp.ClientSession, args map[string]any) []node {
	t.Helper()
	var out struct {
		Nodes []node `json:"nodes"`
	}
	call(t, cs, "list_edge_nodes", args, &out)
	return out.Nodes
}

type drainOut struct {
	DryRun   bool     `json:"dry_run"`
	Complete bool     `json:"complete"`
	Message  string   `json:"message"`
	Evicted  []string `json:"evicted"`
	Skipped  []struct{ Name, Reason string }
	Blocked  []struct{ Namespace, Name, Reason string }
	Warnings []string `json:"warnings"`
}

func TestEdgeMCPAgainstRealCluster(t *testing.T) {
	web, cache, solo := nodeWith(t, "e2e/pool=web"), nodeWith(t, "e2e/cache=true"), nodeWith(t, "e2e/solo=true")

	t.Run("read tools", func(t *testing.T) {
		cs := start(t)

		var ov struct {
			Nodes      int                       `json:"nodes"`
			NodesReady int                       `json:"nodes_ready"`
			Sites      map[string]map[string]int `json:"sites"`
		}
		call(t, cs, "cluster_overview", nil, &ov)
		if ov.Nodes < 4 || ov.NodesReady != ov.Nodes {
			t.Errorf("overview nodes=%d ready=%d, want all (>=4) ready", ov.Nodes, ov.NodesReady)
		}
		if ov.Sites["store-a"]["nodes"] != 2 || ov.Sites["store-b"]["nodes"] != 1 {
			t.Errorf("sites = %v, want store-a:2 and store-b:1", ov.Sites)
		}

		if n := listNodes(t, cs, map[string]any{"site": "store-a"}); len(n) != 2 {
			t.Errorf("store-a nodes = %+v, want 2", n)
		}
		if n := listNodes(t, cs, map[string]any{"unhealthy_only": true}); len(n) != 0 {
			t.Errorf("unhealthy nodes on a fresh cluster: %+v", n)
		}

		var d struct {
			Pods []struct{ Namespace, Name string } `json:"pods"`
		}
		call(t, cs, "describe_edge_node", map[string]any{"name": solo}, &d)
		if !slices.ContainsFunc(d.Pods, func(p struct{ Namespace, Name string }) bool { return strings.HasPrefix(p.Name, "solo-") }) {
			t.Errorf("describe %s pods = %+v, want the solo pod", solo, d.Pods)
		}

		var wl struct {
			Workloads []struct {
				Name    string `json:"name"`
				Healthy bool   `json:"healthy"`
			} `json:"workloads"`
		}
		call(t, cs, "list_workloads", map[string]any{"namespace": "e2e", "unhealthy_only": true}, &wl)
		if len(wl.Workloads) != 1 || wl.Workloads[0].Name != "crashy" {
			t.Errorf("unhealthy workloads = %+v, want only crashy", wl.Workloads)
		}

		var ro struct {
			TargetImage string `json:"target_image"`
			Sites       []struct {
				Site string `json:"site"`
				Done bool   `json:"done"`
			} `json:"sites"`
			Complete bool `json:"complete"`
		}
		call(t, cs, "rollout_status", map[string]any{"kind": "Deployment", "namespace": "e2e", "name": "web"}, &ro)
		if !ro.Complete || !strings.Contains(ro.TargetImage, "pause") {
			t.Errorf("rollout_status(web) = %+v, want complete", ro)
		}

		if res := call(t, cs, "accelerator_usage", nil, nil); res.IsError {
			t.Errorf("accelerator_usage failed: %s", text(res))
		}
		if res := call(t, cs, "list_events", map[string]any{"namespace": "e2e", "all": true}, nil); res.IsError {
			t.Errorf("list_events failed: %s", text(res))
		}

		// Crash loop: the previous container's log explains why it died.
		var logs struct {
			Log string `json:"log"`
		}
		eventually(t, 3*time.Minute, "crashy's previous log", func() bool {
			pod := sh(t, "kubectl", "-n", "e2e", "get", "pod", "-l", "app=crashy", "-o", "jsonpath={.items[0].metadata.name}")
			res := call(t, cs, "get_pod_logs", map[string]any{"namespace": "e2e", "pod": pod, "previous": true}, &logs)
			return !res.IsError && strings.Contains(logs.Log, "model load failed")
		})
	})

	t.Run("read-only RBAC: writes fail cleanly", func(t *testing.T) {
		cs := start(t, "--allow-writes")
		for _, dry := range []bool{true, false} {
			res := call(t, cs, "cordon_node", map[string]any{"name": solo, "reason": "e2e", "dry_run": dry}, nil)
			if !res.IsError || !strings.Contains(strings.ToLower(text(res)), "forbidden") {
				t.Errorf("cordon (dry_run=%v) with read-only RBAC: isError=%v text=%q, want a forbidden error", dry, res.IsError, text(res))
			}
		}
		if n := listNodes(t, cs, map[string]any{"site": "store-b"}); len(n) != 1 || n[0].Unschedulable {
			t.Errorf("a refused cordon changed the node: %+v", n)
		}
	})

	// From here on the service account also has deploy/rbac-operator.yaml.
	sh(t, "kubectl", "apply", "-f", filepath.Join("..", "..", "deploy", "rbac-operator.yaml"))

	t.Run("cordon: dry run changes nothing, real run does", func(t *testing.T) {
		cs := start(t, "--allow-writes", "--namespaces", "e2e")
		var r struct {
			DryRun  bool   `json:"dry_run"`
			Message string `json:"message"`
		}
		eventually(t, 30*time.Second, "operator RBAC to take effect", func() bool {
			res := call(t, cs, "cordon_node", map[string]any{"name": solo, "reason": "e2e"}, &r)
			return !res.IsError
		})
		if !r.DryRun {
			t.Errorf("default should be a dry run: %+v", r)
		}
		if n := listNodes(t, cs, map[string]any{"site": "store-b"}); n[0].Unschedulable {
			t.Fatal("dry-run cordon changed the node")
		}
		call(t, cs, "cordon_node", map[string]any{"name": solo, "reason": "e2e", "dry_run": false}, &r)
		if n := listNodes(t, cs, map[string]any{"site": "store-b"}); !n[0].Unschedulable {
			t.Fatal("real cordon did not cordon the node")
		}
		call(t, cs, "uncordon_node", map[string]any{"name": solo, "reason": "e2e", "dry_run": false}, &r)
		if n := listNodes(t, cs, map[string]any{"site": "store-b"}); n[0].Unschedulable {
			t.Fatal("uncordon did not make the node schedulable")
		}
	})

	t.Run("drain: PDB blocks, emptyDir refuses, solo node drains", func(t *testing.T) {
		cs := start(t, "--allow-writes", "--namespaces", "e2e")
		var d drainOut

		// web's PDB allows no disruption: the dry run reports every web pod as blocked.
		call(t, cs, "drain_node", map[string]any{"name": web, "reason": "e2e"}, &d)
		if !d.DryRun || d.Complete || len(d.Blocked) == 0 || !strings.Contains(strings.Join(d.Warnings, "\n"), "PodDisruptionBudget") {
			t.Errorf("dry-run drain of %s: %+v, want PDB-blocked pods and a PDB warning", web, d)
		}
		if n := listNodes(t, cs, nil); slices.ContainsFunc(n, func(x node) bool { return x.Name == web && x.Unschedulable }) {
			t.Error("dry-run drain cordoned the node")
		}

		// A real drain against the PDB gives up at the timeout, evicts nothing, leaves the node cordoned.
		call(t, cs, "drain_node", map[string]any{"name": web, "reason": "e2e", "dry_run": false, "timeout_seconds": 8}, &d)
		if d.Complete || len(d.Evicted) != 0 || len(d.Blocked) == 0 {
			t.Errorf("real drain against the PDB: %+v, want nothing evicted and pods reported", d)
		}
		call(t, cs, "uncordon_node", map[string]any{"name": web, "reason": "e2e", "dry_run": false}, nil)
		if got := sh(t, "kubectl", "-n", "e2e", "get", "deploy", "web", "-o", "jsonpath={.status.readyReplicas}"); got != "3" {
			t.Errorf("web has %s ready replicas after the blocked drain, want 3", got)
		}

		// emptyDir: refused, nothing changed, then allowed explicitly (dry run only here).
		call(t, cs, "drain_node", map[string]any{"name": cache, "reason": "e2e", "dry_run": false}, &d)
		if d.Complete || !strings.Contains(d.Message, "refused") {
			t.Errorf("drain of %s with an emptyDir pod: %+v, want a refusal", cache, d)
		}
		if n := listNodes(t, cs, nil); slices.ContainsFunc(n, func(x node) bool { return x.Name == cache && x.Unschedulable }) {
			t.Error("a refused drain cordoned the node")
		}

		// The solo node: no PDB, no local data. It is also the only node at store-b.
		call(t, cs, "drain_node", map[string]any{"name": solo, "reason": "e2e", "dry_run": false, "timeout_seconds": 60}, &d)
		if !d.Complete || !slices.ContainsFunc(d.Evicted, func(s string) bool { return strings.HasPrefix(s, "e2e/solo-") }) {
			t.Errorf("drain of %s: %+v, want the solo pod evicted", solo, d)
		}
		if !strings.Contains(strings.Join(d.Warnings, "\n"), "store-b") {
			t.Errorf("warnings = %v, want a last-node-at-site warning", d.Warnings)
		}
		if n := listNodes(t, cs, map[string]any{"site": "store-b"}); !n[0].Unschedulable {
			t.Error("drained node is not cordoned")
		}
		// Nothing is allowed to schedule onto the cordoned node, so the solo pod stays Pending.
		eventually(t, 60*time.Second, "solo pod to be evicted and unable to reschedule", func() bool {
			return sh(t, "kubectl", "-n", "e2e", "get", "pods", "-l", "app=solo", "--field-selector", "status.phase=Pending", "-o", "name") != ""
		})
		call(t, cs, "uncordon_node", map[string]any{"name": solo, "reason": "e2e", "dry_run": false}, nil)
		sh(t, "kubectl", "-n", "e2e", "rollout", "status", "deploy/solo", "--timeout=120s")
	})

	t.Run("node outage is flagged with a stale heartbeat", func(t *testing.T) {
		container := sh(t, "docker", "ps", "-a", "--filter", "name=^"+solo+"$", "--format", "{{.Names}}")
		if container != solo {
			t.Skipf("node %q is not a local docker container (not kind/k3d), skipping", solo)
		}
		cs := start(t, "--stale-heartbeat", "20s")
		sh(t, "docker", "stop", solo)
		t.Cleanup(func() { _ = exec.Command("docker", "start", solo).Run() })

		var down node
		eventually(t, 4*time.Minute, fmt.Sprintf("%s to be flagged unhealthy", solo), func() bool {
			for _, n := range listNodes(t, cs, map[string]any{"unhealthy_only": true}) {
				if n.Name == solo && !n.Ready {
					down = n
					return true
				}
			}
			return false
		})
		if down.LastHeartbeat == "" || !down.Stale {
			t.Errorf("stopped node = %+v, want a stale last heartbeat", down)
		}
		var ov struct {
			Attention []string `json:"nodes_needing_attention"`
		}
		call(t, cs, "cluster_overview", nil, &ov)
		if !slices.Contains(ov.Attention, solo) {
			t.Errorf("overview attention = %v, want %s", ov.Attention, solo)
		}
	})
}
