package config

import (
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestImageInputSettings 验证视觉系列、手动覆盖、未知模型、清理已删除模型及编辑副本隔离。
func TestImageInputSettings(t *testing.T) {
	data := shared.Object{"version": 1, "models": shared.Object{"demo": shared.Object{"protocol": "responses", "model": "gemini-3.8-flash-high", "relayModels": []string{"private-vision", "gpt-6-astra", "deepseek-v4-flash"}, "modelImageInputs": shared.Object{"private-vision": true, "gpt-6-astra": false, "removed": true}}}}
	c, err := ValidateConfig(shared.Marshal(data), true)
	if err != nil {
		t.Fatal(err)
	}
	p := c.Models["demo"]
	if !p.SupportsImages(p.Model) || !p.SupportsImages("private-vision") || p.SupportsImages("gpt-6-astra") || p.SupportsImages("deepseek-v4-flash") || p.SupportsImages("unknown") || len(p.ModelImageInputs) != 2 {
		t.Fatal("incorrect image capability selection")
	}
	draft := Editable(c)
	draft.Models["demo"].ModelImageInputs["gpt-6-astra"] = true
	if p.SupportsImages("gpt-6-astra") {
		t.Fatal("draft changed saved capability")
	}
	data["models"] = shared.Object{"bad": shared.Object{"protocol": "responses", "model": "m", "modelImageInputs": shared.Object{"m": "yes"}}}
	if _, err := ValidateConfig(shared.Marshal(data), true); err == nil {
		t.Fatal("non-boolean image input setting accepted")
	}
}
