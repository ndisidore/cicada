// Package visualize builds an intermediate graph representation from a
// validated pipeline and renders it via a terminal renderer (Unicode
// box-drawing characters), D2, or Graphviz DOT.
package visualize

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// EdgeKind distinguishes dependency edges from artifact transfer edges.
type EdgeKind int

// EdgeKind values.
const (
	DependsEdge  EdgeKind = iota
	ArtifactEdge EdgeKind = iota
)

// Edge is a directed connection between two jobs.
type Edge struct {
	From  string
	To    string
	Label string // artifact path annotation; empty for DependsEdge
	Kind  EdgeKind
}

// StepInfo is a display-oriented summary of a pipeline step.
type StepInfo struct {
	Name         string
	When         string
	HasSecrets   bool
	AllowFailure bool
}

// Node is a display-oriented summary of a pipeline job.
type Node struct {
	ID         string // job name (unique)
	Steps      []StepInfo
	When       string   // When.Expression, empty if nil
	Matrix     []string // e.g. ["go=1.23,1.24", "platform=linux/amd64,linux/arm64"]
	Image      string
	HasSecrets bool   // true if job or any step has a SecretRef
	Publish    string // non-empty if job.Publish != nil
}

// Graph is the intermediate representation consumed by all renderers.
type Graph struct {
	Title  string
	Nodes  []Node // one per job; index matches Jobs slice order
	Edges  []Edge
	Layers [][]int // node indices per layer, layer[0] = roots
}

// _nonIDChar matches characters that are not safe identifier characters.
var _nonIDChar = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// SanitizeID replaces non-identifier characters with underscores and resolves
// collisions by appending _N suffixes. When seen is nil it is initialized
// internally, so collisions are tracked within that call but not across calls.
func SanitizeID(s string, seen map[string]int) string {
	if seen == nil {
		seen = make(map[string]int)
	}
	base := _nonIDChar.ReplaceAllString(s, "_")
	if _, ok := seen[base]; !ok {
		seen[base] = 0
		return base
	}
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s_%d", base, i)
		if _, ok := seen[candidate]; !ok {
			seen[candidate] = 0
			return candidate
		}
	}
}

// BuildGraph converts a validated pipeline (TopoOrder must be set) to a Graph.
func BuildGraph(p pm.Pipeline) *Graph {
	nameToIdx := make(map[string]int, len(p.Jobs))
	for i, j := range p.Jobs {
		nameToIdx[j.Name] = i
	}
	return &Graph{
		Title:  p.Name,
		Nodes:  buildNodes(p.Jobs),
		Edges:  buildEdges(p.Jobs),
		Layers: buildLayers(p.Jobs, p.TopoOrder, nameToIdx),
	}
}

// buildLayers computes the display layer for each job (longest path from root)
// and groups job indices by layer number. topoOrder must be non-empty; if it
// is empty while jobs are present, all jobs are placed in layer 0 as a fallback.
func buildLayers(jobs []pm.Job, topoOrder []int, nameToIdx map[string]int) [][]int {
	if len(jobs) == 0 {
		return nil
	}
	if len(topoOrder) == 0 {
		all := make([]int, len(jobs))
		for i := range len(jobs) {
			all[i] = i
		}
		return [][]int{all}
	}
	depths := make([]int, len(jobs))
	for _, idx := range topoOrder {
		for _, dep := range jobs[idx].DependsOn {
			if di, ok := nameToIdx[dep]; ok {
				depths[idx] = max(depths[idx], depths[di]+1)
			}
		}
	}
	maxDepth := slices.Max(depths)
	grouped := make([][]int, maxDepth+1)
	for i, d := range depths {
		grouped[d] = append(grouped[d], i)
	}
	return grouped
}

// buildNodes converts a job slice into display-oriented Node values.
func buildNodes(jobs []pm.Job) []Node {
	nodes := make([]Node, len(jobs))
	for i, j := range jobs {
		nodes[i] = buildNode(j)
	}
	return nodes
}

// buildNode converts a single job to a Node.
func buildNode(j pm.Job) Node {
	node := Node{
		ID:         j.Name,
		Image:      j.Image,
		HasSecrets: len(j.Secrets) > 0,
	}
	if j.When != nil {
		node.When = j.When.Expression
	}
	if j.Publish != nil {
		node.Publish = j.Publish.Image
	}
	if j.Matrix != nil {
		node.Matrix = make([]string, len(j.Matrix.Dimensions))
		for di, dim := range j.Matrix.Dimensions {
			node.Matrix[di] = dim.Name + "=" + strings.Join(dim.Values, ",")
		}
	}
	node.Steps = make([]StepInfo, len(j.Steps))
	for si, s := range j.Steps {
		info := StepInfo{
			Name:         s.Name,
			AllowFailure: s.AllowFailure,
			HasSecrets:   len(s.Secrets) > 0,
		}
		if s.When != nil {
			info.When = s.When.Expression
		}
		if info.HasSecrets {
			node.HasSecrets = true
		}
		node.Steps[si] = info
	}
	return node
}

// buildEdges extracts DependsEdge and ArtifactEdge entries from all jobs.
func buildEdges(jobs []pm.Job) []Edge {
	var edges []Edge
	for _, j := range jobs {
		for _, dep := range j.DependsOn {
			edges = append(edges, Edge{From: dep, To: j.Name, Kind: DependsEdge})
		}
		for _, a := range j.Artifacts {
			edges = append(edges, Edge{
				From:  a.From,
				To:    j.Name,
				Label: a.Source + " -> " + a.Target,
				Kind:  ArtifactEdge,
			})
		}
	}
	return edges
}

// nodeIndex returns a map of node ID to index in g.Nodes for O(1) lookup.
func (g *Graph) nodeIndex() map[string]int {
	m := make(map[string]int, len(g.Nodes))
	for i, n := range g.Nodes {
		m[n.ID] = i
	}
	return m
}
