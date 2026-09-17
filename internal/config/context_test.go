package config

import (
	"path/filepath"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestModelContextValidation 覆盖旧配置回退、容量边界、非法类型及取消选择后的清理。
func TestModelContextValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  any
		valid bool
	}{
		{"default", 0, true}, {"minimum", 4096, true}, {"128k", 128000, true},
		{"maximum", MaxContextWindow, true}, {"negative", -1, false},
		{"tooSmall", 4095, false}, {"tooLarge", MaxContextWindow + 1, false},
		{"fraction", 128000.5, false}, {"string", "128000", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := shared.Object{"protocol": "compatible", "model": "one", "relayModels": []string{"two"}, "modelContextWindows": shared.Object{"one": tc.size, "two": 200000, "removed": 1000000}}
			c, err := ValidateConfig(shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": p}}), true)
			if (err == nil) != tc.valid {
				t.Fatalf("validation: %v", err)
			}
			if !tc.valid {
				return
			}
			profile := c.Models["demo"]
			if profile.ModelContextWindows["removed"] != 0 || profile.ContextWindow("two") != 200000 || profile.ContextWindow("unknown") != DefaultContextWindow {
				t.Fatal("model context isolation failed")
			}
			if tc.size == 0 && profile.ContextWindow("one") != DefaultContextWindow {
				t.Fatal("old/default context changed")
			}
		})
	}
}

// TestModelContextPersistence 检查保存读回、编辑副本及连接改名不会污染原始容量或丢失独立模型设置。
func TestModelContextPersistence(t *testing.T) {
	c, err := ValidateConfig([]byte(`{"version":1,"models":{"demo":{"protocol":"compatible","model":"one","relayModels":["two"],"modelContextWindows":{"one":128000,"two":1000000}}}}`), true)
	if err != nil {
		t.Fatal(err)
	}
	store := NewConfigStore(filepath.Join(t.TempDir(), "models.json"))
	if err = store.Save(c); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	draft := Editable(saved)
	draft.Models["demo"].ModelContextWindows["one"] = 200000
	if saved.Models["demo"].ContextWindow("one") != 128000 {
		t.Fatal("draft mutated saved context")
	}
	draft.Models["renamed"] = draft.Models["demo"]
	delete(draft.Models, "demo")
	merged, err := MergeKeys(shared.Marshal(draft), saved, true)
	if err != nil || merged.Models["renamed"].ContextWindow("one") != 200000 || merged.Models["renamed"].ContextWindow("two") != 1000000 {
		t.Fatal("rename lost contexts", err)
	}
}
