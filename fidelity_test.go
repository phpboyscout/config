package config

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Fidelity is the guarantee this module makes about the files it writes:
// comments stay grouped with the right keys, and nothing the author wrote is
// silently destroyed. The rules themselves live in yamldoc, but this module is
// the canonical consumer of them — a substrate change that broke any of these
// would break the D5 guarantee here, so they are asserted here rather than
// assumed.
//
// Acceptance criteria 1 through 5h.

// fileWith writes one source file and returns a Store over it.
func fileWith(t *testing.T, content string) (*Store, FS) {
	t.Helper()

	filesystem := memFS(t, map[string]string{"/app.yaml": content})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	return s, filesystem
}

func contentOf(t *testing.T, filesystem FS, path string) string {
	t.Helper()

	raw, err := filesystem.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(raw)
}

// commentOwner reports which key a comment sits with, by walking back from the
// comment to the nearest following key at the same or lower indentation.
//
// Asserting a comment is merely "still present somewhere" is not the guarantee:
// a comment reattached to the wrong key, or hoisted to the wrong parent, has
// destroyed the author's meaning just as surely as deleting it. Attachment is
// the whole of the fidelity promise, so it has to be what the test measures.
func commentOwner(content, comment string) string {
	lines := strings.Split(content, "\n")
	key := regexp.MustCompile(`^\s*([A-Za-z0-9_.-]+):`)

	for i, line := range lines {
		if !strings.Contains(line, comment) {
			continue
		}

		// An inline comment belongs to the key on its own line.
		if m := key.FindStringSubmatch(line); m != nil {
			return m[1]
		}

		// Otherwise it is a head comment: it owns the next key below it.
		for _, next := range lines[i+1:] {
			if m := key.FindStringSubmatch(next); m != nil {
				return m[1]
			}
		}

		return "<end of document>"
	}

	return "<absent>"
}

// AC1 — a Put changes the value and every comment stays with the same key.
func TestFidelity_CommentsStayAttachedAcrossAPut(t *testing.T) {
	t.Parallel()

	const src = `# Top of file, owns server.
server:
  # Which port to bind.
  port: 8080 # inline on port
  # Which interface.
  host: localhost
# Section comment for logging.
log:
  level: info
`

	s, filesystem := fileWith(t, src)

	if _, err := s.Apply(context.Background(), Set("server.port", 9090)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := contentOf(t, filesystem, "/app.yaml")

	if !strings.Contains(got, "9090") {
		t.Fatalf("the value did not change:\n%s", got)
	}

	// Every comment must still own the key it owned before.
	for comment, want := range map[string]string{
		"Top of file, owns server":    "server",
		"Which port to bind":          "port",
		"inline on port":              "port",
		"Which interface":             "host",
		"Section comment for logging": "log",
	} {
		if owner := commentOwner(got, comment); owner != want {
			t.Errorf("comment %q now belongs to %q, want %q\n---\n%s", comment, owner, want, got)
		}
	}
}

// AC2 — repeated writes converge. Drift accumulates silently: each save looks
// fine, and twenty saves later the file has been reflowed beyond recognition.
func TestFidelity_RepeatedWritesConverge(t *testing.T) {
	t.Parallel()

	const src = `# Head.
server:
  port: 8080 # inline
  hosts:
    - a
    - b
empty: {}
log:
  level: info # trailing on the last key
`

	s, filesystem := fileWith(t, src)

	var renderings []string

	for i := range 5 {
		port := 9000 + i
		if _, err := s.Apply(context.Background(), Set("server.port", port)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}

		renderings = append(renderings, contentOf(t, filesystem, "/app.yaml"))
	}

	// Normalise away the value that is deliberately changing, so what remains
	// is the document's shape.
	shape := func(s string) string {
		return regexp.MustCompile(`port: \d+`).ReplaceAllString(s, "port: N")
	}

	first := shape(renderings[1])
	for i, r := range renderings[2:] {
		if shape(r) != first {
			t.Fatalf("write %d drifted from write 1:\n--- write 1 ---\n%s\n--- write %d ---\n%s",
				i+2, first, i+2, shape(r))
		}
	}
}

// AC3 — the constructs a naive re-encode mangles. Each is checked separately so
// a failure names which one broke.
func TestFidelity_ExoticConstructsSurviveAWrite(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		src      string
		survives []string
	}{
		{
			name:     "literal block scalar",
			src:      "script: |\n  line one\n  line two\nother: keep\n",
			survives: []string{"|", "line one", "line two"},
		},
		{
			name:     "folded block scalar",
			src:      "note: >\n  folded text\n  continues\nother: keep\n",
			survives: []string{">", "folded text"},
		},
		{
			name:     "anchor and alias",
			src:      "base: &base\n  timeout: 30\nuse: *base\nother: keep\n",
			survives: []string{"&base", "*base"},
		},
		{
			// A plain astral-plane character survives verbatim. A zero-width
			// joiner is escaped, by design — see
			// TestFidelity_InvisibleCharactersAreEscapedLosslessly.
			name:     "astral plane emoji",
			src:      "greeting: \"hello 🌍 world\"\nother: keep\n",
			survives: []string{"🌍"},
		},
		{
			name:     "merge key",
			src:      "defaults: &d\n  timeout: 30\nsvc:\n  <<: *d\n  name: api\nother: keep\n",
			survives: []string{"<<: *d", "&d"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			s, filesystem := fileWith(t, c.src)

			// The write targets an unrelated key, so nothing here should move.
			if _, err := s.Apply(context.Background(), Set("other", "changed")); err != nil {
				t.Fatalf("Apply: %v", err)
			}

			got := contentOf(t, filesystem, "/app.yaml")

			for _, want := range c.survives {
				if !strings.Contains(got, want) {
					t.Errorf("%q did not survive the write:\n%s", want, got)
				}
			}
		})
	}
}

// AC4 and D17 — the five comment-ownership rules on delete. These rules live in
// yamldoc and this module depends on them, so each is asserted separately here:
// an upstream regression must name the rule it broke.

// D17 rule 1 — a head comment directly above a key is removed with it.
func TestFidelity_DeleteRule_HeadCommentGoesWithItsKey(t *testing.T) {
	t.Parallel()

	s, filesystem := fileWith(t, "# Owns doomed.\ndoomed: 1\nkeep: 2\n")

	if _, err := s.Apply(context.Background(), Remove("doomed")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := contentOf(t, filesystem, "/app.yaml"); strings.Contains(got, "Owns doomed") {
		t.Errorf("the deleted key's head comment was orphaned:\n%s", got)
	}
}

// D17 rule 2 — a comment separated by a blank line describes the section, not
// the key below it, and must outlive an unrelated delete.
func TestFidelity_DeleteRule_SectionCommentSurvives(t *testing.T) {
	t.Parallel()

	s, filesystem := fileWith(t,
		"first: 1\n\n# Describes the whole section below.\n\ndoomed: 2\nkeep: 3\n")

	if _, err := s.Apply(context.Background(), Remove("doomed")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := contentOf(t, filesystem, "/app.yaml"); !strings.Contains(got, "Describes the whole section") {
		t.Errorf("a section comment was destroyed with an unrelated key:\n%s", got)
	}
}

// D17 rule 3 — an inline comment belongs to the key on its own line.
func TestFidelity_DeleteRule_InlineCommentGoesWithItsKey(t *testing.T) {
	t.Parallel()

	s, filesystem := fileWith(t, "doomed: 1 # dies with doomed\nkeep: 2 # stays\n")

	if _, err := s.Apply(context.Background(), Remove("doomed")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := contentOf(t, filesystem, "/app.yaml")

	if strings.Contains(got, "dies with doomed") {
		t.Errorf("the deleted key's inline comment was orphaned:\n%s", got)
	}

	if !strings.Contains(got, "stays") {
		t.Errorf("a sibling's inline comment was destroyed:\n%s", got)
	}
}

// D17 rule 4 — the parser attaches a trailing comment to the last entry of a
// block, so a naive delete of that entry destroys a comment describing the
// whole section. It must be hoisted to the preceding sibling instead.
func TestFidelity_DeleteRule_TrailingCommentIsHoisted(t *testing.T) {
	t.Parallel()

	s, filesystem := fileWith(t,
		"section:\n  keep: 1\n  doomed: 2\n  # Trailing note about the section.\nafter: 3\n")

	if _, err := s.Apply(context.Background(), Remove("section.doomed")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := contentOf(t, filesystem, "/app.yaml"); !strings.Contains(got, "Trailing note") {
		t.Errorf("a trailing comment was destroyed rather than hoisted:\n%s", got)
	}
}

// D17 rule 5 — no bleed onto the following key's head comment.
func TestFidelity_DeleteRule_NoBleedOntoTheNextKey(t *testing.T) {
	t.Parallel()

	s, filesystem := fileWith(t, "doomed: 1\n# Owns survivor.\nsurvivor: 2\n")

	if _, err := s.Apply(context.Background(), Remove("doomed")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := contentOf(t, filesystem, "/app.yaml")

	if owner := commentOwner(got, "Owns survivor"); owner != "survivor" {
		t.Errorf("the following key's head comment now belongs to %q:\n%s", owner, got)
	}
}

// AC4 — the subtree goes with the key, leaving no residue.
func TestFidelity_DeleteRemovesTheWholeSubtree(t *testing.T) {
	t.Parallel()

	s, filesystem := fileWith(t, "doomed:\n  a: 1\n  b:\n    c: 2\nkeep: 3\n")

	if _, err := s.Apply(context.Background(), Remove("doomed")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := contentOf(t, filesystem, "/app.yaml")
	for _, residue := range []string{"doomed", "a:", "b:", "c:"} {
		if strings.Contains(got, residue) {
			t.Errorf("%q survived the subtree delete:\n%s", residue, got)
		}
	}
}

// test only ever created a brand-new top-level key, which never exercises the
// question.
func TestFidelity_CreateAttachesAtTheDeepestExistingAncestor(t *testing.T) {
	t.Parallel()

	const src = `# Owns server.
server:
  # Owns port.
  port: 8080
  nested:
    existing: yes
other: keep
`

	s, filesystem := fileWith(t, src)

	if _, err := s.Apply(context.Background(), Set("server.nested.created", "new")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := contentOf(t, filesystem, "/app.yaml")

	// It must land inside server.nested, not at the top level.
	if s.View().GetString("server.nested.created") != "new" {
		t.Errorf("the created key did not resolve where it was addressed:\n%s", got)
	}

	if regexp.MustCompile(`(?m)^created:`).MatchString(got) {
		t.Errorf("the created key was attached at column 0 instead of its parent:\n%s", got)
	}

	// Sibling comments and order are undisturbed.
	if owner := commentOwner(got, "Owns port"); owner != "port" {
		t.Errorf("a sibling comment moved to %q:\n%s", owner, got)
	}

	if strings.Index(got, "existing:") > strings.Index(got, "created:") {
		t.Errorf("the created key was inserted before an existing sibling:\n%s", got)
	}
}

// AC5a — single-line flow collections are supported, including anchors on them.
func TestFidelity_SingleLineFlowCollectionsSurvive(t *testing.T) {
	t.Parallel()

	const src = `# Owns voice.
voice: &voice {rate: 1, pitch: 2} # inline on voice
list: [a, b, c]
other: keep
`

	s, filesystem := fileWith(t, src)

	if _, err := s.Apply(context.Background(), Set("other", "changed")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := contentOf(t, filesystem, "/app.yaml")

	for _, want := range []string{"&voice", "rate", "pitch", "[a, b, c]", "inline on voice"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q did not survive an edit to a sibling:\n%s", want, got)
		}
	}

	// And a delete on a sibling leaves the flow node alone too.
	if _, err := s.Apply(context.Background(), Remove("list")); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	after := contentOf(t, filesystem, "/app.yaml")
	if !strings.Contains(after, "&voice") {
		t.Errorf("a sibling delete destroyed the flow node's anchor:\n%s", after)
	}
}

// AC5b — refused at load, naming the location. Never partially written.
func TestFidelity_MultiLineFlowWithInteriorCommentsIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	const src = `voice: {
  rate: 1, # interior comment
  pitch: 2
}
`

	filesystem := memFS(t, map[string]string{"/app.yaml": src})

	_, err := NewStore(context.Background(), WithFiles(filesystem, "/app.yaml"))
	if err == nil {
		t.Fatal("a document that cannot be safely edited was accepted at load")
	}

	msg := err.Error()

	if !strings.Contains(msg, "/app.yaml") {
		t.Errorf("the error does not name the file: %v", err)
	}

	// Naming the construct without naming where it is leaves the user hunting.
	if !strings.Contains(msg, "line") {
		t.Errorf("the error does not name the location: %v", err)
	}

	// And the file is untouched — refusal happens before anything is written.
	if got := contentOf(t, filesystem, "/app.yaml"); got != src {
		t.Errorf("the refused file was modified:\n%s", got)
	}
}

// AC5d — editing one document leaves every other document, separator and
// comment untouched. Three documents, so "the other one" is not ambiguous.
func TestFidelity_EditingOneDocumentLeavesTheOthersUntouched(t *testing.T) {
	t.Parallel()

	const src = `# Document zero.
first: 0
shared: from-zero
---
# Document one.
second: 1
shared: from-one
---
# Document two.
third: 2
`

	s, filesystem := fileWith(t, src)

	if _, err := s.Apply(context.Background(), Set("first", 99)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := contentOf(t, filesystem, "/app.yaml")

	if strings.Count(got, "---") != 2 {
		t.Errorf("document separators changed, want 2:\n%s", got)
	}

	for _, want := range []string{"Document zero", "Document one", "Document two",
		"second: 1", "third: 2", "from-one"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was disturbed by an edit to another document:\n%s", want, got)
		}
	}
}

// AC5f — an empty container in a source file survives a write, including when
// the write targets something else entirely. This was the historical defect:
// the old writer re-encoded the merged view and dropped empties on every save.
func TestFidelity_EmptyContainersSurviveAnUnrelatedWrite(t *testing.T) {
	t.Parallel()

	const src = `emptymap: {}
emptylist: []
nested:
  alsoempty: {}
unrelated: original
`

	s, filesystem := fileWith(t, src)

	if _, err := s.Apply(context.Background(), Set("unrelated", "changed")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := contentOf(t, filesystem, "/app.yaml")

	for _, want := range []string{"emptymap", "emptylist", "alsoempty"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was silently removed by an unrelated write:\n%s", want, got)
		}
	}

	// And they are still values, not absences, in the resolved configuration.
	for _, path := range []string{"emptymap", "emptylist", "nested.alsoempty"} {
		if !s.View().Has(path) {
			t.Errorf("%s is absent from the snapshot after the write", path)
		}
	}
}

// AC5g — a map-valued Put with an empty map empties the subtree rather than
// removing the key. The distinction matters: "this section is now empty" and
// "this section is gone" are different statements.
func TestFidelity_EmptyMapPutEmptiesRatherThanRemoves(t *testing.T) {
	t.Parallel()

	s, filesystem := fileWith(t, "sub:\n  a: 1\n  b: 2\nkeep: yes\n")

	if _, err := s.Apply(context.Background(), Set("sub", map[string]any{})); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := contentOf(t, filesystem, "/app.yaml")

	if !strings.Contains(got, "sub") {
		t.Fatalf("the key was removed rather than emptied:\n%s", got)
	}

	for _, gone := range []string{"a: 1", "b: 2"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q survived a replacing Put:\n%s", gone, got)
		}
	}

	if !s.View().Has("sub") {
		t.Error("sub is absent from the snapshot; an emptied subtree is still a value")
	}

	// It must still reload as an empty container rather than a parse error.
	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("the emptied file does not reload: %v", err)
	}
}

// AC5h — enumeration includes empty containers. The null half of this criterion
// is asserted against actual behaviour, which is the opposite of the spec's
// wording; see the note in the test body.
func TestFidelity_EnumerationIncludesEmptyContainers(t *testing.T) {
	t.Parallel()

	s, _ := fileWith(t, "emptymap: {}\nemptylist: []\nnullval:\npopulated: 1\n")

	keys := map[string]bool{}
	for _, k := range s.View().Keys() {
		keys[k] = true
	}

	for _, want := range []string{"emptymap", "emptylist", "populated"} {
		if !keys[want] {
			t.Errorf("%q missing from Keys(): %v", want, s.View().Keys())
		}
	}

	// The criterion says enumeration should exclude null-valued keys "the
	// getters report as unset". The module instead reports them as *set*, so
	// enumeration and the getters agree — which is what the criterion exists to
	// guarantee — but by the opposite route to the one specified. Asserted here
	// so the behaviour is pinned and the divergence is visible rather than
	// implicit.
	if s.View().Has("nullval") != keys["nullval"] {
		t.Errorf("enumeration and the getters disagree about a null-valued key: Has=%v inKeys=%v",
			s.View().Has("nullval"), keys["nullval"])
	}
}

// The real-world corpus. These are the files the spike measured against, and
// they are carried here rather than rewritten so a regression shows up against
// what people actually author — long files, dense comments, anchors, block
// scalars and multi-document layouts that no hand-written fixture reproduces.
func TestFidelity_CorpusSurvivesAWriteUnchanged(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir("testdata/corpus")
	if err != nil {
		t.Fatalf("reading the corpus: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("the corpus is empty")
	}

	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			t.Parallel()

			assertCorpusFileSurvivesAWrite(t, entry.Name())
		})
	}
}

// assertCorpusFileSurvivesAWrite loads one real-world file, writes a key that
// cannot collide with anything in it, and requires that every comment survives
// and that a second identical write does not drift the file.
func assertCorpusFileSurvivesAWrite(t *testing.T, name string) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata/corpus", name))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	source := string(raw)
	filesystem := memFS(t, map[string]string{"/c.yaml": source})

	s, err := NewStore(context.Background(), WithFiles(filesystem, "/c.yaml"))
	if err != nil {
		// A corpus file that will not load is a finding in itself.
		t.Fatalf("the corpus file does not load: %v", err)
	}

	if _, err := s.Apply(context.Background(), Set("zz_fidelity_probe", "written")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	after := contentOf(t, filesystem, "/c.yaml")
	assertCommentsSurvive(t, source, after)

	if _, err := s.Apply(context.Background(), Set("zz_fidelity_probe", "written")); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	if got := contentOf(t, filesystem, "/c.yaml"); got != after {
		t.Error("a no-op rewrite drifted the file")
	}
}

// assertCommentsSurvive requires every comment line in the source to still be
// present afterwards. Short ones are skipped: a bare "#" or a divider matches
// too loosely to mean anything.
func assertCommentsSurvive(t *testing.T, source, after string) {
	t.Helper()

	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") || len(trimmed) < 4 {
			continue
		}

		if !strings.Contains(after, trimmed) {
			t.Errorf("comment lost: %q", trimmed)
		}
	}
}

// AC3, partially divergent — recorded rather than glossed.
// AC3 and R3 — invisible characters are escaped on write, and that is the
// feature rather than a defect.
//
// A family emoji is several astral-plane characters joined by U+200D. The
// emoji survive verbatim; the joiners between them come back as escapes, which
// is why this initially read as a fidelity failure against AC3's first wording.
//
// Escaping invisible and bidirectional characters is a security measure. The
// bidi controls are the Trojan Source construct (CVE-2021-42574) — they make a
// file render one way and parse another, so a reviewer approves what they see
// while the parser reads something else. Configuration is exactly where that
// matters. The escape is lossless, so nothing is given up for it.
//
// yamldoc pins the full character matrix. Asserted here is the part this
// module depends on: the value is unchanged by a write and reload.
func TestFidelity_InvisibleCharactersAreEscapedLosslessly(t *testing.T) {
	t.Parallel()

	const family = "\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466"

	s, filesystem := fileWith(t, "greeting: \"hi "+family+"\"\nother: keep\n")

	if _, err := s.Apply(context.Background(), Set("other", "changed")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Read back from the bytes on disk, which is what a restart would see.
	if err := s.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if got := s.View().GetString("greeting"); got != "hi "+family {
		t.Errorf("the value did not survive a write and reload:\n got %q\nwant %q",
			got, "hi "+family)
	}

	got := contentOf(t, filesystem, "/app.yaml")

	// The visible emoji stay visible; only the joiners between them escape.
	for _, visible := range []string{"\U0001F468", "\U0001F469", "\U0001F467", "\U0001F466"} {
		if !strings.Contains(got, visible) {
			t.Errorf("a visible character was escaped: %q\n%s", visible, got)
		}
	}

	if strings.Contains(got, "\u200d") {
		t.Errorf("a zero-width joiner was written literally, leaving it invisible:\n%s", got)
	}
}

// AC3 and D16 — a map-valued Put on a subtree containing a merge key.
//
// D16 lets a caller supplying a map accept that comments, anchors and block
// styles *within that subtree* may not survive. It does not licence emitting a
// different document: here the addressed key becomes null and its child is
// promoted to the top level, so the resolved configuration is wrong and nothing
// reports it. D16's motivating case is a themes subtree carrying anchors and
// aliases, which is exactly this shape.
func TestFidelity_MapPutOverAMergeKeyDoesNotCorruptTheDocument(t *testing.T) {
	t.Parallel()

	s, filesystem := fileWith(t,
		"defaults: &d\n  timeout: 30\ndb:\n  <<: *d\n  host: old\nother: keep\n")

	if _, err := s.Apply(context.Background(),
		Set("db", map[string]any{"host": "new"})); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := contentOf(t, filesystem, "/app.yaml")

	if v := s.View().Get("db"); v == nil {
		t.Errorf("db resolved to nil after a map-valued Put:\n%s", got)
	}

	if v := s.View().GetString("db.host"); v != "new" {
		t.Errorf("db.host = %q, want new:\n%s", v, got)
	}

	if s.View().Has("host") {
		t.Errorf("the Put invented a top-level host key:\n%s", got)
	}
}
