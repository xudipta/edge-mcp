package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ErrReadOnly is returned by every action when the server runs without --allow-writes.
var ErrReadOnly = errors.New("edge-mcp is running read-only; restart it with --allow-writes to enable actions")

// ActionResult reports what an action did, or would do in a dry run.
type ActionResult struct {
	DryRun  bool   `json:"dry_run"`
	Target  string `json:"target"`
	Message string `json:"message"`
}

func (c *Cluster) dryRunOpt(dryRun bool) []string {
	if dryRun {
		return []string{metav1.DryRunAll}
	}
	return nil
}

func (c *Cluster) audit(action, target string, dryRun bool, reason string) {
	slog.Info("edge-mcp action", "action", action, "target", target, "dry_run", dryRun, "reason", reason)
}

func (c *Cluster) patch(dryRun bool) metav1.PatchOptions {
	return metav1.PatchOptions{DryRun: c.dryRunOpt(dryRun), FieldManager: "edge-mcp"}
}

// SetCordon marks a node unschedulable (cordon) or schedulable again (uncordon).
func (c *Cluster) SetCordon(ctx context.Context, node string, cordon, dryRun bool, reason string) (*ActionResult, error) {
	if !c.cfg.AllowWrites {
		return nil, ErrReadOnly
	}
	body, _ := json.Marshal(map[string]any{"spec": map[string]any{"unschedulable": cordon}})
	if _, err := c.client.CoreV1().Nodes().Patch(ctx, node, types.StrategicMergePatchType, body, c.patch(dryRun)); err != nil {
		return nil, fmt.Errorf("patching node %q: %w", node, err)
	}
	verb := map[bool]string{true: "cordoned", false: "uncordoned"}[cordon]
	c.audit(verb, "node/"+node, dryRun, reason)
	return &ActionResult{DryRun: dryRun, Target: "node/" + node, Message: wouldOrDid(dryRun, verb)}, nil
}

// RestartWorkload performs a rolling restart, like `kubectl rollout restart`.
func (c *Cluster) RestartWorkload(ctx context.Context, kind, namespace, name string, dryRun bool, reason string) (*ActionResult, error) {
	if !c.cfg.AllowWrites {
		return nil, ErrReadOnly
	}
	if err := c.checkNamespace(namespace); err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{"kubectl.kubernetes.io/restartedAt": c.now().UTC().Format(time.RFC3339)},
	}}}})
	apps := c.client.AppsV1()
	var err error
	switch kind {
	case "Deployment":
		_, err = apps.Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, c.patch(dryRun))
	case "DaemonSet":
		_, err = apps.DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, c.patch(dryRun))
	case "StatefulSet":
		_, err = apps.StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, c.patch(dryRun))
	default:
		return nil, fmt.Errorf("kind must be Deployment, DaemonSet or StatefulSet, not %q", kind)
	}
	target := fmt.Sprintf("%s %s/%s", kind, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("restarting %s: %w", target, err)
	}
	c.audit("restart", target, dryRun, reason)
	return &ActionResult{DryRun: dryRun, Target: target, Message: wouldOrDid(dryRun, "rolling restart started")}, nil
}

// ScaleDeployment sets a deployment's replica count within [0, MaxReplicas].
func (c *Cluster) ScaleDeployment(ctx context.Context, namespace, name string, replicas int32, dryRun bool, reason string) (*ActionResult, error) {
	if !c.cfg.AllowWrites {
		return nil, ErrReadOnly
	}
	if err := c.checkNamespace(namespace); err != nil {
		return nil, err
	}
	if replicas < 0 || replicas > c.cfg.MaxReplicas {
		return nil, fmt.Errorf("replicas must be between 0 and %d", c.cfg.MaxReplicas)
	}
	body, _ := json.Marshal(map[string]any{"spec": map[string]any{"replicas": replicas}})
	target := fmt.Sprintf("Deployment %s/%s", namespace, name)
	if _, err := c.client.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, c.patch(dryRun)); err != nil {
		return nil, fmt.Errorf("scaling %s: %w", target, err)
	}
	c.audit("scale", target, dryRun, reason)
	return &ActionResult{DryRun: dryRun, Target: target, Message: wouldOrDid(dryRun, fmt.Sprintf("scaled to %d replicas", replicas))}, nil
}

func wouldOrDid(dryRun bool, what string) string {
	if dryRun {
		return "dry run OK: would be " + what + " (set dry_run=false to apply)"
	}
	return what
}
