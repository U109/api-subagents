package config

import (
	"path/filepath"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestReasoningPersistence 覆盖旧配置缺省、各合法档位、保存重读和改名；非法值不能进入请求。
func TestReasoningPersistence(t *testing.T) {
	store := NewConfigStore(filepath.Join(t.TempDir(), "models.json"))
	for _, effort := range []any{nil, "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "invalid", 3, true} {
		profile := shared.Object{"protocol": "responses", "model": "mock"}
		if effort != nil {
			profile["reasoningEffort"] = effort
		}
		c, err := ValidateConfig(shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": profile}}), true)
		text, isString := effort.(string)
		valid := effort == nil || isString && text != "invalid"
		if !valid {
			if err == nil {
				t.Fatal("invalid effort accepted", effort)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Save(c); err != nil {
			t.Fatal(err)
		}
		c, err = store.Read()
		if err != nil || c.Models["demo"].ReasoningEffort != text {
			t.Fatal("save lost effort", err)
		}
		draft := Editable(c)
		draft.Models["renamed"] = draft.Models["demo"]
		delete(draft.Models, "demo")
		renamed, err := MergeKeys(shared.Marshal(draft), c, true)
		if err != nil || renamed.Models["renamed"].ReasoningEffort != text {
			t.Fatal("rename lost effort", err)
		}
	}
}
