package edge_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stesting "k8s.io/client-go/testing"

	"github.com/xudipta/edge-mcp/internal/edge"
	"github.com/xudipta/edge-mcp/internal/edgetest"
)

func cluster(t *testing.T, mut func(*edge.Config)) (*edge.Cluster, context.Context) {
	t.Helper()
	cfg := edge.DefaultConfig()
	if mut != nil {
		mut(&cfg)
	}
	c := edge.New(edgetest.Fleet(), cfg).WithClock(func() time.Time { return edgetest.Now }).WithLogSource(edgetest.Logs)
	return c, context.Background()
}

func TestListNodesFlagsOfflineNode(t *testing.T) {
	c, ctx := cluster(t, nil)
	nodes, err := c.ListNodes(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Name != "blr-02" {
		t.Fatalf("unhealthy nodes = %+v, want only blr-02", nodes)
	}
	n := nodes[0]
	if n.Ready || !n.StaleHeartbeat || n.LastHeartbeat != "9m" || n.Site != "store-blr" {
		t.Errorf("blr-02 summary = %+v", n)
	}
}

func TestListNodesBySiteAndAccelerators(t *testing.T) {
	c, ctx := cluster(t, nil)
	nodes, err := c.ListNodes(ctx, "factory-pune", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes at factory-pune, want 2", len(nodes))
	}
	for _, n := range nodes {
		if n.Accelerators["gpu.intel.com/i915"] != "1" {
			t.Errorf("%s accelerators = %v, want an Intel GPU", n.Name, n.Accelerators)
		}
		if _, ok := n.Accelerators["cpu"]; ok {
			t.Errorf("%s: core resources must not be reported as accelerators", n.Name)
		}
		if !slices.Equal(n.Roles, []string{"edge"}) {
			t.Errorf("%s roles = %v", n.Name, n.Roles)
		}
	}
}

func TestOverview(t *testing.T) {
	c, ctx := cluster(t, nil)
	o, err := c.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.Nodes != 5 || o.NodesReady != 4 {
		t.Errorf("nodes = %d ready = %d, want 5 / 4", o.Nodes, o.NodesReady)
	}
	if got := o.Sites["store-blr"]; got.Nodes != 2 || got.Ready != 1 {
		t.Errorf("store-blr = %+v", got)
	}
	if o.Accelerators["gpu.intel.com/i915"] != 2 || o.Accelerators["npu.intel.com/accel"] != 1 {
		t.Errorf("accelerators = %v", o.Accelerators)
	}
	if o.PodsFailing != 1 || o.PodsRunning != 3 {
		t.Errorf("pods running=%d failing=%d, want 3 / 1", o.PodsRunning, o.PodsFailing)
	}
	if len(o.RecentWarnings) != 1 || o.RecentWarnings[0].Reason != "BackOff" {
		t.Errorf("warnings = %+v", o.RecentWarnings)
	}
}

func TestDescribeNodeShowsCrashLoop(t *testing.T) {
	c, ctx := cluster(t, nil)
	d, err := c.DescribeNode(ctx, "pune-02")
	if err != nil {
		t.Fatal(err)
	}
	var crash bool
	for _, p := range d.Pods {
		if p.Name == "defect-detector-c" && p.Reason == "CrashLoopBackOff" && p.Restarts == 7 && p.Ready == "0/1" {
			crash = true
		}
	}
	if len(d.Pods) != 2 || !crash {
		t.Errorf("pods on pune-02 = %+v", d.Pods)
	}
}

func TestWorkloadsUnhealthyOnly(t *testing.T) {
	c, ctx := cluster(t, nil)
	w, err := c.ListWorkloads(ctx, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 1 || w[0].Name != "defect-detector" || w[0].Ready != 2 || w[0].Desired != 3 {
		t.Errorf("unhealthy workloads = %+v", w)
	}
}

func TestNamespaceAllowlist(t *testing.T) {
	c, ctx := cluster(t, func(cfg *edge.Config) { cfg.Namespaces = []string{"retail"} })
	if _, err := c.ListWorkloads(ctx, "vision", false); err == nil {
		t.Error("listing a namespace outside the allowlist should fail")
	}
	w, err := c.ListWorkloads(ctx, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 1 || w[0].Namespace != "retail" {
		t.Errorf("workloads = %+v, want only retail", w)
	}
	if _, err := c.PodLogs(ctx, "vision", "defect-detector-c", "", 10, false); err == nil {
		t.Error("reading logs outside the allowlist should fail")
	}
	d, err := c.DescribeNode(ctx, "pune-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Pods) != 0 {
		t.Errorf("pods outside the allowlist leaked: %+v", d.Pods)
	}
}

func TestActionsAreReadOnlyByDefault(t *testing.T) {
	c, ctx := cluster(t, nil)
	if _, err := c.SetCordon(ctx, "blr-02", true, false, "test"); !errors.Is(err, edge.ErrReadOnly) {
		t.Errorf("cordon without --allow-writes: err = %v, want ErrReadOnly", err)
	}
	if _, err := c.ScaleDeployment(ctx, "vision", "defect-detector", 1, false, "test"); !errors.Is(err, edge.ErrReadOnly) {
		t.Errorf("scale without --allow-writes: err = %v, want ErrReadOnly", err)
	}
}

func TestCordonAndScale(t *testing.T) {
	client := edgetest.Fleet()
	cfg := edge.DefaultConfig()
	cfg.AllowWrites = true
	c := edge.New(client, cfg)
	ctx := context.Background()

	r, err := c.SetCordon(ctx, "blr-02", true, false, "node offline for 9m")
	if err != nil {
		t.Fatal(err)
	}
	if r.DryRun {
		t.Error("expected a real cordon")
	}
	n, _ := client.CoreV1().Nodes().Get(ctx, "blr-02", metav1.GetOptions{})
	if !n.Spec.Unschedulable {
		t.Error("blr-02 should be cordoned")
	}

	if _, err := c.ScaleDeployment(ctx, "vision", "defect-detector", 99, false, "test"); err == nil {
		t.Error("scaling above --max-replicas should fail")
	}
	if _, err := c.ScaleDeployment(ctx, "vision", "defect-detector", 5, false, "more capacity"); err != nil {
		t.Fatal(err)
	}
	d, _ := client.AppsV1().Deployments("vision").Get(ctx, "defect-detector", metav1.GetOptions{})
	if *d.Spec.Replicas != 5 {
		t.Errorf("replicas = %d, want 5", *d.Spec.Replicas)
	}

	if _, err := c.RestartWorkload(ctx, "Deployment", "vision", "defect-detector", false, "pick up new model"); err != nil {
		t.Fatal(err)
	}
	d, _ = client.AppsV1().Deployments("vision").Get(ctx, "defect-detector", metav1.GetOptions{})
	if d.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] == "" {
		t.Error("restart should stamp the pod template")
	}
	if _, err := c.RestartWorkload(ctx, "CronJob", "vision", "x", true, "test"); err == nil {
		t.Error("unsupported kinds should be rejected")
	}
}

func TestDryRunIsSentToTheAPIServer(t *testing.T) {
	client := edgetest.Fleet()
	cfg := edge.DefaultConfig()
	cfg.AllowWrites = true
	c := edge.New(client, cfg)
	ctx := context.Background()

	if _, err := c.SetCordon(ctx, "blr-02", true, true, "check"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ScaleDeployment(ctx, "vision", "defect-detector", 4, true, "check"); err != nil {
		t.Fatal(err)
	}
	patches := 0
	for _, a := range client.Actions() {
		p, ok := a.(k8stesting.PatchActionImpl)
		if !ok {
			continue
		}
		patches++
		if opts := p.GetPatchOptions(); !slices.Equal(opts.DryRun, []string{metav1.DryRunAll}) || opts.FieldManager != "edge-mcp" {
			t.Errorf("%s %s: patch options = %+v, want dryRun=All and fieldManager=edge-mcp", p.GetVerb(), p.GetName(), opts)
		}
	}
	if patches != 2 {
		t.Errorf("saw %d patch requests, want 2", patches)
	}
}

func TestDemoLogsExplainCrash(t *testing.T) {
	c, ctx := cluster(t, nil)
	log, err := c.PodLogs(ctx, "vision", "defect-detector-c", "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "failed to load model") {
		t.Errorf("crash log should name the reason, got:\n%s", log)
	}
	tail, err := c.PodLogs(ctx, "vision", "defect-detector-c", "", 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(tail, "\n"); n != 2 {
		t.Errorf("tail_lines=2 returned %d lines:\n%s", n, tail)
	}
	healthy, err := c.PodLogs(ctx, "vision", "defect-detector-a", "", 0, false)
	if err != nil || !strings.Contains(healthy, "listening") || strings.Contains(healthy, "fake logs") {
		t.Errorf("healthy log = %q, err = %v", healthy, err)
	}
}
