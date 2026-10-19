package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const smallSpecYAML = `openapi: "3.0.3"
info:
  title: "Specgen Test API <v1> & More"
  version: "1.0.0"
paths:
  /ping:
    get:
      summary: Health check endpoint
      responses:
        "200":
          description: OK
`

var (
	specgenBinOnce sync.Once
	specgenBinPath string
	specgenBinErr  error
)

func buildSpecgenBinary(t *testing.T) string {
	t.Helper()
	specgenBinOnce.Do(func() {
		tmpDir, err := os.MkdirTemp("", "specgen-bin-*")
		if err != nil {
			specgenBinErr = err
			return
		}
		bin := filepath.Join(tmpDir, "specgen")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, ".")
		out, err := cmd.CombinedOutput()
		if err != nil {
			specgenBinErr = err
			return
		}
		_ = out
		specgenBinPath = bin
	})
	require.NoError(t, specgenBinErr, "building specgen binary")
	return specgenBinPath
}

// TestSpecgen_SmallKnownInput asserts that a small OpenAPI YAML renders into
// indented JSON with expected fields and formatting (#1097).
func TestSpecgen_SmallKnownInput(t *testing.T) {
	tmpDir := t.TempDir()
	inPath := filepath.Join(tmpDir, "spec.yaml")
	outPath := filepath.Join(tmpDir, "spec.json")

	require.NoError(t, os.WriteFile(inPath, []byte(smallSpecYAML), 0o644))

	err := run(inPath, outPath)
	require.NoError(t, err)

	content, err := os.ReadFile(outPath)
	require.NoError(t, err)

	jsonStr := string(content)
	assert.Contains(t, jsonStr, `"openapi": "3.0.3"`)
	assert.Contains(t, jsonStr, `"title": "Specgen Test API <v1> & More"`)
	assert.Contains(t, jsonStr, `"version": "1.0.0"`)
	assert.Contains(t, jsonStr, `"/ping": {`)
	assert.True(t, strings.HasSuffix(jsonStr, "\n"), "rendered JSON must end with a newline")
}

// TestSpecgen_PreservesKeyOrderAndHTMLEscaping verifies key order retention and
// raw HTML characters (<, &) without unicode escaping (#1097).
func TestSpecgen_PreservesKeyOrderAndHTMLEscaping(t *testing.T) {
	tmpDir := t.TempDir()
	inPath := filepath.Join(tmpDir, "order.yaml")
	outPath := filepath.Join(tmpDir, "order.json")

	specWithOrder := `z_first: "1"
a_second: "2"
m_third: "3 <tag> & url"
`
	require.NoError(t, os.WriteFile(inPath, []byte(specWithOrder), 0o644))

	require.NoError(t, run(inPath, outPath))

	content, err := os.ReadFile(outPath)
	require.NoError(t, err)
	s := string(content)

	posZ := strings.Index(s, `"z_first"`)
	posA := strings.Index(s, `"a_second"`)
	posM := strings.Index(s, `"m_third"`)

	assert.True(t, posZ < posA && posA < posM, "authored key order must be preserved: z before a before m")
	assert.Contains(t, s, "3 <tag> & url", "HTML characters must remain raw, not escaped as \\u003c")
}

// TestSpecgen_MalformedYAML asserts that malformed YAML causes an error and
// nothing is written to out (#1097).
func TestSpecgen_MalformedYAML(t *testing.T) {
	tmpDir := t.TempDir()
	inPath := filepath.Join(tmpDir, "bad.yaml")
	outPath := filepath.Join(tmpDir, "bad.json")

	require.NoError(t, os.WriteFile(inPath, []byte("paths: [unclosed"), 0o644))

	err := run(inPath, outPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rendering")

	_, statErr := os.Stat(outPath)
	assert.True(t, os.IsNotExist(statErr), "output file must not be created on malformed YAML")
}

// TestSpecgen_EmptyInput asserts that an empty file input is refused (#1097).
func TestSpecgen_EmptyInput(t *testing.T) {
	tmpDir := t.TempDir()
	inPath := filepath.Join(tmpDir, "empty.yaml")
	outPath := filepath.Join(tmpDir, "empty.json")

	require.NoError(t, os.WriteFile(inPath, []byte(""), 0o644))

	err := run(inPath, outPath)
	assert.Error(t, err)

	_, statErr := os.Stat(outPath)
	assert.True(t, os.IsNotExist(statErr), "output file must not be created on empty input")
}

// TestSpecgen_NonExistentInput asserts that missing input returns an error (#1097).
func TestSpecgen_NonExistentInput(t *testing.T) {
	tmpDir := t.TempDir()
	inPath := filepath.Join(tmpDir, "missing.yaml")
	outPath := filepath.Join(tmpDir, "missing.json")

	err := run(inPath, outPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reading")

	_, statErr := os.Stat(outPath)
	assert.True(t, os.IsNotExist(statErr), "output file must not be created on missing input")
}

// TestSpecgen_WriteError asserts failure when writing output fails (#1097).
func TestSpecgen_WriteError(t *testing.T) {
	tmpDir := t.TempDir()
	inPath := filepath.Join(tmpDir, "spec.yaml")
	outPath := filepath.Join(tmpDir, "nonexistent-dir", "out.json")

	require.NoError(t, os.WriteFile(inPath, []byte(smallSpecYAML), 0o644))

	err := run(inPath, outPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "writing")
}

// TestSpecgen_Determinism asserts that generating JSON twice produces byte-identical results (#1097).
func TestSpecgen_Determinism(t *testing.T) {
	tmpDir := t.TempDir()
	inPath := filepath.Join(tmpDir, "spec.yaml")
	outPath1 := filepath.Join(tmpDir, "spec1.json")
	outPath2 := filepath.Join(tmpDir, "spec2.json")

	require.NoError(t, os.WriteFile(inPath, []byte(smallSpecYAML), 0o644))

	require.NoError(t, run(inPath, outPath1))
	require.NoError(t, run(inPath, outPath2))

	bytes1, err := os.ReadFile(outPath1)
	require.NoError(t, err)
	bytes2, err := os.ReadFile(outPath2)
	require.NoError(t, err)

	assert.Equal(t, bytes1, bytes2, "specgen runs on identical input must be byte-identical")
}

// TestSpecgen_CLIExecution verifies the CLI flags and non-zero exit code (#1097).
func TestSpecgen_CLIExecution(t *testing.T) {
	bin := buildSpecgenBinary(t)
	tmpDir := t.TempDir()

	t.Run("success with flags", func(t *testing.T) {
		inPath := filepath.Join(tmpDir, "cli_valid.yaml")
		outPath := filepath.Join(tmpDir, "cli_spec.json")
		require.NoError(t, os.WriteFile(inPath, []byte(smallSpecYAML), 0o644))

		cmd := exec.Command(bin, "-in", inPath, "-out", outPath)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		require.NoError(t, err, "binary should exit 0 on valid spec: %s", stderr.String())

		content, err := os.ReadFile(outPath)
		require.NoError(t, err)
		assert.Contains(t, string(content), `"openapi": "3.0.3"`)
	})

	t.Run("failure with non-zero exit on malformed spec", func(t *testing.T) {
		inPath := filepath.Join(tmpDir, "cli_bad.yaml")
		outPath := filepath.Join(tmpDir, "cli_bad.json")
		require.NoError(t, os.WriteFile(inPath, []byte("bad: [yaml"), 0o644))

		cmd := exec.Command(bin, "-in", inPath, "-out", outPath)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		assert.Error(t, err, "binary should exit non-zero on malformed spec")

		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, 1, exitErr.ExitCode())
		assert.Contains(t, stderr.String(), "specgen:")

		_, statErr := os.Stat(outPath)
		assert.True(t, os.IsNotExist(statErr), "output file should not exist on failure")
	})
}
