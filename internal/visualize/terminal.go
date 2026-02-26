package visualize

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

var (
	_styleJobName   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")) // bright white
	_styleBullet    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))             // green
	_styleAllowFail = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))            // bright yellow
	_styleWhenLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))             // cyan
	_styleWhenExpr  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))             // dim
	_styleMatrix    = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))             // magenta
	_styleSecrets   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))            // bright yellow
	_stylePublish   = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))             // blue
	_styleConnector = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))             // dim
	_styleBorder    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))             // dim
)

const _minBoxWidth = 22

// renderedBox holds fixed-width lines for a single job node.
type renderedBox struct {
	lines []string
	width int // visual width (rune count), not byte length
}

// connEdge is an edge resolved to layer-local positions for connector rendering.
type connEdge struct {
	fromNodeIdx int // index in g.Nodes
	toNodeIdx   int // index in g.Nodes
	kind        EdgeKind
	label       string
}

// RenderTerminal writes a Unicode box-drawing diagram of g to w.
func RenderTerminal(g *Graph, w io.Writer) error {
	bw := &termWriter{w: w}

	// Header.
	title := "Pipeline"
	if g.Title != "" {
		title = "Pipeline: " + g.Title
	}
	bw.writeln(title)
	bw.writeln(strings.Repeat("─", utf8.RuneCountInString(title)))

	// Build all boxes up front so we can compute layer offsets.
	allBoxes := make([]renderedBox, len(g.Nodes))
	for i, n := range g.Nodes {
		allBoxes[i] = buildBox(n)
	}

	nodeIdx := g.nodeIndex()
	nodeToLayer := buildNodeToLayer(g)

	for i, layer := range g.Layers {
		renderLayer(bw, layer, allBoxes)
		if i+1 < len(g.Layers) {
			renderConnectors(connectorOpts{bw, g, i, allBoxes, nodeIdx, nodeToLayer})
		}
	}
	if bw.err != nil {
		return fmt.Errorf("rendering to terminal: %w", bw.err)
	}
	return nil
}

// renderLayer prints all boxes in the layer side-by-side.
func renderLayer(bw *termWriter, indices []int, boxes []renderedBox) {
	if len(indices) == 0 {
		return
	}
	maxH := 0
	for _, idx := range indices {
		maxH = max(maxH, len(boxes[idx].lines))
	}
	for row := 0; row < maxH; row++ {
		var sb strings.Builder
		for bi, idx := range indices {
			if bi > 0 {
				_, _ = sb.WriteString("  ")
			}
			box := &boxes[idx]
			if row < len(box.lines) {
				_, _ = sb.WriteString(box.lines[row])
			} else {
				_, _ = sb.WriteString(strings.Repeat(" ", box.width))
			}
		}
		bw.writeln(sb.String())
	}
}

// gatherConns collects edges that cross from fromLayer to toLayer.
func gatherConns(g *Graph, fromPos, toPos map[int]int, nodeIdx map[string]int) []connEdge {
	var conns []connEdge
	for _, e := range g.Edges {
		fi, fok := nodeIdx[e.From]
		ti, tok := nodeIdx[e.To]
		if !fok || !tok {
			continue
		}
		if _, inFrom := fromPos[fi]; !inFrom {
			continue
		}
		if _, inTo := toPos[ti]; !inTo {
			continue
		}
		conns = append(conns, connEdge{fi, ti, e.Kind, e.Label})
	}
	return conns
}

// buildNodeToLayer returns a map from node index to layer index across all layers.
func buildNodeToLayer(g *Graph) map[int]int {
	m := make(map[int]int, len(g.Nodes))
	for li, layer := range g.Layers {
		for _, ni := range layer {
			m[ni] = li
		}
	}
	return m
}

// nodeLayerCenter returns the x-center column of node ni within its actual layer.
func nodeLayerCenter(g *Graph, ni int, nodeToLayer map[int]int, boxes []renderedBox) int {
	li := nodeToLayer[ni]
	layer := g.Layers[li]
	centers := layerCenters(layer, boxes)
	for pos, idx := range layer {
		if idx == ni {
			return centers[pos]
		}
	}
	return 0
}

// stemLine builds the vertical stem line: │ at each parent center with a child,
// plus any additional columns from skip-layer edges.
func stemLine(conns []connEdge, fromPos map[int]int, fromCenters []int, extraCols []int, canvasW int) string {
	parentHasChild := make(map[int]bool, len(conns))
	for _, c := range conns {
		fp := fromPos[c.fromNodeIdx]
		parentHasChild[fp] = true
	}
	pos := make(map[int]rune, len(parentHasChild)+len(extraCols))
	for fp := range parentHasChild {
		pos[fromCenters[fp]] = '│'
	}
	for _, col := range extraCols {
		pos[col] = '│'
	}
	return stampLine(canvasW, pos)
}

// _mergeCell is a rune stamped on the merge connector line with a draw priority.
type _mergeCell struct {
	r   rune
	pri int
}

const (
	_priDash  = iota // '─' lowest
	_priJoin         // '└' '┘' '┴'
	_priArrow        // '▼' highest — never overwritten
)

// mergeLine builds the horizontal merge + arrow line from a pre-built
// parentCenter→childCenters map. Uses priority stamping so '▼' is never
// overwritten by a horizontal '─' run from a sibling edge.
func mergeLine(parentChildren map[int][]int, canvasW int) string {
	cells := make(map[int]_mergeCell, canvasW)
	setCell := func(col int, r rune, p int) {
		if ex, ok := cells[col]; !ok || p > ex.pri {
			cells[col] = _mergeCell{r, p}
		}
	}
	for pc, childCols := range parentChildren {
		stampParentSegment(setCell, pc, childCols)
	}
	pos := make(map[int]rune, len(cells))
	for col, c := range cells {
		pos[col] = c.r
	}
	return stampLine(canvasW, pos)
}

// stampParentSegment draws the horizontal run, child arrows, and parent junction
// for a single parent fanning out to its children.
func stampParentSegment(setCell func(int, rune, int), pc int, childCols []int) {
	if len(childCols) == 0 {
		return
	}
	minC := slices.Min(childCols)
	maxC := slices.Max(childCols)
	runStart := min(pc, minC)
	runEnd := max(pc, maxC)

	for col := runStart; col <= runEnd; col++ {
		setCell(col, '─', _priDash)
	}
	for _, cc := range childCols {
		setCell(cc, '▼', _priArrow)
	}
	// Parent junction — skip when parent is directly above all children (▼ suffices).
	if pc != minC || pc != maxC {
		switch pc {
		case runStart:
			setCell(pc, '└', _priJoin)
		case runEnd:
			setCell(pc, '┘', _priJoin)
		default:
			setCell(pc, '┴', _priJoin)
		}
	}
}

// connectorOpts groups the inputs for renderConnectors.
type connectorOpts struct {
	bw           *termWriter
	g            *Graph
	fromLayerIdx int
	boxes        []renderedBox
	nodeIdx      map[string]int
	nodeToLayer  map[int]int
}

// renderConnectors prints vertical/horizontal connector lines between adjacent
// layers fromLayerIdx and fromLayerIdx+1, including stems for skip-layer edges
// (departing, pass-through, and arriving) and artifact edge annotations.
//
//revive:disable-next-line:cognitive-complexity,cyclomatic renderConnectors orchestrates adjacent and skip-layer connector passes; splitting would obscure the visual flow.
func renderConnectors(opts connectorOpts) {
	bw, g, fromLayerIdx, boxes, nodeIdx, nodeToLayer := opts.bw, opts.g, opts.fromLayerIdx, opts.boxes, opts.nodeIdx, opts.nodeToLayer
	fromLayer := g.Layers[fromLayerIdx]
	toLayer := g.Layers[fromLayerIdx+1]
	fromCenters := layerCenters(fromLayer, boxes)
	toCenters := layerCenters(toLayer, boxes)

	fromPos := make(map[int]int, len(fromLayer))
	for i, idx := range fromLayer {
		fromPos[idx] = i
	}
	toPos := make(map[int]int, len(toLayer))
	for i, idx := range toLayer {
		toPos[idx] = i
	}

	// Adjacent edges: source in fromLayer, target in toLayer.
	conns := gatherConns(g, fromPos, toPos, nodeIdx)

	// Build parentChildren from adjacent edges; will be extended by arriving skips.
	parentChildren := make(map[int][]int, len(conns))
	for _, c := range conns {
		pc := fromCenters[fromPos[c.fromNodeIdx]]
		tc := toCenters[toPos[c.toNodeIdx]]
		parentChildren[pc] = append(parentChildren[pc], tc)
	}

	// Handle skip-layer edges crossing this boundary.
	var extraStemCols []int
	var skipConns []connEdge // arriving skip edges, for artifact annotations
	for _, e := range g.Edges {
		fi, fok := nodeIdx[e.From]
		ti, tok := nodeIdx[e.To]
		if !fok || !tok {
			continue
		}
		fli, fliOK := nodeToLayer[fi]
		tli, tliOK := nodeToLayer[ti]
		if !fliOK || !tliOK {
			continue
		}
		switch {
		case fli == fromLayerIdx && tli > fromLayerIdx+1:
			// Departing: starts here, target is beyond toLayer — draw │ stem.
			extraStemCols = append(extraStemCols, fromCenters[fromPos[fi]])
		case fli < fromLayerIdx && tli == fromLayerIdx+1:
			// Arriving: from an earlier layer, terminates at toLayer.
			srcCenter := nodeLayerCenter(g, fi, nodeToLayer, boxes)
			extraStemCols = append(extraStemCols, srcCenter)
			parentChildren[srcCenter] = append(parentChildren[srcCenter], toCenters[toPos[ti]])
			skipConns = append(skipConns, connEdge{fi, ti, e.Kind, e.Label})
		case fli < fromLayerIdx && tli > fromLayerIdx+1:
			// Pass-through: from an earlier layer, going further — draw │ stem.
			extraStemCols = append(extraStemCols, nodeLayerCenter(g, fi, nodeToLayer, boxes))
		default:
			// Adjacent or same-layer edge; handled by gatherConns.
		}
	}

	if len(parentChildren) == 0 && len(extraStemCols) == 0 {
		return
	}

	canvasW := max(canvasWidth(fromLayer, boxes), canvasWidth(toLayer, boxes))
	for _, col := range extraStemCols {
		canvasW = max(canvasW, col+1)
	}

	bw.writeln(_styleConnector.Render(stemLine(conns, fromPos, fromCenters, extraStemCols, canvasW)))
	bw.writeln(_styleConnector.Render(mergeLine(parentChildren, canvasW)))

	for _, c := range append(conns, skipConns...) {
		if c.kind == ArtifactEdge {
			bw.writeln(fmt.Sprintf("      [artifact: %s]", c.label))
		}
	}
}

// layerCenters computes the horizontal center column (0-based rune offset) for
// each box in the layer slice (ordered by slice position).
func layerCenters(layer []int, boxes []renderedBox) []int {
	centers := make([]int, len(layer))
	col := 0
	for i, idx := range layer {
		w := boxes[idx].width
		centers[i] = col + w/2
		col += w + 2 // 2-space gap between boxes
	}
	return centers
}

// canvasWidth returns the total visual width of a layer's boxes (including gaps).
func canvasWidth(layer []int, boxes []renderedBox) int {
	if len(layer) == 0 {
		return 0
	}
	total := 0
	for i, idx := range layer {
		if i > 0 {
			total += 2
		}
		total += boxes[idx].width
	}
	return total
}

// stampLine builds a string of length `width` spaces with runes stamped at the
// specified column positions.
func stampLine(width int, positions map[int]rune) string {
	runes := make([]rune, width)
	for i := range runes {
		runes[i] = ' '
	}
	for col, r := range positions {
		if col >= 0 && col < width {
			runes[col] = r
		}
	}
	return string(runes)
}

// buildBoxContent returns parallel raw and styled content lines for n.
// raw lines drive width calculation; styled lines carry ANSI codes for display.
//
//codecheck:allow-emojis
func buildBoxContent(n Node) (raw, styled []string) {
	add := func(r, s string) {
		raw = append(raw, r)
		styled = append(styled, s)
	}

	add(n.ID, _styleJobName.Render(n.ID))

	for _, s := range n.Steps {
		const bullet = "○" //codecheck:allow-emojis
		rawLine := "  " + bullet + " " + s.Name
		styledLine := "  " + _styleBullet.Render(bullet) + " " + s.Name
		if s.AllowFailure {
			rawLine += " [!]"
			styledLine += " " + _styleAllowFail.Render("[!]")
		}
		if s.When != "" {
			rawLine += " (when: " + s.When + ")"
			styledLine += " " + _styleWhenLabel.Render("(when:") + " " + _styleWhenExpr.Render(s.When) + _styleWhenLabel.Render(")")
		}
		add(rawLine, styledLine)
	}
	if n.When != "" {
		add(
			"  when: "+n.When,
			"  "+_styleWhenLabel.Render("when:")+" "+_styleWhenExpr.Render(n.When),
		)
	}
	for _, m := range n.Matrix {
		add(
			"  matrix: "+m,
			"  "+_styleMatrix.Render("matrix:")+" "+m,
		)
	}
	if n.HasSecrets {
		add("  secrets: yes", "  "+_styleSecrets.Render("secrets: yes"))
	}
	if n.Publish != "" {
		add(
			"  publish: "+n.Publish,
			"  "+_stylePublish.Render("publish:")+" "+n.Publish,
		)
	}
	return raw, styled
}

// buildBox constructs the fixed-width box lines for a node using rounded borders
// and per-field ANSI color styles. Width calculation uses raw (unstyled) strings
// to avoid counting ANSI escape bytes.
func buildBox(n Node) renderedBox {
	raw, styled := buildBoxContent(n)

	maxContent := 0
	for _, c := range raw {
		maxContent = max(maxContent, utf8.RuneCountInString(c))
	}
	innerW := max(maxContent, _minBoxWidth-4) // 4 = 2 border + 2 padding
	boxW := innerW + 4

	b := lipgloss.RoundedBorder()
	top := _styleBorder.Render(b.TopLeft + strings.Repeat(b.Top, boxW-2) + b.TopRight)
	mid := _styleBorder.Render(b.MiddleLeft + strings.Repeat(b.Top, boxW-2) + b.MiddleRight)
	bottom := _styleBorder.Render(b.BottomLeft + strings.Repeat(b.Bottom, boxW-2) + b.BottomRight)
	lBorder := _styleBorder.Render(b.Left)
	rBorder := _styleBorder.Render(b.Right)

	lines := make([]string, 0, len(raw)+4)
	lines = append(lines, top)
	for i, s := range styled {
		pad := strings.Repeat(" ", innerW-utf8.RuneCountInString(raw[i]))
		lines = append(lines, lBorder+" "+s+pad+" "+rBorder)
		if i == 0 {
			lines = append(lines, mid)
		}
	}
	lines = append(lines, bottom)

	return renderedBox{lines: lines, width: boxW}
}

// termWriter is a simple error-accumulating writer.
type termWriter struct {
	w   io.Writer
	err error
}

func (tw *termWriter) writeln(s string) {
	if tw.err != nil {
		return
	}
	_, tw.err = lipgloss.Fprintln(tw.w, s)
}
