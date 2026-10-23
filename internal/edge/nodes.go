package edge

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
)

// NodeSummary is the edge-oriented view of a node.
type NodeSummary struct {
	Name           string            `json:"name"`
	Site           string            `json:"site,omitempty"`
	Ready          bool              `json:"ready"`
	Status         string            `json:"status" jsonschema:"Ready, NotReady or Unknown"`
	LastHeartbeat  string            `json:"last_heartbeat,omitempty" jsonschema:"age of the node's last Ready heartbeat, e.g. 40s"`
	StaleHeartbeat bool              `json:"stale_heartbeat,omitempty"`
	Roles          []string          `json:"roles,omitempty"`
	Unschedulable  bool              `json:"unschedulable,omitempty"`
	Pressure       []string          `json:"pressure,omitempty" jsonschema:"active pressure conditions such as DiskPressure"`
	Taints         []string          `json:"taints,omitempty"`
	KubeletVersion string            `json:"kubelet_version,omitempty"`
	OSImage        string            `json:"os_image,omitempty"`
	Arch           string            `json:"arch,omitempty"`
	CPU            string            `json:"cpu,omitempty"`
	Memory         string            `json:"memory,omitempty"`
	Accelerators   map[string]string `json:"accelerators,omitempty" jsonschema:"allocatable extended resources such as gpu.intel.com/i915 or npu devices"`
}

// Healthy reports whether a node needs no attention.
func (n NodeSummary) Healthy() bool {
	return n.Ready && !n.StaleHeartbeat && len(n.Pressure) == 0 && !n.Unschedulable
}

func (c *Cluster) summarize(n *corev1.Node) NodeSummary {
	s := NodeSummary{
		Name:           n.Name,
		Site:           n.Labels[c.cfg.SiteLabel],
		Status:         "Unknown",
		Unschedulable:  n.Spec.Unschedulable,
		KubeletVersion: n.Status.NodeInfo.KubeletVersion,
		OSImage:        n.Status.NodeInfo.OSImage,
		Arch:           n.Status.NodeInfo.Architecture,
	}
	for k := range n.Labels {
		if role, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && role != "" {
			s.Roles = append(s.Roles, role)
		}
	}
	sort.Strings(s.Roles)
	for _, cond := range n.Status.Conditions {
		switch {
		case cond.Type == corev1.NodeReady:
			s.Ready = cond.Status == corev1.ConditionTrue
			if s.Ready {
				s.Status = "Ready"
			} else if cond.Status == corev1.ConditionFalse {
				s.Status = "NotReady"
			}
			if !cond.LastHeartbeatTime.IsZero() {
				age := c.now().Sub(cond.LastHeartbeatTime.Time)
				s.LastHeartbeat = humanAge(age)
				s.StaleHeartbeat = age > c.cfg.StaleHeartbeat
			}
		case cond.Status == corev1.ConditionTrue:
			s.Pressure = append(s.Pressure, string(cond.Type))
		}
	}
	for _, t := range n.Spec.Taints {
		s.Taints = append(s.Taints, fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect))
	}
	if q, ok := n.Status.Capacity[corev1.ResourceCPU]; ok {
		s.CPU = q.String()
	}
	if q, ok := n.Status.Capacity[corev1.ResourceMemory]; ok {
		s.Memory = q.String()
	}
	s.Accelerators = accelerators(n.Status.Allocatable)
	return s
}

// accelerators returns the vendor extended resources on a node (GPUs, NPUs, FPGAs,
// QAT, ...). Device plugins advertise these as domain-prefixed resource names.
func accelerators(rl corev1.ResourceList) map[string]string {
	out := map[string]string{}
	for name, q := range rl {
		n := string(name)
		if !strings.Contains(n, "/") || strings.HasPrefix(n, "kubernetes.io/") || strings.HasPrefix(n, "hugepages-") {
			continue
		}
		if q.IsZero() {
			continue
		}
		out[n] = q.String()
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ListNodes returns every node, optionally filtered to one site or to nodes that need attention.
func (c *Cluster) ListNodes(ctx context.Context, site string, unhealthyOnly bool) ([]NodeSummary, error) {
	list, err := c.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	out := []NodeSummary{}
	for i := range list.Items {
		s := c.summarize(&list.Items[i])
		if site != "" && s.Site != site {
			continue
		}
		if unhealthyOnly && s.Healthy() {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Site != out[j].Site {
			return out[i].Site < out[j].Site
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// NodeDetail is a single node with what is running on it and what happened to it.
type NodeDetail struct {
	Node   NodeSummary  `json:"node"`
	Pods   []PodBrief   `json:"pods"`
	Events []EventBrief `json:"events"`
}

// PodBrief is a one-line view of a pod.
type PodBrief struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	Ready     string `json:"ready" jsonschema:"ready containers out of total, e.g. 1/2"`
	Restarts  int32  `json:"restarts"`
	Reason    string `json:"reason,omitempty" jsonschema:"why a container is not running, e.g. CrashLoopBackOff"`
}

// DescribeNode returns a node, its pods (within the namespace allowlist) and its recent events.
func (c *Cluster) DescribeNode(ctx context.Context, name string) (*NodeDetail, error) {
	n, err := c.client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting node %q: %w", name, err)
	}
	d := &NodeDetail{Node: c.summarize(n), Pods: []PodBrief{}, Events: []EventBrief{}}

	nss, _ := c.namespacesFor("")
	if len(nss) == 0 {
		nss = []string{metav1.NamespaceAll}
	}
	sel := fields.OneTermEqualSelector("spec.nodeName", name).String()
	for _, ns := range nss {
		pods, err := c.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{FieldSelector: sel})
		if err != nil {
			return nil, fmt.Errorf("listing pods on %q: %w", name, err)
		}
		for i := range pods.Items {
			// Re-check client-side: not every API (or fake) honours field selectors.
			if pods.Items[i].Spec.NodeName == name {
				d.Pods = append(d.Pods, briefPod(&pods.Items[i]))
			}
		}
	}
	evs, err := c.client.CoreV1().Events(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: fields.AndSelectors(
			fields.OneTermEqualSelector("involvedObject.kind", "Node"),
			fields.OneTermEqualSelector("involvedObject.name", name),
		).String(),
	})
	if err == nil {
		mine := evs.Items[:0]
		for _, e := range evs.Items {
			if e.InvolvedObject.Kind == "Node" && e.InvolvedObject.Name == name {
				mine = append(mine, e)
			}
		}
		d.Events = c.briefEvents(mine, false, 20)
	}
	return d, nil
}

func briefPod(p *corev1.Pod) PodBrief {
	b := PodBrief{Namespace: p.Namespace, Name: p.Name, Phase: string(p.Status.Phase)}
	ready := 0
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		b.Restarts += cs.RestartCount
		if cs.State.Waiting != nil && b.Reason == "" {
			b.Reason = cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && b.Reason == "" {
			b.Reason = cs.State.Terminated.Reason
		}
	}
	b.Ready = fmt.Sprintf("%d/%d", ready, len(p.Spec.Containers))
	return b
}
