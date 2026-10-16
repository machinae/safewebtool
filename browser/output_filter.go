package browser

// output_filter.go holds the model-facing output modifiers: AX-region
// scoping (include/exclude), a post-render section slice, and a post-render
// line filter. They shape how much of a page reaches the model on a read,
// expressed in the universal accessibility role vocabulary so they can be
// set blind, with no prior snapshot, and applied identically to snapshot,
// markdown, and html.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// OutputFilter shapes a group's page read. The zero value is the whole
// page with no filtering.
type OutputFilter struct {
	// IncludeElementIDs roots the output at these element ids from a
	// snapshot, returning only their subtrees. Composes with
	// IncludeAXScope: the two unite into one set of include roots. A
	// requested id that is absent from the page is a loud miss, not a
	// no-op (the resolution layer waits and refetches once, then errors),
	// so the caller knows to re-snapshot.
	IncludeElementIDs []string
	// IncludeAXScope keeps only nodes under regions matching these AX
	// selectors. A selector that matches nothing simply contributes no
	// root. Empty (with no IncludeElementIDs) means the whole page is the
	// root.
	IncludeAXScope []string
	// ExcludeAXScope drops nodes under regions matching these AX
	// selectors. Composes with the includes: includes select the root(s),
	// exclude subtracts subtrees within.
	ExcludeAXScope []string
	// Match, when set, keeps only rendered lines matching this Go regular
	// expression. Applied last, to the final output of any mode.
	Match string
	// Section, when set, keeps only one section of the rendered output: the
	// heading whose text equals it (case-insensitive, trimmed) through the
	// next heading of the same or higher level, or the page end. The matched
	// heading is included. A document-order slice, so it works on snapshot
	// and markdown but not html. Applied after rendering, before Match.
	Section string
}

// hasIncludeScope reports whether any include rooting is requested, by
// element id or AX selector. When false the whole page is the root.
func (f OutputFilter) hasIncludeScope() bool {
	return len(f.IncludeElementIDs) > 0 || len(f.IncludeAXScope) > 0
}

// hasScope reports whether any region scoping (include or exclude) is
// requested, so the reader knows to take the scoped path.
func (f OutputFilter) hasScope() bool {
	return f.hasIncludeScope() || len(f.ExcludeAXScope) > 0
}

// axSelector is a parsed AX scope selector: a role, optionally narrowed to
// an accessible name. "main" matches every node with role main;
// "region:About" matches a region whose name is About.
type axSelector struct {
	role    string
	name    string
	hasName bool
}

// parseAXSelector splits "role" or "role:name" on the first colon.
// Whatever the model sends is matched as-is against the live AX tree;
// there is no list of known roles, so a selector that names nothing simply
// matches nothing.
func parseAXSelector(sel string) axSelector {
	if role, name, ok := strings.Cut(sel, ":"); ok {
		return axSelector{role: role, name: normalizeText(name), hasName: true}
	}
	return axSelector{role: sel}
}

// matches reports whether an AX node satisfies the selector: role equal,
// and when a name is given, the node's accessible name equal after
// normalization.
func (s axSelector) matches(role, name string) bool {
	if role != s.role {
		return false
	}
	if s.hasName {
		return normalizeText(name) == s.name
	}
	return true
}

// applyMatch keeps only the lines of s that match pattern, a Go regular
// expression, and rejoins them. An empty pattern returns s unchanged. An
// invalid pattern is a loud error, never a silent passthrough.
func applyMatch(s, pattern string) (string, error) {
	if pattern == "" {
		return s, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid match regex %q: %w", pattern, err)
	}
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if re.MatchString(line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n"), nil
}

// sectionFormat selects how applySection recognizes a heading line, since
// snapshot and markdown render headings differently.
type sectionFormat int

const (
	sectionMarkdown sectionFormat = iota // ATX headings: "## History"
	sectionSnapshot                      // AX lines: [id] heading[2] "History"
)

var (
	// mdHeadingRe matches an ATX markdown heading, capturing the level (run
	// of #) and the heading text.
	mdHeadingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	// snapHeadingRe matches a snapshot heading line, capturing the level and
	// the quoted accessible name. The name is optional (a heading with no
	// accessible name renders without the quoted part).
	snapHeadingRe = regexp.MustCompile(`heading\[(\d+)\](?:\s+"([^"]*)")?`)
)

// headingOf reports whether line is a heading in the given format and, if
// so, its level and text. Levels are 1 (top) to 6.
func headingOf(line string, format sectionFormat) (level int, text string, ok bool) {
	re := mdHeadingRe
	if format == sectionSnapshot {
		re = snapHeadingRe
	}
	m := re.FindStringSubmatch(line)
	if m == nil {
		return 0, "", false
	}
	if format == sectionSnapshot {
		n, _ := strconv.Atoi(m[1])
		return n, m[2], true
	}
	return len(m[1]), strings.TrimSpace(m[2]), true
}

// applySection keeps only the named section of rendered text: the heading
// whose text equals name (case-insensitive, trimmed) and every line after
// it until the next heading of the same or higher level, or the end. The
// matched heading is included. An empty name returns text unchanged. A name
// matching no heading is a loud error listing the headings that are present,
// so the caller can correct the name in one turn.
//
// In markdown, a "#" inside a fenced code block is not a heading, so fenced
// regions are skipped. Snapshot output has no code fences.
func applySection(text, name string, format sectionFormat) (string, error) {
	if name == "" {
		return text, nil
	}
	target := strings.ToLower(strings.TrimSpace(name))

	lines := strings.Split(text, "\n")
	var headings []string
	inFence := false
	start, end, startLevel := -1, len(lines), 0

	for i, line := range lines {
		if format == sectionMarkdown && isCodeFence(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		level, htext, ok := headingOf(line, format)
		if !ok {
			continue
		}
		if start == -1 {
			headings = append(headings, htext)
			if strings.ToLower(strings.TrimSpace(htext)) == target {
				start, startLevel = i, level
			}
		} else if level <= startLevel {
			end = i
			break
		}
	}

	if start == -1 {
		return "", fmt.Errorf("no heading matching %q; headings: %s", name, strings.Join(headings, ", "))
	}
	return strings.Join(lines[start:end], "\n"), nil
}

// isCodeFence reports whether a markdown line opens or closes a fenced code
// block (``` or ~~~), so headings inside code are not treated as section
// boundaries.
func isCodeFence(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}
