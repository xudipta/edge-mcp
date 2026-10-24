package edge

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AcceleratorSource says where an accelerator number came from.
const AcceleratorSource = "requests"

// AcceleratorResource is the allocation of one accelerator resource on a node.
type AcceleratorResource struct {
	Resource    string `json:"resource" jsonschema:"extended resource name, e.g. gpu.intel.com/i915"`
	Allocatable int64  `json:"allocatable"`
	Allocated   int64  `json:"allocated" jsonschema:"sum of the requests of pods scheduled on the node; not a measure of actual use"`
	Free        int64  `json:"free"`
}

// NodeAccelerators is the accelerator allocation on one node.
type NodeAccelerators struct {
	Node      string                `json:"node"`
	Site      string                `json:"site,omitempty"`
	Ready     bool                  `json:"ready"`
	Resources []AcceleratorResource `json:"resources"`
}

// AcceleratorReport lists accelerator allocation per node.
type AcceleratorReport struct {
	Source  string             `json:"source" jsonschema:"where the numbers come from: requests means allocated = pod resource requests, not measured utilisation"`
	Partial bool               `json:"partial,omitempty" jsonschema:"true when a namespace allowlist hides pods, so allocated may be too low"`
	Nodes   []NodeAccelerators `json:"nodes"`
}

// podAccelRequests returns the effective request of each accelerator resource for a pod:
// the larger of the sum over app containers and the biggest init container. Extended
// resources must have requests equal to limits, so a limit counts if no request is set.
func podAccelRequests(p *corev1.Pod) map[string]int64 {
	val := func(c corev1.Container, name corev1.ResourceName) (resource.Quantity, bool) {
		if q, ok := c.Resources.Requests[name]; ok {
			return q, true
		}
		q, ok := c.Resources.Limits[name]
		return q, ok
	}
	names := map[corev1.ResourceName]bool{}
	for _, cs := range [][]corev1.Container{p.Spec.Containers, p.Spec.InitContainers} {
		for _, c := range cs {
			for _, rl := range []corev1.ResourceList{c.Resources.Requests, c.Resources.Limits} {
				for n := range rl {
					if accelerators(corev1.ResourceList{n: resource.MustParse("1")}) != nil {
						names[n] = true
					}
				}
			}
		}
	}
	out := map[string]int64{}
	for n := range names {
		var sum, maxInit int64
		for _, c := range p.Spec.Containers {
			if q, ok := val(c, n); ok {
				sum += q.Value()
			}
		}
		for _, c := range p.Spec.InitContainers {
			if q, ok := val(c, n); ok {
				maxInit = max(maxInit, q.Value())
			}
		}
		out[string(n)] = max(sum, maxInit)
	}
	return out
}

// AcceleratorUsage reports allocatable vs requested accelerators on nodes that have any,
// optionally limited to one site or one node.
func (c *Cluster) AcceleratorUsage(ctx context.Context, site, node string) (*AcceleratorReport, error) {
	nodes, err := c.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	rep := &AcceleratorReport{Source: AcceleratorSource, Partial: len(c.cfg.Namespaces) > 0, Nodes: []NodeAccelerators{}}

	allocated := map[string]map[string]int64{} // node -> resource -> count
	nss, _ := c.namespacesFor("")
	if len(nss) == 0 {
		nss = []string{metav1.NamespaceAll}
	}
	for _, ns := range nss {
		pods, err := c.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing pods: %w", err)
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
				continue
			}
			for r, n := range podAccelRequests(p) {
				if allocated[p.Spec.NodeName] == nil {
					allocated[p.Spec.NodeName] = map[string]int64{}
				}
				allocated[p.Spec.NodeName][r] += n
			}
		}
	}

	for i := range nodes.Items {
		n := &nodes.Items[i]
		s := c.summarize(n)
		if (site != "" && s.Site != site) || (node != "" && s.Name != node) {
			continue
		}
		na := NodeAccelerators{Node: s.Name, Site: s.Site, Ready: s.Ready, Resources: []AcceleratorResource{}}
		for name, q := range n.Status.Allocatable {
			if accelerators(corev1.ResourceList{name: q}) == nil {
				continue
			}
			alloc := allocated[s.Name][string(name)]
			na.Resources = append(na.Resources, AcceleratorResource{
				Resource: string(name), Allocatable: q.Value(), Allocated: alloc, Free: max(q.Value()-alloc, 0),
			})
		}
		if len(na.Resources) == 0 {
			continue
		}
		sort.Slice(na.Resources, func(i, j int) bool { return na.Resources[i].Resource < na.Resources[j].Resource })
		rep.Nodes = append(rep.Nodes, na)
	}
	sort.Slice(rep.Nodes, func(i, j int) bool {
		if rep.Nodes[i].Site != rep.Nodes[j].Site {
			return rep.Nodes[i].Site < rep.Nodes[j].Site
		}
		return rep.Nodes[i].Node < rep.Nodes[j].Node
	})
	return rep, nil
}
