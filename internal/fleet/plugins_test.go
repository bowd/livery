package fleet

import (
	"strings"
	"testing"
)

func TestSummarizeSkillshareIncludesPluginChanges(t *testing.T) {
	res := map[string]any{
		"installed": true,
		"version":   "skillshare v0.33.0",
		"diff": map[string]any{
			"targets": []any{
				map[string]any{"name": "claude", "synced": true, "items": []any{}},
			},
		},
		"plugins": map[string]any{
			"blocked": false,
			"changes": []any{
				map[string]any{
					"name":    "frontend-design",
					"target":  "claude",
					"action":  "install",
					"message": "Install plugin into claude",
				},
			},
		},
	}
	needs, lines := SummarizeSkillshare(res)
	if !needs {
		t.Fatalf("expected needs=true when plugin changes exist, lines=%v", lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "plugins(1 change)") || !strings.Contains(joined, "frontend-design") {
		t.Fatalf("unexpected summary lines:\n%s", joined)
	}

	// Noop-only plugin entries must NOT mark target as needing sync
	resNoop := map[string]any{
		"installed": true,
		"version":   "skillshare v0.33.0",
		"diff": map[string]any{
			"targets": []any{
				map[string]any{"name": "claude", "synced": true, "items": []any{}},
			},
		},
		"plugins": map[string]any{
			"blocked": false,
			"changes": []any{
				map[string]any{"name": "frontend-design", "target": "claude", "action": "noop"},
			},
		},
	}
	needsNoop, linesNoop := SummarizeSkillshare(resNoop)
	if needsNoop {
		t.Fatalf("expected needs=false for noop-only plugin changes, got lines=%v", linesNoop)
	}
}
