package edge_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/xudipta/edge-mcp/internal/edge"
	"github.com/xudipta/edge-mcp/internal/edgetest"
)

func drainCluster(t *testing.T, client *fake.Clientset) *edge.Cluster {
	t.Helper()
	cfg := edge.DefaultConfig()
	cfg.AllowWrites = true
	return edge.New(client, cfg).WithClock(func() time.Time { return edgetest.Now }).WithDrainPoll(10 * time.Millisecond)
}

func unschedulable(t *testing.T, client *fake.Clientset, node string) bool {
	t.Helper()
	n, err := client.CoreV1().Nodes().Get(context.Background(), node, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return n.Spec.Unschedulable
}

func podExists(client *fake.Clientset, ns, name string) bool {
	_, err := client.CoreV1().Pods(ns).Get(context.Background(), name, metav1.GetOptions{})
	return err == nil
}

func pdbWarned(r *edge.DrainResult) bool {
	return slices.ContainsFunc(r.Warnings, func(w string) bool { return strings.Contains(w, "PodDisruptionBudget vision/defect-detector allows 0") })
}

func TestDrainRequiresWrites(t *testing.T) {
	c, ctx := cluster(t, nil)
	if _, err := c.DrainNode(ctx, "pune-02", false, 0, true, "x"); !errors.Is(err, edge.ErrReadOnly) {
		t.Fatalf("err = %v, want ErrReadOnly", err)
	}
}

func TestDrainDryRunReportsPDBBlockAndChangesNothing(t *testing.T) {
	client := edgetest.Fleet()
	c := drainCluster(t, client)
	r, err := c.DrainNode(context.Background(), "pune-02", false, 0, true, "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if !r.DryRun || r.Complete || len(r.Evicted) != 0 || len(r.Blocked) != 2 {
		t.Errorf("dry run result = %+v, want 2 pods blocked by the PDB", r)
	}
	if !pdbWarned(r) {
		t.Errorf("warnings = %v, want the zero-disruption PDB flagged", r.Warnings)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Name != "model-runtime-pune-02" {
		t.Errorf("skipped = %+v, want only the DaemonSet pod", r.Skipped)
	}
	if unschedulable(t, client, "pune-02") || !podExists(client, "vision", "defect-detector-b") {
		t.Error("dry run changed the cluster")
	}
}

func TestDrainTimesOutOnPDBAndReportsWhatRemains(t *testing.T) {
	client := edgetest.Fleet()
	c := drainCluster(t, client)
	start := time.Now()
	r, err := c.DrainNode(context.Background(), "pune-02", false, 150*time.Millisecond, false, "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("drain ignored its timeout")
	}
	if r.Complete || len(r.Blocked) != 2 || len(r.Evicted) != 0 || !strings.Contains(r.Blocked[0].Reason, "PodDisruptionBudget") {
		t.Errorf("result = %+v", r)
	}
	if !unschedulable(t, client, "pune-02") {
		t.Error("node should stay cordoned after an incomplete drain")
	}
	if !podExists(client, "vision", "defect-detector-b") {
		t.Error("PDB-protected pod was evicted")
	}
}

func TestDrainRetriesUntilPDBAllowsEviction(t *testing.T) {
	client := edgetest.Fleet()
	c := drainCluster(t, client)
	go func() {
		time.Sleep(80 * time.Millisecond)
		pdb, _ := client.PolicyV1().PodDisruptionBudgets("vision").Get(context.Background(), "defect-detector", metav1.GetOptions{})
		pdb.Status.DisruptionsAllowed = 5
		_, _ = client.PolicyV1().PodDisruptionBudgets("vision").Update(context.Background(), pdb, metav1.UpdateOptions{})
	}()
	r, err := c.DrainNode(context.Background(), "pune-02", false, 5*time.Second, false, "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete || len(r.Evicted) != 2 {
		t.Fatalf("result = %+v, want both pods evicted once the PDB relaxed", r)
	}
	if podExists(client, "vision", "defect-detector-b") || !podExists(client, "edge-system", "model-runtime-pune-02") {
		t.Error("expected detector pods gone and the DaemonSet pod untouched")
	}
}

func TestDrainRefusesLocalStorageUnlessAllowed(t *testing.T) {
	client := edgetest.Fleet()
	c := drainCluster(t, client)
	r, err := c.DrainNode(context.Background(), "blr-01", false, 0, false, "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete || len(r.Blocked) != 1 || !strings.Contains(r.Blocked[0].Reason, "emptyDir") || len(r.Evicted) != 0 {
		t.Errorf("result = %+v, want a refusal because of emptyDir", r)
	}
	if unschedulable(t, client, "blr-01") || !podExists(client, "retail", "shelf-scanner-a") {
		t.Error("a refused drain must change nothing, not even cordon")
	}
	if !slices.ContainsFunc(r.Warnings, func(w string) bool { return strings.Contains(w, `site "store-blr"`) }) {
		t.Errorf("warnings = %v, want a site-capacity warning (blr-02 is NotReady)", r.Warnings)
	}

	r, err = c.DrainNode(context.Background(), "blr-01", true, 0, false, "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete || !slices.Equal(r.Evicted, []string{"retail/shelf-scanner-a"}) {
		t.Errorf("with delete_local_data: %+v", r)
	}
	if !unschedulable(t, client, "blr-01") || podExists(client, "retail", "shelf-scanner-a") {
		t.Error("node should be cordoned and emptied")
	}
}

func TestDrainSkipsDaemonSetPodsAndWarnsAboutLastNodeAtSite(t *testing.T) {
	client := edgetest.Fleet()
	c := drainCluster(t, client)
	r, err := c.DrainNode(context.Background(), "del-01", false, 0, false, "decommission")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete || len(r.Evicted) != 0 || len(r.Skipped) != 1 {
		t.Errorf("result = %+v, want nothing to evict and the DaemonSet pod skipped", r)
	}
	if len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "warehouse-del") {
		t.Errorf("warnings = %v, want a warning that del-01 is the site's only node", r.Warnings)
	}
	if !unschedulable(t, client, "del-01") {
		t.Error("node should be cordoned")
	}
}

func TestDrainRefusesUnmanagedPodsAndSkipsMirrorAndFinished(t *testing.T) {
	client := edgetest.Fleet()
	mk := func(name string, phase corev1.PodPhase, mut func(*corev1.Pod)) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name}, Spec: corev1.PodSpec{NodeName: "del-01"}, Status: corev1.PodStatus{Phase: phase}}
		if mut != nil {
			mut(p)
		}
		if _, err := client.CoreV1().Pods("default").Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mk("static-web", corev1.PodRunning, func(p *corev1.Pod) { p.Annotations = map[string]string{"kubernetes.io/config.mirror": "abc"} })
	mk("job-done", corev1.PodSucceeded, nil)
	c := drainCluster(t, client)
	r, err := c.DrainNode(context.Background(), "del-01", false, 0, true, "x")
	if err != nil || !r.Complete || len(r.Skipped) != 3 {
		t.Fatalf("mirror, finished and DaemonSet pods should be skipped: %+v, %v", r, err)
	}

	mk("bare", corev1.PodRunning, nil)
	r, err = c.DrainNode(context.Background(), "del-01", false, 0, false, "x")
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete || len(r.Blocked) != 1 || r.Blocked[0].Name != "bare" || unschedulable(t, client, "del-01") || !podExists(client, "default", "bare") {
		t.Errorf("unmanaged pod should refuse the drain without changes: %+v", r)
	}
}

func TestDrainUnknownNode(t *testing.T) {
	c := drainCluster(t, edgetest.Fleet())
	if _, err := c.DrainNode(context.Background(), "nope", false, 0, true, "x"); err == nil {
		t.Error("unknown node should fail")
	}
}
