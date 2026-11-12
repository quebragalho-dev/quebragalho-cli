package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mcpOutputOfTokens(tokens int) Result {
	return Result{Status: StatusOK, Output: strings.Repeat("x", tokens*4)}
}

// Between 10k and 25k tokens the model keeps the full output but sees a
// warning: the turn continues with eyes open instead of silently bloating.
func TestMCPOutputWarnsAbove10kTokens(t *testing.T) {
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "")
	result := enforceOutputCeiling("mcp_docs_lookup", mcpOutputOfTokens(12_000))
	if result.Truncated {
		t.Fatal("12k-token MCP output must not be truncated")
	}
	if !strings.Contains(result.Output, "aviso") || !strings.Contains(result.Output, "12000") {
		t.Fatalf("12k-token MCP output has no token warning:\n%.200s", result.Output)
	}
	if !strings.Contains(result.Meta["mcp.output_warning"], "12000") {
		t.Fatalf("Meta[mcp.output_warning] = %q, want the token count", result.Meta["mcp.output_warning"])
	}
}

// Past 25k tokens the output is capped head+tail, the full text spills to
// disk, and the model gets the path — nothing is lost.
func TestMCPOutputCapsAt25kWithSpillPath(t *testing.T) {
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "")
	t.Setenv("TMPDIR", t.TempDir())
	result := enforceOutputCeiling("mcp_docs_lookup", mcpOutputOfTokens(30_000))
	if !result.Truncated {
		t.Fatal("30k-token MCP output must be truncated")
	}
	// The MCP lane is 25k, wider than the 16k universal ceiling: the retained
	// output must exceed what the universal ceiling would keep.
	if len(result.Output) <= 16_000*4 {
		t.Fatalf("retained %d bytes, want the wider MCP lane past 16k tokens", len(result.Output))
	}
	if !strings.Contains(result.Output, "full output saved to") {
		t.Fatalf("truncated MCP output names no spill file:\n%.300s", result.Output)
	}
	path := spillPathFromNotice(t, result.Output)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spill %s: %v", path, err)
	}
	if len(content) != 30_000*4 {
		t.Fatalf("spill has %d bytes, want the full %d", len(content), 30_000*4)
	}
}

func spillPathFromNotice(t *testing.T, output string) string {
	t.Helper()
	const marker = "full output saved to "
	index := strings.Index(output, marker)
	if index < 0 {
		t.Fatalf("no spill marker in output:\n%.300s", output)
	}
	rest := output[index+len(marker):]
	end := strings.Index(rest, " ")
	if end < 0 {
		t.Fatalf("malformed spill marker in output:\n%.300s", output)
	}
	path := rest[:end]
	if !filepath.IsAbs(path) {
		t.Fatalf("spill path %q is not absolute", path)
	}
	return path
}

// MAX_MCP_OUTPUT_TOKENS overrides the 25k default per environment.
func TestMCPOutputCeilingEnvOverridesDefault(t *testing.T) {
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "100")
	result := enforceOutputCeiling("mcp_docs_lookup", mcpOutputOfTokens(200))
	if !result.Truncated {
		t.Fatal("200-token MCP output must be truncated at MAX_MCP_OUTPUT_TOKENS=100")
	}
}

// Small MCP outputs pass through untouched: no warning, no truncation.
func TestMCPOutputSmallPassesThrough(t *testing.T) {
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "")
	result := enforceOutputCeiling("mcp_docs_lookup", mcpOutputOfTokens(100))
	if result.Truncated || strings.Contains(result.Output, "aviso") {
		t.Fatalf("small MCP output was touched: truncated=%v output=%.100s", result.Truncated, result.Output)
	}
}
