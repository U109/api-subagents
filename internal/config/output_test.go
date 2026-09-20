package config

import (
	"path/filepath"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestModelOutputSources 验证型号、平台、接口及显式选择分别决定预算，未知型号不套用上下文容量。
func TestModelOutputSources(t *testing.T) {
	for _, tc := range []struct {
		model, mode, base, protocol string
		tokens, want                int
	}{
		{"deepseek-v4-flash", "", "", "responses", 0, 384000},
		{"gemini-3.8-flash-high", "", "", "responses", 0, 65536},
		{"gpt-6-astra", "", "", "responses", 0, 128000},
		{"kimi-k3", "", "", "responses", 0, 0},
		{"kimi-k3", "volcengine", "", "responses", 0, 128000},
		{"kimi-k2.7-code", "", "https://ark.cn-beijing.volces.com/api/coding/v3", "responses", 0, 32000},
		{"kimi-k3", "", "https://ark.cn-beijing.volces.com.evil.test/api/coding", "responses", 0, 0},
		{"deepseek-v4-flash", "upstream", "", "responses", 0, 0},
		{"unknown", "", "", "responses", 0, 0},
		{"unknown", "custom", "", "responses", 12345, 12345},
		{"deepseek-v4-flash", "custom", "", "compatible", 32768, 0},
	} {
		p := Profile{Protocol: tc.protocol, BaseURL: tc.base, ModelOutputs: map[string]ModelOutputSetting{tc.model: {Mode: tc.mode, Tokens: tc.tokens}}}
		if got := p.OutputLimit(tc.model); got != tc.want {
			t.Errorf("%+v: got %d", tc, got)
		}
	}
	copy := OfficialModelOutputs()
	delete(copy["official"], "deepseek-v4-flash")
	if (Profile{Protocol: "responses"}).OutputLimit("deepseek-v4-flash") != 384000 {
		t.Fatal("metadata mutated defaults")
	}
}

// TestModelOutputValidationAndPersistence 验证非法额度被拒绝、删除模型清理、编辑副本隔离及保存读回。
func TestModelOutputValidationAndPersistence(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		tokens any
		valid  bool
	}{
		{"official", 0, true}, {"upstream", 0, true}, {"volcengine", 0, true}, {"custom", 128, true}, {"custom", 1000000, true},
		{"custom", 127, false}, {"custom", 1000001, false}, {"custom", 1.5, false}, {"custom", "32000", false},
		{"upstream", 32000, false}, {"official", 1, false}, {"invalid", 0, false},
	} {
		raw := shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": shared.Object{"protocol": "responses", "model": "one", "relayModels": []string{"two"}, "modelOutputs": shared.Object{"one": shared.Object{"mode": tc.mode, "tokens": tc.tokens}, "two": shared.Object{"mode": "custom", "tokens": 8192}, "removed": shared.Object{"mode": "custom", "tokens": 12345}}}}})
		c, err := ValidateConfig(raw, true)
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
		if !tc.valid {
			continue
		}
		if _, ok := c.Models["demo"].ModelOutputs["removed"]; ok {
			t.Fatal("removed model retained")
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
		draft.Models["demo"].ModelOutputs["two"] = ModelOutputSetting{Mode: "custom", Tokens: 9999}
		if saved.Models["demo"].OutputLimit("two") != 8192 {
			t.Fatal("draft mutated saved output")
		}
		merged, err := MergeKeys(shared.Marshal(draft), saved, true)
		if err != nil || merged.Models["demo"].OutputLimit("two") != 9999 {
			t.Fatal("merge lost output", err)
		}
	}
}
