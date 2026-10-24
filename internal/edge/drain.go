package edge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	// DefaultDrainTimeout bounds how long a drain keeps retrying PDB-blocked evictions.
	DefaultDrainTimeout = 2 * time.Minute
	// MaxDrainTimeout is the longest per-node timeout a caller may ask for.
	MaxDrainTimeout = 10 * time.Minute

	mirrorPodAnnotation = "kubernetes.io/config.mirror"
)

// DrainPod is a pod and why it was skipped or blocked.
type DrainPod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
}

// DrainResult reports what drain_node did, or would do in a dry run.
type DrainResult struct {
	DryRun   bool       `json:"dry_run"`
	Target   string     `json:"target"`
	Message  string     `json:"message"`
	Complete bool       `json:"complete" jsonschema:"every pod that had to move was (or, in a dry run, would be) evicted"`
	Evicted  []string   `json:"evicted" jsonschema:"namespace/pod of pods evicted (dry run: that would be evicted)"`
	Skipped  []DrainPod `json:"skipped,omitempty" jsonschema:"pods deliberately left in place: DaemonSet pods, mirror pods, finished pods"`
	Blocked  []DrainPod `json:"blocked,omitempty" jsonschema:"pods that could not be evicted and why"`
	Warnings []string   `json:"warnings,omitempty"`
}

// WithDrainPoll sets how often PDB-blocked evictions are retried (for tests).
func (c *Cluster) WithDrainPoll(d time.Duration) *Cluster {
	c.drainPoll = d
	return c
}

func controllerKind(p *corev1.Pod) string {
	for _, o := range p.OwnerReferences {
		if o.Controller != nil && *o.Controller {
			return o.Kind
		}
	}
	return ""
}

func hasEmptyDir(p *corev1.Pod) bool {
	for _, v := range p.Spec.Volumes {
		if v.EmptyDir != nil {
			return true
		}
	}
	return false
}

// siteWarning warns when draining node would leave its site without a Ready, schedulable node.
func (c *Cluster) siteWarning(ctx context.Context, node string) string {
	nodes, err := c.ListNodes(ctx, "", false)
	if err != nil {
		return ""
	}
	var site string
	for _, n := range nodes {
		if n.Name == node {
			site = n.Site
		}
	}
	if site == "" {
		return ""
	}
	for _, n := range nodes {
		if n.Site == site && n.Name != node && n.Ready && !n.Unschedulable {
			return ""
		}
	}
	return fmt.Sprintf("draining %s leaves site %q with no other Ready, schedulable node: workloads evicted from it have nowhere to go at that site", node, site)
}

// pdbWarnings flags PodDisruptionBudgets that will block, or be exceeded by, the planned evictions.
func (c *Cluster) pdbWarnings(ctx context.Context, plan []*corev1.Pod) []string {
	byNS := map[string][]*corev1.Pod{}
	for _, p := range plan {
		byNS[p.Namespace] = append(byNS[p.Namespace], p)
	}
	var out []string
	for ns, pods := range byNS {
		pdbs, err := c.client.PolicyV1().PodDisruptionBudgets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			out = append(out, fmt.Sprintf("could not read PodDisruptionBudgets in %s: %v", ns, err))
			continue
		}
		for _, pdb := range pdbs.Items {
			sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
			if err != nil || sel.Empty() {
				continue
			}
			n := 0
			for _, p := range pods {
				if sel.Matches(labels.Set(p.Labels)) {
					n++
				}
			}
			switch {
			case n == 0:
			case pdb.Status.DisruptionsAllowed <= 0:
				out = append(out, fmt.Sprintf("PodDisruptionBudget %s/%s allows 0 disruptions: %d of its pod(s) on this node cannot be evicted until it recovers", ns, pdb.Name, n))
			case int32(n) > pdb.Status.DisruptionsAllowed: // #nosec G115 -- small pod count
				out = append(out, fmt.Sprintf("PodDisruptionBudget %s/%s allows %d disruption(s) but %d of its pods are on this node: some evictions will wait for replacements", ns, pdb.Name, pdb.Status.DisruptionsAllowed, n))
			}
		}
	}
	sort.Strings(out)
	return out
}

func (c *Cluster) evict(ctx context.Context, p *corev1.Pod, dryRun bool) error {
	ev := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace}}
	if dryRun {
		ev.DeleteOptions = &metav1.DeleteOptions{DryRun: []string{metav1.DryRunAll}}
	}
	return c.client.CoreV1().Pods(p.Namespace).EvictV1(ctx, ev)
}

// DrainNode cordons a node and evicts its pods through the Eviction API, so
// PodDisruptionBudgets are honoured. DaemonSet and mirror pods are left alone.
// Pods that use emptyDir storage are refused unless deleteLocalData is set, and
// pods without a controller are always refused because evicting them deletes them
// for good. Nothing is changed if any pod is refused up front.
func (c *Cluster) DrainNode(ctx context.Context, node string, deleteLocalData bool, timeout time.Duration, dryRun bool, reason string) (*DrainResult, error) {
	if !c.cfg.AllowWrites {
		return nil, ErrReadOnly
	}
	if timeout <= 0 {
		timeout = DefaultDrainTimeout
	}
	timeout = min(timeout, MaxDrainTimeout)
	target := "node/" + node
	n, err := c.client.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting node %q: %w", node, err)
	}

	res := &DrainResult{DryRun: dryRun, Target: target, Evicted: []string{}}
	if w := c.siteWarning(ctx, n.Name); w != "" {
		res.Warnings = append(res.Warnings, w)
	}
	nss, _ := c.namespacesFor("")
	if len(nss) == 0 {
		nss = []string{metav1.NamespaceAll}
	} else {
		res.Warnings = append(res.Warnings, "namespace allowlist is active: pods in other namespaces are neither listed nor evicted, so the node may not end up empty")
	}

	var plan []*corev1.Pod
	for _, ns := range nss {
		pods, err := c.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node})
		if err != nil {
			return nil, fmt.Errorf("listing pods on %q: %w", node, err)
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Spec.NodeName != node { // not every API (or fake) honours field selectors
				continue
			}
			ref := func(why string) DrainPod { return DrainPod{Namespace: p.Namespace, Name: p.Name, Reason: why} }
			_, mirror := p.Annotations[mirrorPodAnnotation]
			switch {
			case p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed:
				res.Skipped = append(res.Skipped, ref("already finished"))
			case mirror:
				res.Skipped = append(res.Skipped, ref("static (mirror) pod, managed by the kubelet"))
			case controllerKind(p) == "DaemonSet":
				res.Skipped = append(res.Skipped, ref("DaemonSet pod, would be recreated on this node"))
			case controllerKind(p) == "":
				res.Blocked = append(res.Blocked, ref("not managed by a controller: evicting would delete it permanently"))
			case hasEmptyDir(p) && !deleteLocalData:
				res.Blocked = append(res.Blocked, ref("uses emptyDir local storage that would be lost; set delete_local_data=true to allow"))
			default:
				plan = append(plan, p)
			}
		}
	}
	res.Warnings = append(res.Warnings, c.pdbWarnings(ctx, plan)...)

	if len(res.Blocked) > 0 {
		res.Message = fmt.Sprintf("drain refused: %d pod(s) cannot be evicted safely; nothing was changed", len(res.Blocked))
		return res, nil
	}

	if _, err := c.SetCordon(ctx, node, true, dryRun, reason); err != nil {
		return nil, err
	}

	if dryRun {
		// Server-side dry-run evictions surface PDB blocks without removing anything.
		for _, p := range plan {
			switch err := c.evict(ctx, p, true); {
			case err == nil, apierrors.IsNotFound(err):
				res.Evicted = append(res.Evicted, p.Namespace+"/"+p.Name)
			case apierrors.IsTooManyRequests(err):
				res.Blocked = append(res.Blocked, DrainPod{p.Namespace, p.Name, "blocked by a PodDisruptionBudget right now"})
			default:
				res.Blocked = append(res.Blocked, DrainPod{p.Namespace, p.Name, err.Error()})
			}
		}
		res.Complete = len(res.Blocked) == 0
		c.audit("drain", target, true, reason)
		res.Message = fmt.Sprintf("dry run: would cordon %s and evict %d pod(s); %d would be blocked, %d left in place (set dry_run=false to apply)",
			node, len(res.Evicted), len(res.Blocked), len(res.Skipped))
		return res, nil
	}

	poll := c.drainPoll
	if poll <= 0 {
		poll = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	pending := plan
	for len(pending) > 0 {
		var retry []*corev1.Pod
		for _, p := range pending {
			switch err := c.evict(ctx, p, false); {
			case err == nil, apierrors.IsNotFound(err):
				res.Evicted = append(res.Evicted, p.Namespace+"/"+p.Name)
			case apierrors.IsTooManyRequests(err):
				retry = append(retry, p)
			default:
				res.Blocked = append(res.Blocked, DrainPod{p.Namespace, p.Name, err.Error()})
			}
		}
		pending = retry
		if len(pending) == 0 {
			break
		}
		if !time.Now().Add(poll).Before(deadline) {
			for _, p := range pending {
				res.Blocked = append(res.Blocked, DrainPod{p.Namespace, p.Name,
					fmt.Sprintf("blocked by a PodDisruptionBudget; gave up after %s", timeout)})
			}
			break
		}
		select {
		case <-ctx.Done():
			return res, errors.Join(ctx.Err(), fmt.Errorf("drain interrupted after evicting %d pod(s)", len(res.Evicted)))
		case <-time.After(poll):
		}
	}
	res.Complete = len(res.Blocked) == 0
	c.audit("drain", target, false, reason)
	if res.Complete {
		res.Message = fmt.Sprintf("drained %s: cordoned and evicted %d pod(s); %d left in place", node, len(res.Evicted), len(res.Skipped))
	} else {
		res.Message = fmt.Sprintf("drain of %s incomplete: node is cordoned, %d pod(s) evicted, %d could not be evicted", node, len(res.Evicted), len(res.Blocked))
	}
	return res, nil
}
