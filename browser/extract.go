package browser

import (
	"context"
	"fmt"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/machinae/safewebtool/internal/rod/lib/proto"
	"golang.org/x/net/html"
)

// Extract returns page content as markdown or HTML. f scopes and filters
// the output the same way Snapshot's does: the include roots come from f's
// element ids and AX selectors (united), exclude subtracts, and match
// filters the rendered lines. The zero value returns the whole page.
//
// TODO: HTML() does not serialize shadow roots, so web-component pages
// extract as near-empty even though the snapshot (AX tree) sees their
// content. Now that extract is the only reading path, pierce shadow DOM
// here (DOM.getOuterHTML per shadow root, or a JS walker).
func (t *Tab) Extract(ctx context.Context, asHTML bool, f OutputFilter) (string, error) {
	return t.enqueue(ctx, func() (string, error) { return t.doExtract(ctx, asHTML, f) })
}

func (t *Tab) doExtract(ctx context.Context, asHTML bool, f OutputFilter) (string, error) {
	waitPageSettle(ctx, t.page)

	var html string
	var err error
	if f.hasScope() {
		html, err = t.scopedHTML(ctx, f)
	} else {
		html, err = t.page.Context(ctx).HTML()
	}
	if err != nil {
		return "", fmt.Errorf("extracting HTML: %w", err)
	}

	out := html
	if asHTML {
		if f.Section != "" {
			return "", fmt.Errorf("section is not supported for html output; use snapshot or markdown")
		}
	} else {
		if out, err = toMarkdown(html); err != nil {
			return "", err
		}
		if out, err = applySection(out, f.Section, sectionMarkdown); err != nil {
			return "", err
		}
	}
	return applyMatch(out, f.Match)
}

// scopedHTML serializes the page HTML under the filter's scope: the include
// regions (element ids and AX selectors, united; the whole page when none
// are given) with the exclude regions removed. It maps kept AX nodes to DOM
// elements by backend node id and concatenates the largest fully-kept
// subtrees, so a region with an excluded hole is serialized around the
// hole. A requested element id absent from the page is a loud miss
// (snapshotWithIDs), so the caller re-snapshots.
func (t *Tab) scopedHTML(ctx context.Context, f OutputFilter) (string, error) {
	ps, err := t.snapshotWithIDs(ctx, ModeDefault, f.IncludeElementIDs)
	if err != nil {
		return "", err
	}
	var roots []idResolution
	if f.hasIncludeScope() {
		roots = ps.includeRootIDs(f)
	} else {
		roots = []idResolution{{owner: ps, node: ps.nodeMap[ps.rootID]}}
	}
	exclude := ps.selectSet(f.ExcludeAXScope)
	straddles := ps.ancestorsOf(exclude)

	var parts []string
	for _, r := range roots {
		// Excludes resolve in the main frame (phase 1); their node ids would
		// not apply, and could even collide, inside a child frame's tree, so
		// drop them when a root lives in a frame.
		ex, st := exclude, straddles
		if r.owner != ps {
			ex, st = nil, nil
		}
		h, err := t.collectKeptHTML(ctx, r.owner, r.node.NodeID, ex, st)
		if err != nil {
			return "", err
		}
		if h != "" {
			parts = append(parts, h)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// collectKeptHTML returns the HTML of nodeID's subtree minus the excluded
// subtrees. A node in exclude is a hole (""). A node with no excluded
// descendant is serialized whole in one call. A straddling node (an
// excluded descendant somewhere below) recurses so the hole is cut out.
func (t *Tab) collectKeptHTML(
	ctx context.Context,
	ps *PageSnapshot,
	nodeID proto.AccessibilityAXNodeID,
	exclude, straddles map[proto.AccessibilityAXNodeID]bool,
) (string, error) {
	if exclude[nodeID] {
		return "", nil
	}
	node := ps.nodeMap[nodeID]
	if node == nil {
		return "", nil
	}
	// A StaticText's backend id is a DOM #text node, and ElementFromNode
	// promotes #text to its parent element. Serializing that parent would
	// re-include excluded siblings and duplicate the parent (once per text
	// child), so emit the text directly instead. This keeps the exclude
	// path lossy (inline wrapper dropped) but correct.
	if axStr(node.Role) == "StaticText" {
		return normalizeText(axStr(node.Name)), nil
	}
	if !straddles[nodeID] && node.BackendDOMNodeID != 0 {
		el, err := t.page.Context(ctx).ElementFromNode(&proto.DOMNode{BackendNodeID: node.BackendDOMNodeID})
		if err != nil {
			return "", fmt.Errorf("resolving scoped element: %w", err)
		}
		return el.HTML()
	}
	var parts []string
	for _, child := range ps.childMap[nodeID] {
		h, err := t.collectKeptHTML(ctx, ps, child, exclude, straddles)
		if err != nil {
			return "", err
		}
		if h != "" {
			parts = append(parts, h)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// toMarkdown converts an HTML fragment to markdown. Images with a data: URL
// are dropped: their base64 payload is useless to the model and can run to
// many kilobytes.
func toMarkdown(htmlInput string) (string, error) {
	conv := converter.NewConverter(converter.WithPlugins(base.NewBasePlugin(), commonmark.NewCommonmarkPlugin()))
	conv.Register.RendererFor("img", converter.TagTypeInline, func(ctx converter.Context, w converter.Writer, n *html.Node) converter.RenderStatus {
		for _, a := range n.Attr {
			if a.Key == "src" && strings.HasPrefix(strings.TrimSpace(a.Val), "data:") {
				return converter.RenderSuccess
			}
		}
		return converter.RenderTryNext
	}, converter.PriorityEarly)
	md, err := conv.ConvertString(htmlInput)
	if err != nil {
		return "", fmt.Errorf("converting HTML to markdown: %w", err)
	}
	return md, nil
}
