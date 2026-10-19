package docs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
