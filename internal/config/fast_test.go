package config

import (
	"path/filepath"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestModelFastPersistence 验证开关按模型保存、清理孤立项、默认关闭及脱敏副本隔离，不接触用户配置。
func TestModelFastPersistence(t *testing.T) {
	c, err := ValidateConfig(shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": shared.Object{"protocol": "responses", "model": "slow", "relayModels": []string{"fast-named"}, "modelFastModes": shared.Object{"slow": true, "fast-named": false, "removed": true}}}}), true)
	if err != nil {
		t.Fatal(err)
	}
	p := c.Models["demo"]
	if !p.FastMode() || len(p.ModelFastModes) != 1 {
		t.Fatal(p.ModelFastModes)
	}
	p.Model = "fast-named"
	if p.FastMode() {
		t.Fatal("model name implicitly enabled Fast")
	}
	store := NewConfigStore(filepath.Join(t.TempDir(), "models.json"))
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Read()
	if err != nil || !loaded.Models["demo"].FastMode() {
		t.Fatal(err, loaded)
	}
	edited := Editable(loaded)
	edited.Models["demo"].ModelFastModes["slow"] = false
	if !loaded.Models["demo"].FastMode() {
		t.Fatal("editable Fast map shared with saved configuration")
	}
}

// TestFastProtocolAndLegacyEffort 拒绝不支持的原生协议开关，并让旧 Ultra 配置安全迁移到 Max。
func TestFastProtocolAndLegacyEffort(t *testing.T) {
	for _, protocol := range []string{"responses", "compatible", "anthropic", "gemini"} {
		c, err := ValidateConfig(shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": shared.Object{"protocol": protocol, "model": "mock", "reasoningEffort": "ultra", "modelFastModes": shared.Object{"mock": true}}}}), true)
		if protocol == "gemini" {
			if err == nil {
				t.Fatal("unsupported native Fast accepted")
			}
			continue
		}
		if err != nil || c.Models["demo"].ReasoningEffort != "max" {
			t.Fatal(err, c)
		}
	}
	if ValidReasoningEffort("ultra") {
		t.Fatal("Ultra remains a selectable effort")
	}
}
