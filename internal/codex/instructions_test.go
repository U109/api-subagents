package codex

import (
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestCatalogCodingInstructions 确保两种目录指令入口一致，并保留反馈、收敛与权限边界。
func TestCatalogCodingInstructions(t *testing.T) {
	model := catalogModel("demo", "Demo", "", "", 0, 128000, false)
	base := shared.Str(model["base_instructions"])
	if base != codingInstructions || shared.Obj(model["model_messages"])["instructions_template"] != base {
		t.Fatal("catalog instruction entries diverged")
	}
	for _, required := range []string{"commentary", "continue working", "private reasoning", "independent read-only", "overlapping ranges", "output is truncated", "sufficient evidence", "do not authorize additional agents", "skip necessary safety checks and tests"} {
		if !strings.Contains(base, required) {
			t.Errorf("missing work boundary: %s", required)
		}
	}
}
