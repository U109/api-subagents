package config

import (
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestDefaultModelIDValidation 验证可编辑默认 ID 的字符边界，目录查询仍允许尚未选择模型。
func TestDefaultModelIDValidation(t *testing.T) {
	for _, tc := range []struct {
		id              string
		required, valid bool
	}{
		{"  corrected/model  ", true, true},
		{strings.Repeat("模", 200), true, true},
		{strings.Repeat("模", 201), true, false},
		{"private\x00input", true, false},
		{"private\ninput", true, false},
		{"", true, false},
		{"", false, true},
	} {
		c, err := ValidateConfig(shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": shared.Object{"protocol": "responses", "model": tc.id}}}), tc.required)
		if (err == nil) != tc.valid {
			t.Fatalf("model validation mismatch: required=%v valid=%v", tc.required, tc.valid)
		}
		if err == nil && c.Models["demo"].Model != strings.TrimSpace(tc.id) {
			t.Fatal("model ID not normalized")
		}
		if err != nil && strings.Contains(err.Error(), "private") {
			t.Fatal("error disclosed input")
		}
	}
}
