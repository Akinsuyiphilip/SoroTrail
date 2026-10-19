package buildinfo

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ldflagPattern captures the target of a linker -X flag: the package-qualified
// variable name, e.g. github.com/sorotrail/sorotrail/internal/buildinfo.Version.
// The non-greedy path stops at the first '=' so the injected value — a shell
// variable in the Makefile and Dockerfiles, a GoReleaser template elsewhere —
// is not part of the capture.
var ldflagPattern = regexp.MustCompile(`-X\s+(\S+?)=`)

// TestDefaultsArePlaceholders covers the documented fallback: a plain
// "go build" leaves every symbol as the non-empty placeholder "unknown".
// It asserts non-empty rather than the literal value so the same test passes
// when the test binary itself was stamped with real build info.
func TestDefaultsArePlaceholders(t *testing.T) {
	t.Parallel()

	assert.NotEmpty(t, Version, "Version must default to a non-empty placeholder")
	assert.NotEmpty(t, Commit, "Commit must default to a non-empty placeholder")
	assert.NotEmpty(t, BuildDate, "BuildDate must default to a non-empty placeholder")
}

// TestLDFlagsTargetsAreDeclared is the regression guard for this package's
// link-time stamping. A -X flag that names a variable that does not exist, or
// an existing variable under the wrong import path, is silently ignored by the
// linker: the build succeeds and the binary reports "unknown" forever. The
// four build configurations spell the path by hand, so a typo can only be
// caught by reading them; compiling cannot.
//
// Every failure names the file and the offending target so a contributor does
// not have to hunt through all four files. Files missing from a checkout are
// skipped rather than failed, so a renamed or partial tree does not turn the
// guard into a nuisance.
func TestLDFlagsTargetsAreDeclared(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	importPath := packageImportPath(t, root)
	declared := declaredSymbols(t)

	configs := []string{"Makefile", "Dockerfile", "Dockerfile.sorotrail", ".goreleaser.yaml"}
	for _, name := range configs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(filepath.Join(root, name))
			if errors.Is(err, fs.ErrNotExist) {
				t.Skipf("%s is not present in this checkout", name)
			}
			require.NoError(t, err)

			matches := ldflagPattern.FindAllStringSubmatch(string(data), -1)
			require.NotEmpty(t, matches,
				"%s must stamp build info with -X ldflags; removing them makes every binary report \"unknown\"", name)

			for _, match := range matches {
				target := match[1]
				pkgPath, symbol, ok := splitTarget(target)
				if !assert.True(t, ok, "%s: -X target %q is not a package-qualified variable", name, target) {
					continue
				}

				assert.Equal(t, importPath, pkgPath,
					"%s: -X target %q names package %q, but buildinfo lives at %q", name, target, pkgPath, importPath)
				assert.Contains(t, declared, symbol,
					"%s: -X target %q names %q, which buildinfo.go does not declare", name, target, symbol)
			}
		})
	}
}

// repoRoot walks up from the test's working directory until it finds the
// module's go.mod, so the guard does not hard-code how deep the package sits.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no go.mod found above %s", dir)
		dir = parent
	}
}

// packageImportPath derives the import path of this package from the module
// path in go.mod and the package's location relative to the repository root.
func packageImportPath(t *testing.T, root string) string {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)
	rel, err := filepath.Rel(root, wd)
	require.NoError(t, err)
	require.NotEqual(t, ".", rel, "the test must run from the package directory")

	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err)
	for _, line := range strings.Split(string(goMod), "\n") {
		if module, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return module + "/" + filepath.ToSlash(rel)
		}
	}

	t.Fatal("module path not found in go.mod")
	return ""
}

// declaredSymbols returns the package-level variables buildinfo.go declares.
// Parsing the source rather than hard-coding the three names is what makes a
// rename (e.g. BuildDate to Date) break the guard instead of quietly matching
// a stale list.
func declaredSymbols(t *testing.T) map[string]bool {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "buildinfo.go", nil, 0)
	require.NoError(t, err)

	symbols := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range value.Names {
				symbols[name.Name] = true
			}
		}
	}

	require.NotEmpty(t, symbols, "buildinfo.go declares no variables for ldflags to stamp")
	return symbols
}

// splitTarget separates a -X target into its package path and symbol at the
// last dot, which is the only split point that keeps a dotted path intact.
func splitTarget(target string) (pkgPath, symbol string, ok bool) {
	dot := strings.LastIndex(target, ".")
	if dot <= 0 || dot == len(target)-1 {
		return "", "", false
	}
	return target[:dot], target[dot+1:], true
}
