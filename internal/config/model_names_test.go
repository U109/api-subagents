package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestModelNamesValidation 覆盖旧配置、中文名称、清空恢复和错误输入；名称不会改写模型 ID。
func TestModelNamesValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   any
		want    map[string]string
		invalid bool
	}{
		{name: "legacy"},
		{name: "trim", value: map[string]string{"default": " 日常编码 ", "alternate": "代码审查"}, want: map[string]string{"default": "日常编码", "alternate": "代码审查"}},
		{name: "reset", value: map[string]string{"default": "  ", "alternate": "alternate"}},
		{name: "unselected", value: map[string]string{"removed": "旧名称"}},
		{name: "unicode-limit", value: map[string]string{"default": strings.Repeat("名", 80)}, want: map[string]string{"default": strings.Repeat("名", 80)}},
		{name: "too-long", value: map[string]string{"default": strings.Repeat("名", 81)}, invalid: true},
		{name: "control", value: map[string]string{"default": "private-input\x00suffix"}, invalid: true},
		{name: "wrong-type", value: "private-input", invalid: true},
		{name: "wrong-name", value: map[string]int{"default": 10}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ValidateConfig(shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": shared.Object{"protocol": "responses", "model": "default", "relayModels": []string{"alternate"}, "modelNames": tc.value}}}), true)
			if (err != nil) != tc.invalid {
				t.Fatalf("validation error=%v, want invalid=%v", err, tc.invalid)
			}
			if err != nil {
				if strings.Contains(err.Error(), "private-input") {
					t.Fatal("input disclosed in error")
				}
				return
			}
			p := c.Models["demo"]
			if !reflect.DeepEqual(p.ModelNames, tc.want) || p.Model != "default" || p.RelayModels[0] != "alternate" {
				t.Fatal("display names changed model identity or were not normalized")
			}
		})
	}
}

// TestModelNamesPersistence 验证名称保存、读回、改连接名和清空恢复；编辑副本不能污染原配置。
func TestModelNamesPersistence(t *testing.T) {
	c, err := ValidateConfig([]byte(`{"version":1,"models":{"demo":{"protocol":"responses","model":"default","relayModels":["alternate"],"modelNames":{"default":"日常编码","alternate":"深入分析"}}}}`), true)
	if err != nil {
		t.Fatal(err)
	}
	store := NewConfigStore(filepath.Join(t.TempDir(), "models.json"))
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	draft := Editable(saved)
	draft.Models["demo"].ModelNames["default"] = "草稿名称"
	if saved.Models["demo"].ModelName("default") != "日常编码" {
		t.Fatal("draft mutated saved map")
	}
	draft.Models["renamed"] = draft.Models["demo"]
	delete(draft.Models, "demo")
	merged, err := MergeKeys(shared.Marshal(draft), saved, true)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Models["renamed"].ModelName("alternate") != "深入分析" {
		t.Fatal("rename lost model names")
	}
	merged.Models["renamed"].ModelNames["default"] = ""
	if err := store.Save(merged); err != nil {
		t.Fatal(err)
	}
	result, err := store.Read()
	if err != nil || result.Models["renamed"].ModelName("default") != "default" || result.Models["renamed"].ModelName("alternate") != "深入分析" {
		t.Fatal("reset did not persist or affected another model", err)
	}
}
