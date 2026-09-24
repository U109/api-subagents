package codex

import (
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestCatalogReasoningLevels 保证目录完整且按强度排列，并能保留 max/ultra 默认值。
func TestCatalogReasoningLevels(t *testing.T) {
	want := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}
	for _, effort := range []string{"", "max", "ultra"} {
		model := catalogModel("mock", "mock", "", effort, 0, 256000, false)
		levels := shared.Arr(model["supported_reasoning_levels"])
		if len(levels) != len(want) {
			t.Fatalf("levels = %v", levels)
		}
		for i, level := range levels {
			option := shared.Obj(level)
			if option["effort"] != want[i] || shared.Str(option["description"]) == "" {
				t.Fatalf("option %d = %v", i, option)
			}
		}
		if effort == "" && model["default_reasoning_level"] != nil || shared.Str(model["default_reasoning_level"]) != effort {
			t.Fatalf("default = %v", model["default_reasoning_level"])
		}
	}
}
