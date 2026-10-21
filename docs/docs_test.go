package docs

// Structural tests for the documentation in this directory. They are not a
// style police: each check guards something that has actually rotted in
// documentation before — a diagram that stops rendering, a cross-link to a
// renamed file, an anchor that no longer exists, a half-closed code fence
// that swallows the rest of a page.
//
// Everything here reads the .md files next to this test, so the docs stay
// the single source of truth and the test only asserts properties of them.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sorotrail/sorotrail/internal/store"
)

// readDoc loads a markdown file from this directory.
func readDoc(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	require.NoErrorf(t, err, "documentation file %s is missing", name)
	return string(b)
}

// markdownDocs is every markdown file in this directory, discovered rather
// than listed so a new doc is covered by the generic checks automatically.
func markdownDocs(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("*.md")
	require.NoError(t, err)
	require.NotEmpty(t, matches, "no markdown files found in docs/")
	return matches
}

// TestDocsIndexIncludesAllDocs ensures every .md file in docs/ (except
// README.md) is linked from docs/README.md so a new doc cannot be added
// without updating the index.
func TestDocsIndexIncludesAllDocs(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read docs directory: %v", err)
	}

	documents := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".md" || entry.Name() == "README.md" {
			continue
		}
		documents[entry.Name()] = struct{}{}
	}

	if len(documents) == 0 {
		t.Fatal("expected at least one Markdown document under docs/")
	}

	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("README.md should exist in docs/: %v", err)
	}
	content := string(readme)

	for name := range documents {
		if !strings.Contains(content, "["+name+"]("+name+")") {
			t.Fatalf("docs/README.md is missing an entry for %s", name)
		}
	}
}

// TestRootReadmeLinksDocsIndex ensures the root README.md links to the
// documentation index so visitors can discover the full docs.
func TestRootReadmeLinksDocsIndex(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatalf("read root README.md: %v", err)
	}

	content := string(readme)
	if !strings.Contains(content, "docs/README.md") && !strings.Contains(content, "documentation index") {
		t.Fatal("root README.md should link to the documentation index")
	}
}

// TestCodeFencesAreBalanced catches the classic markdown bug: an unclosed
// ``` fence, which renders the whole remainder of the page as code.
func TestCodeFencesAreBalanced(t *testing.T) {
	for _, name := range markdownDocs(t) {
		t.Run(name, func(t *testing.T) {
			var fences int
			for _, line := range strings.Split(readDoc(t, name), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "```") {
					fences++
				}
			}
			assert.Zerof(t, fences%2, "%s has an odd number of ``` fences (%d): one is unclosed", name, fences)
		})
	}
}

// TestRelativeLinksResolve checks every relative markdown link between docs
// points at a file that exists, so renaming a doc cannot silently orphan
// the links into it.
func TestRelativeLinksResolve(t *testing.T) {
	linkRE := regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

	for _, name := range markdownDocs(t) {
		t.Run(name, func(t *testing.T) {
			for _, m := range linkRE.FindAllStringSubmatch(readDoc(t, name), -1) {
				target := m[1]
				switch {
				case strings.HasPrefix(target, "http://"), strings.HasPrefix(target, "https://"):
					continue // external, not ours to verify
				case strings.HasPrefix(target, "#"):
					continue // same-page anchor, checked by TestAnchorsResolve
				}
				path, _, _ := strings.Cut(target, "#")
				if path == "" {
					continue
				}
				_, err := os.Stat(path)
				assert.NoErrorf(t, err, "%s links to %q, which does not exist", name, target)
			}
		})
	}
}

// headingAnchors returns the GitHub-style anchor slugs of every heading in
// a markdown document.
func headingAnchors(doc string) map[string]bool {
	anchors := map[string]bool{}
	drop := regexp.MustCompile("[^a-z0-9 -]")
	for _, line := range strings.Split(doc, "\n") {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		text := strings.TrimSpace(strings.TrimLeft(line, "#"))
		// Strip inline code and link syntax the way GitHub does before
		// slugging: backticks vanish, [text](url) keeps only text.
		text = strings.ReplaceAll(text, "`", "")
		text = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`).ReplaceAllString(text, "$1")
		slug := drop.ReplaceAllString(strings.ToLower(text), "")
		anchors[strings.ReplaceAll(slug, " ", "-")] = true
	}
	return anchors
}

// TestAnchorsResolve checks that same-page `#anchor` links, and
// `file.md#anchor` links between docs in this directory, name a heading
// that actually exists.
func TestAnchorsResolve(t *testing.T) {
	linkRE := regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

	for _, name := range markdownDocs(t) {
		t.Run(name, func(t *testing.T) {
			doc := readDoc(t, name)
			for _, m := range linkRE.FindAllStringSubmatch(doc, -1) {
				target := m[1]
				if strings.HasPrefix(target, "http") {
					continue
				}
				path, anchor, hasAnchor := strings.Cut(target, "#")
				if !hasAnchor || anchor == "" {
					continue
				}
				targetdoc := doc
				if path != "" {
					if filepath.Ext(path) != ".md" || strings.Contains(path, "/") {
						continue // outside this directory; not ours to slug
					}
					b, err := os.ReadFile(path)
					if err != nil {
						continue // reported by TestRelativeLinksResolve
					}
					targetdoc = string(b)
				}
				assert.Truef(t, headingAnchors(targetdoc)[anchor],
					"%s links to %q but no heading in %s produces that anchor",
					name, target, map[bool]string{true: path, false: name}[path != ""])
			}
		})
	}
}

// TestRequiredSections pins the sections a reader is promised by each
// guide. Table-driven: one row per document, listing headings that must be
// present for the document to answer the question it exists to answer.
func TestRequiredSections(t *testing.T) {
	tests := []struct {
		doc      string
		headings []string
	}{
		{
			doc: "replay.md",
			headings: []string{
				"## When to run it",
				"## Usage",
				"## The workflow, end to end",
				"### 1. Confirm the new decoder is the one deployed",
				"### 2. Size the job",
				"### 3. Dry-run the range",
				"### 4. Spot-check a single event first",
				"### 5. Run it",
				"### 6. Verify",
				"## Interrupting and resuming",
				"## Running against a live database",
				"## Rows without raw XDR",
			},
		},
		{
			doc: "troubleshooting.md",
			headings: []string{
				"## Startup and configuration errors",
				"## RPC errors",
				"## Database errors",
				"## API errors",
				"### A contract is watched but no events are ingested",
				"## Replay errors",
				"## First things to check",
			},
		},
		{
			doc: "architecture.md",
			headings: []string{
				"## System overview",
				"### Component diagram",
				"### Data flow",
				"### Request and ingest lifecycle",
				"### Replay lifecycle",
				"## Components",
				"#### The watch list and its network dimension",
			},
		},
		{
			doc: "graphql.md",
			headings: []string{
				"## Enabling the endpoint and the playground",
				"## Authentication",
				"## Top-level query fields",
				"## Pagination",
				"## Limits: depth and complexity",
				"## Filter parity with `GET /events`",
				"## Multi-tenant behaviour by query field",
				"## When to use GraphQL instead of REST",
			},
		},
		{
			doc: "spec-overrides.md",
			headings: []string{
				"## When an override is needed",
				"## Authentication",
				"## Endpoints",
				"## Accepted JSON format",
				"## Validation rules and error messages",
				"## Wrapper vs. bare request body",
				"## Precedence and contract upgrades",
				"## Caching and propagation timing",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.doc, func(t *testing.T) {
			doc := readDoc(t, tt.doc)
			for _, h := range tt.headings {
				assert.Containsf(t, doc, h+"\n", "%s must document %q", tt.doc, h)
			}
		})
	}
}

// mermaidBlocks returns the body of every ```mermaid fence in a document.
func mermaidBlocks(doc string) []string {
	re := regexp.MustCompile("(?s)```mermaid\n(.*?)```")
	var out []string
	for _, m := range re.FindAllStringSubmatch(doc, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestArchitectureDiagrams checks the diagrams in architecture.md are
// present, well-formed enough to render, and cover both the component view
// and the sequence view.
func TestArchitectureDiagrams(t *testing.T) {
	doc := readDoc(t, "architecture.md")
	blocks := mermaidBlocks(doc)
	require.NotEmpty(t, blocks, "architecture.md must contain mermaid diagrams")

	// Every diagram declares a type mermaid understands on its first
	// non-empty line; a typo there renders as an error box on GitHub.
	knownTypes := []string{"flowchart", "graph", "sequenceDiagram", "erDiagram", "stateDiagram", "classDiagram"}
	var haveFlowchart, haveSequence bool

	for i, b := range blocks {
		t.Run(fmt.Sprintf("block_%d", i+1), func(t *testing.T) {
			var first string
			for _, line := range strings.Split(b, "\n") {
				if strings.TrimSpace(line) != "" {
					first = strings.TrimSpace(line)
					break
				}
			}
			require.NotEmpty(t, first, "empty mermaid block")

			var known bool
			for _, kt := range knownTypes {
				if strings.HasPrefix(first, kt) {
					known = true
					if kt == "flowchart" || kt == "graph" {
						haveFlowchart = true
					}
					if kt == "sequenceDiagram" {
						haveSequence = true
					}
				}
			}
			assert.Truef(t, known, "diagram starts with %q, which is not a mermaid diagram type", first)

			// Tabs are not valid mermaid indentation and render
			// inconsistently across viewers.
			assert.NotContains(t, b, "\t", "mermaid blocks must be indented with spaces")

			// subgraph/end and alt/loop/opt/end must balance, the most
			// common way a hand-edited diagram stops parsing.
			assert.Equalf(t, countKeyword(b, "subgraph")+countKeyword(b, "loop")+
				countKeyword(b, "alt")+countKeyword(b, "opt"),
				countKeyword(b, "end"),
				"block openers (subgraph/loop/alt/opt) and `end` do not balance")
		})
	}

	assert.True(t, haveFlowchart, "architecture.md must contain a component/data-flow diagram")
	assert.True(t, haveSequence, "architecture.md must contain a sequence diagram")
}

// countKeyword counts lines whose first word is the given mermaid keyword.
func countKeyword(block, keyword string) int {
	var n int
	for _, line := range strings.Split(block, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == keyword {
			n++
		}
	}
	return n
}

// TestTroubleshootingEntriesAreActionable requires every troubleshooting
// entry to carry the three bullets the guide promises in its preamble. An
// entry with a symptom and no fix is the failure mode worth guarding.
func TestTroubleshootingEntriesAreActionable(t *testing.T) {
	doc := readDoc(t, "troubleshooting.md")

	// Split on entry headings (###), skipping the preamble.
	parts := strings.Split(doc, "\n### ")
	require.Greater(t, len(parts), 5, "troubleshooting.md should document several errors")

	for _, part := range parts[1:] {
		title, body, _ := strings.Cut(part, "\n")
		t.Run(strings.TrimSpace(title), func(t *testing.T) {
			for _, field := range []string{"**Symptom**", "**Cause**", "**Fix**"} {
				assert.Containsf(t, body, field,
					"entry %q must state %s", title, field)
			}
		})
	}
}

// TestClickHouseSupportMatrixMatchesStoreInterface keeps the ClickHouse
// support matrix honest in both directions: every method it names must still
// exist on store.Store, and every Store method must be named somewhere in the
// matrix. The state column stays a human judgement, but adding a method to the
// interface now fails CI until its ClickHouse behaviour is documented too.
func TestClickHouseSupportMatrixMatchesStoreInterface(t *testing.T) {
	doc := readDoc(t, "clickhouse.md")

	// Scope the scan to the matrix section so examples and prose cannot
	// satisfy the coverage check by accident.
	_, section, ok := strings.Cut(doc, "## Support matrix")
	require.True(t, ok, "clickhouse.md must have a ## Support matrix section")
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}

	// Non-method identifiers are allowed in table cells; anything else
	// backticked and capitalised is treated as a Store method name.
	nonMethods := map[string]bool{
		"ErrUnsupported": true,
		"ErrNotFound":    true,
	}

	backtick := regexp.MustCompile("`([^`]+)`")
	named := map[string]bool{}
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		// The matrix layout is | Capability | Methods | State | Notes |,
		// so only the Methods cell contributes method names. Restricting
		// the scan here keeps SQL keywords and notes from being mistaken
		// for interface methods.
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		methodCell := cells[2]
		if strings.TrimSpace(methodCell) == "Methods" {
			continue // header row
		}
		for _, m := range backtick.FindAllStringSubmatch(methodCell, -1) {
			token := m[1]
			if token == "" || nonMethods[token] {
				continue
			}
			// Exported Go identifiers start uppercase; prose identifiers
			// are not methods.
			if token[0] < 'A' || token[0] > 'Z' {
				continue
			}
			named[token] = true
		}
	}
	require.NotEmpty(t, named, "the support matrix must name Store methods")

	storeType := reflect.TypeOf((*store.Store)(nil)).Elem()
	for name := range named {
		_, ok := storeType.MethodByName(name)
		assert.Truef(t, ok,
			"clickhouse.md names %q as a Store method, but store.Store no longer has it", name)
	}
	for i := 0; i < storeType.NumMethod(); i++ {
		name := storeType.Method(i).Name
		assert.Truef(t, named[name],
			"store.Store.%s is not named in the ClickHouse support matrix; document whether it is implemented, refuses, or silently does nothing", name)
	}
}

// TestCommunityFiles guards the community-standard documents GitHub surfaces in
// the contribution sidebar and the community-standards checklist. They live at
// the repository root rather than in docs/, so the per-document checks above
// cannot see them: a vanished code of conduct, a dropped link, or a reporting
// contact that disappears would otherwise ship silently.
func TestCommunityFiles(t *testing.T) {
	rootFile := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", name))
		require.NoErrorf(t, err, "%s should exist at the repository root", name)
		return string(b)
	}

	coc := rootFile("CODE_OF_CONDUCT.md")
	assert.Contains(t, coc, "[Contributor Covenant]",
		"CODE_OF_CONDUCT.md should keep the recognised Contributor Covenant text")
	assert.Contains(t, coc, "reported to the SoroTrail maintainers",
		"CODE_OF_CONDUCT.md must say who handles a report")
	assert.Contains(t, coc, "@sorotrail.io",
		"CODE_OF_CONDUCT.md must publish a real reporting contact")
	assert.Regexpf(t, `within [0-9]+ (business )?(hour|day|week)`, coc,
		"CODE_OF_CONDUCT.md must say how quickly a report is acknowledged")
	// Vulnerability reports have their own channel in SECURITY.md; neither
	// document should read as covering the other's reports.
	assert.NotContains(t, strings.ToLower(coc), "vulnerabilit",
		"CODE_OF_CONDUCT.md must not claim to cover vulnerability reports")

	for _, name := range []string{"CONTRIBUTING.md", "README.md"} {
		assert.Containsf(t, rootFile(name), "(CODE_OF_CONDUCT.md)",
			"%s should link to CODE_OF_CONDUCT.md", name)
	}
	assert.Contains(t, rootFile("SECURITY.md"), "(CODE_OF_CONDUCT.md)",
		"SECURITY.md should point conduct reports at the Code of Conduct")
}
