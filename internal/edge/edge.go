// Package edge reads and (optionally) operates on Kubernetes clusters that run
// at the edge: many small nodes spread across sites, often with accelerators,
// flaky links and nobody on site.
package edge

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
)

// Config controls what the server is allowed to see and do.
type Config struct {
	// SiteLabel is the node label that names the physical site of a node.
	SiteLabel string
	// Namespaces, when non-empty, limits every namespaced read and write to these namespaces.
	Namespaces []string
	// AllowWrites enables the cordon/uncordon/restart/scale tools.
	AllowWrites bool
	// MaxReplicas caps scale_deployment.
	MaxReplicas int32
	// StaleHeartbeat is how old a node's last heartbeat may be before it is flagged.
	StaleHeartbeat time.Duration
}

// DefaultConfig returns safe defaults: read-only, every namespace, the standard zone label.
func DefaultConfig() Config {
	return Config{
		SiteLabel:      "topology.kubernetes.io/zone",
		MaxReplicas:    20,
		StaleHeartbeat: 2 * time.Minute,
	}
}

// Cluster wraps a Kubernetes client with the edge-specific views and guard rails.
type Cluster struct {
	client kubernetes.Interface
	cfg    Config
	now    func() time.Time
	logs   LogSource
}

// LogSource replaces the Kubernetes log endpoint, for demos whose fake clientset
// can only return a placeholder. It returns the full log; PodLogs applies tail_lines.
type LogSource func(namespace, pod, container string, previous bool) (string, error)

// New returns a Cluster backed by client.
func New(client kubernetes.Interface, cfg Config) *Cluster {
	return &Cluster{client: client, cfg: cfg, now: time.Now}
}

// WithClock replaces the clock used for ages and heartbeat staleness (for tests and demos).
func (c *Cluster) WithClock(now func() time.Time) *Cluster {
	c.now = now
	return c
}

// WithLogSource makes PodLogs read from src instead of the Kubernetes API (for demos).
func (c *Cluster) WithLogSource(src LogSource) *Cluster {
	c.logs = src
	return c
}

// Config returns the active configuration.
func (c *Cluster) Config() Config { return c.cfg }

// checkNamespace rejects namespaces outside the allowlist.
func (c *Cluster) checkNamespace(ns string) error {
	if ns == "" {
		return fmt.Errorf("namespace is required")
	}
	if len(c.cfg.Namespaces) > 0 && !slices.Contains(c.cfg.Namespaces, ns) {
		return fmt.Errorf("namespace %q is not in the allowlist (%s)", ns, strings.Join(c.cfg.Namespaces, ", "))
	}
	return nil
}

// namespacesFor expands an optional namespace argument into the namespaces to query.
// An empty result with a nil error means "all namespaces".
func (c *Cluster) namespacesFor(ns string) ([]string, error) {
	if ns != "" {
		if err := c.checkNamespace(ns); err != nil {
			return nil, err
		}
		return []string{ns}, nil
	}
	return c.cfg.Namespaces, nil
}

// humanAge renders a duration the way kubectl does: 45s, 3m, 2h, 4d.
func humanAge(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
