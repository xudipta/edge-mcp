package edge

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Workload summarizes a Deployment, DaemonSet or StatefulSet.
type Workload struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Desired   int32  `json:"desired"`
	Ready     int32  `json:"ready"`
	Available int32  `json:"available"`
	Healthy   bool   `json:"healthy"`
}

// ListWorkloads returns workloads in one namespace (or every allowed namespace).
func (c *Cluster) ListWorkloads(ctx context.Context, namespace string, unhealthyOnly bool) ([]Workload, error) {
	nss, err := c.namespacesFor(namespace)
	if err != nil {
		return nil, err
	}
	if len(nss) == 0 {
		nss = []string{metav1.NamespaceAll}
	}
	out := []Workload{}
	add := func(w Workload) {
		w.Healthy = w.Ready >= w.Desired && w.Available >= w.Desired
		if !unhealthyOnly || !w.Healthy {
			out = append(out, w)
		}
	}
	for _, ns := range nss {
		deps, err := c.client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing deployments: %w", err)
		}
		for _, d := range deps.Items {
			desired := int32(1)
			if d.Spec.Replicas != nil {
				desired = *d.Spec.Replicas
			}
			add(Workload{"Deployment", d.Namespace, d.Name, desired, d.Status.ReadyReplicas, d.Status.AvailableReplicas, false})
		}
		dss, err := c.client.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing daemonsets: %w", err)
		}
		for _, d := range dss.Items {
			add(Workload{"DaemonSet", d.Namespace, d.Name, d.Status.DesiredNumberScheduled, d.Status.NumberReady, d.Status.NumberAvailable, false})
		}
		sss, err := c.client.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing statefulsets: %w", err)
		}
		for _, s := range sss.Items {
			desired := int32(1)
			if s.Spec.Replicas != nil {
				desired = *s.Spec.Replicas
			}
			add(Workload{"StatefulSet", s.Namespace, s.Name, desired, s.Status.ReadyReplicas, s.Status.AvailableReplicas, false})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Kind+out[i].Name < out[j].Kind+out[j].Name
	})
	return out, nil
}

// EventBrief is a compact cluster event.
type EventBrief struct {
	Age     string `json:"age"`
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Object  string `json:"object"`
	Message string `json:"message"`
	Count   int32  `json:"count,omitempty"`
}

// ListEvents returns recent events, newest first.
func (c *Cluster) ListEvents(ctx context.Context, namespace string, warningsOnly bool, limit int) ([]EventBrief, error) {
	nss, err := c.namespacesFor(namespace)
	if err != nil {
		return nil, err
	}
	if len(nss) == 0 {
		nss = []string{metav1.NamespaceAll}
	}
	var all []corev1.Event
	for _, ns := range nss {
		evs, err := c.client.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("listing events: %w", err)
		}
		all = append(all, evs.Items...)
	}
	return c.briefEvents(all, warningsOnly, limit), nil
}

func eventTime(e corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	default:
		return e.CreationTimestamp.Time
	}
}

func (c *Cluster) briefEvents(evs []corev1.Event, warningsOnly bool, limit int) []EventBrief {
	sort.Slice(evs, func(i, j int) bool { return eventTime(evs[i]).After(eventTime(evs[j])) })
	out := []EventBrief{}
	for _, e := range evs {
		if warningsOnly && e.Type != corev1.EventTypeWarning {
			continue
		}
		obj := e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name
		if e.InvolvedObject.Namespace != "" {
			obj = e.InvolvedObject.Namespace + "/" + obj
		}
		out = append(out, EventBrief{
			Age: humanAge(c.now().Sub(eventTime(e))), Type: e.Type, Reason: e.Reason,
			Object: obj, Message: e.Message, Count: e.Count,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// MaxLogLines caps how much of a log a single call can return.
const MaxLogLines = 500

// PodLogs returns the tail of a container's log.
func (c *Cluster) PodLogs(ctx context.Context, namespace, pod, container string, tail int64, previous bool) (string, error) {
	if err := c.checkNamespace(namespace); err != nil {
		return "", err
	}
	if tail <= 0 {
		tail = 100
	}
	tail = min(tail, MaxLogLines)
	req := c.client.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container: container, TailLines: &tail, Previous: previous,
	})
	rc, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("reading logs of %s/%s: %w", namespace, pod, err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	// Hard cap on bytes too, so one chatty line can't flood the model's context.
	if _, err := io.Copy(&buf, io.LimitReader(rc, 256*1024)); err != nil {
		return "", fmt.Errorf("reading logs of %s/%s: %w", namespace, pod, err)
	}
	return buf.String(), nil
}

// Overview is a one-glance health summary of the whole fleet.
type Overview struct {
	Nodes                 int                   `json:"nodes"`
	NodesReady            int                   `json:"nodes_ready"`
	NodesAttention        []string              `json:"nodes_needing_attention"`
	Sites                 map[string]SiteHealth `json:"sites"`
	Accelerators          map[string]int64      `json:"accelerators" jsonschema:"total allocatable accelerators across the fleet, by resource name"`
	AcceleratorsAllocated map[string]int64      `json:"accelerators_allocated" jsonschema:"accelerators requested by pods across the fleet, by resource name (requests, not measured use)"`
	PodsRunning           int                   `json:"pods_running"`
	PodsPending           int                   `json:"pods_pending"`
	PodsFailing           int                   `json:"pods_failing" jsonschema:"pods failed or stuck in CrashLoopBackOff/ImagePullBackOff"`
	RecentWarnings        []EventBrief          `json:"recent_warnings"`
}

// SiteHealth counts nodes at one site.
type SiteHealth struct {
	Nodes int `json:"nodes"`
	Ready int `json:"ready"`
}

// Overview summarizes nodes, sites, accelerators, pods and recent warnings.
func (c *Cluster) Overview(ctx context.Context) (*Overview, error) {
	nodes, err := c.ListNodes(ctx, "", false)
	if err != nil {
		return nil, err
	}
	o := &Overview{Sites: map[string]SiteHealth{}, Accelerators: map[string]int64{}, NodesAttention: []string{}}
	for _, n := range nodes {
		o.Nodes++
		site := n.Site
		if site == "" {
			site = "(unlabelled)"
		}
		sh := o.Sites[site]
		sh.Nodes++
		if n.Ready {
			o.NodesReady++
			sh.Ready++
		}
		o.Sites[site] = sh
		if !n.Healthy() {
			o.NodesAttention = append(o.NodesAttention, n.Name)
		}
	}
	list, err := c.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, n := range list.Items {
			for name, q := range n.Status.Allocatable {
				if accelerators(corev1.ResourceList{name: q}) != nil {
					o.Accelerators[string(name)] += q.Value()
				}
			}
		}
	}

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
			b := briefPod(&pods.Items[i])
			switch {
			case b.Phase == string(corev1.PodFailed) || b.Reason == "CrashLoopBackOff" || b.Reason == "ImagePullBackOff" || b.Reason == "ErrImagePull":
				o.PodsFailing++
			case b.Phase == string(corev1.PodPending):
				o.PodsPending++
			case b.Phase == string(corev1.PodRunning):
				o.PodsRunning++
			}
		}
	}
	o.AcceleratorsAllocated = map[string]int64{}
	if usage, err := c.AcceleratorUsage(ctx, "", ""); err == nil {
		for _, n := range usage.Nodes {
			for _, r := range n.Resources {
				o.AcceleratorsAllocated[r.Resource] += r.Allocated
			}
		}
	}
	o.RecentWarnings, _ = c.ListEvents(ctx, "", true, 10)
	return o, nil
}
