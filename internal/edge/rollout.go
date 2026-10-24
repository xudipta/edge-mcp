package edge

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ImageCount is how many of a site's pods run one image.
type ImageCount struct {
	Image   string `json:"image"`
	Pods    int    `json:"pods"`
	Ready   int    `json:"ready"`
	Current bool   `json:"current" jsonschema:"true if this is the image in the workload's current pod template"`
}

// SiteRollout is the rollout state of a workload at one site.
type SiteRollout struct {
	Site    string       `json:"site"`
	Pods    int          `json:"pods"`
	Images  []ImageCount `json:"images"`
	Done    bool         `json:"done" jsonschema:"every pod at the site runs the current image and is Ready"`
	Stuck   bool         `json:"stuck"`
	Reasons []string     `json:"reasons,omitempty" jsonschema:"why the site is stuck"`
}

// RolloutReport shows which sites run which version of a workload.
type RolloutReport struct {
	Kind        string        `json:"kind"`
	Namespace   string        `json:"namespace"`
	Name        string        `json:"name"`
	TargetImage string        `json:"target_image" jsonschema:"image(s) in the workload's current pod template"`
	Sites       []SiteRollout `json:"sites"`
	SitesDone   int           `json:"sites_done"`
	StuckSites  []string      `json:"stuck_sites"`
	Complete    bool          `json:"complete" jsonschema:"every site is done"`
	PendingPods int           `json:"pending_pods"`
}

// podImages returns a pod's container images as one comparable string.
func podImages(containers []corev1.Container) string {
	imgs := make([]string, len(containers))
	for i, c := range containers {
		imgs[i] = c.Image
	}
	return strings.Join(imgs, ",")
}

func podReady(p *corev1.Pod) bool {
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	// Fall back to container statuses when conditions are not populated.
	if len(p.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, cs := range p.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return p.Status.Phase == corev1.PodRunning
}

// RolloutStatus groups a Deployment's or DaemonSet's pods by site and image.
func (c *Cluster) RolloutStatus(ctx context.Context, kind, namespace, name string) (*RolloutReport, error) {
	if err := c.checkNamespace(namespace); err != nil {
		return nil, err
	}
	var (
		selector *metav1.LabelSelector
		template corev1.PodTemplateSpec
	)
	switch strings.ToLower(kind) {
	case "deployment":
		kind = "Deployment"
		d, err := c.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("getting deployment %s/%s: %w", namespace, name, err)
		}
		selector, template = d.Spec.Selector, d.Spec.Template
	case "daemonset":
		kind = "DaemonSet"
		d, err := c.client.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("getting daemonset %s/%s: %w", namespace, name, err)
		}
		selector, template = d.Spec.Selector, d.Spec.Template
	default:
		return nil, fmt.Errorf("kind must be Deployment or DaemonSet, got %q", kind)
	}
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil || sel.Empty() {
		return nil, fmt.Errorf("%s %s/%s has no usable pod selector", kind, namespace, name)
	}
	pods, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, fmt.Errorf("listing pods of %s/%s: %w", namespace, name, err)
	}
	nodes, err := c.ListNodes(ctx, "", false)
	if err != nil {
		return nil, err
	}
	nodeByName := map[string]NodeSummary{}
	for _, n := range nodes {
		nodeByName[n.Name] = n
	}

	target := podImages(template.Spec.Containers)
	rep := &RolloutReport{Kind: kind, Namespace: namespace, Name: name, TargetImage: target, Sites: []SiteRollout{}, StuckSites: []string{}}

	type acc struct {
		images  map[string]*ImageCount
		pods    int
		current int // pods on the target image
		okPods  int // pods on the target image that are Ready
		reasons []string
	}
	bySite := map[string]*acc{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil {
			continue
		}
		site := "(unscheduled)"
		var node NodeSummary
		onNode := false
		if p.Spec.NodeName != "" {
			node, onNode = nodeByName[p.Spec.NodeName]
			site = "(unlabelled)"
			if onNode && node.Site != "" {
				site = node.Site
			}
		}
		a := bySite[site]
		if a == nil {
			a = &acc{images: map[string]*ImageCount{}}
			bySite[site] = a
		}
		a.pods++
		img := podImages(p.Spec.Containers)
		ic := a.images[img]
		if ic == nil {
			ic = &ImageCount{Image: img, Current: img == target}
			a.images[img] = ic
		}
		ic.Pods++
		ready := podReady(p)
		if ready {
			ic.Ready++
		}
		isCurrent := img == target
		if isCurrent {
			a.current++
			if ready {
				a.okPods++
			}
		}
		if p.Status.Phase == corev1.PodPending {
			rep.PendingPods++
			a.reasons = append(a.reasons, fmt.Sprintf("pod %s is Pending", p.Name))
		}
		if onNode && !node.Ready && (!isCurrent || !ready) {
			a.reasons = append(a.reasons, fmt.Sprintf("node %s is NotReady", node.Name))
		}
	}

	for site, a := range bySite {
		s := SiteRollout{Site: site, Pods: a.pods, Images: []ImageCount{}, Done: a.okPods == a.pods}
		for _, ic := range a.images {
			s.Images = append(s.Images, *ic)
		}
		sort.Slice(s.Images, func(i, j int) bool {
			if s.Images[i].Current != s.Images[j].Current {
				return s.Images[i].Current
			}
			return s.Images[i].Image < s.Images[j].Image
		})
		reasons := a.reasons
		if a.current == 0 && a.pods > 0 {
			reasons = append([]string{"no pod runs the current image yet"}, reasons...)
		}
		s.Reasons = dedupe(reasons)
		s.Stuck = len(s.Reasons) > 0
		rep.Sites = append(rep.Sites, s)
	}
	sort.Slice(rep.Sites, func(i, j int) bool { return rep.Sites[i].Site < rep.Sites[j].Site })
	for _, s := range rep.Sites {
		if s.Done {
			rep.SitesDone++
		}
		if s.Stuck {
			rep.StuckSites = append(rep.StuckSites, s.Site)
		}
	}
	rep.Complete = len(rep.Sites) > 0 && rep.SitesDone == len(rep.Sites)
	return rep, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
