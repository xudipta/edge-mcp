package edge

import (
	"context"
	"fmt"
)

// SiteSnapshot is every node at one site with the pods and events on each.
type SiteSnapshot struct {
	Site  string       `json:"site"`
	Nodes []NodeDetail `json:"nodes"`
}

// SiteSnapshot returns a snapshot of one site, or an error if no node carries that site label value.
func (c *Cluster) SiteSnapshot(ctx context.Context, site string) (*SiteSnapshot, error) {
	nodes, err := c.ListNodes(ctx, site, false)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no nodes found at site %q", site)
	}
	snap := &SiteSnapshot{Site: site}
	for _, n := range nodes {
		d, err := c.DescribeNode(ctx, n.Name)
		if err != nil {
			return nil, err
		}
		snap.Nodes = append(snap.Nodes, *d)
	}
	return snap, nil
}
