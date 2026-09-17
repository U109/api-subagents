package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestRelayModelsValidation 覆盖旧配置、模型 ID 边界和标准化；错误不得带出输入内容或凭据。
func TestRelayModelsValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  []string
		valid bool
	}{
		{"legacy", nil, nil, true},
		{"empty", []string{}, nil, true},
		{"normalized", []string{" model/one ", "模型二", "model/one"}, []string{"model/one", "模型二"}, true},
		{"length-limit", []string{strings.Repeat("模", 200)}, []string{strings.Repeat("模", 200)}, true},
		{"too-long", []string{strings.Repeat("模", 201)}, nil, false},
		{"too-many", make([]string, MaxRelayModels+1), nil, false},
		{"empty-id", []string{"   "}, nil, false},
		{"control", []string{"model\nprivate-input"}, nil, false},
		{"wrong-element", []any{4}, nil, false},
		{"null-element", []any{nil}, nil, false},
		{"wrong-type", "private-input", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := shared.Object{"protocol": "responses", "model": "default", "apiKey": "synthetic-key"}
			if tc.value != nil {
				profile["relayModels"] = tc.value
			}
			c, err := ValidateConfig(shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": profile}}), true)
			if (err == nil) != tc.valid {
				t.Fatalf("validation=%v, want valid=%v", err, tc.valid)
			}
			if err != nil {
				if strings.Contains(err.Error(), "private-input") || strings.Contains(err.Error(), "synthetic-key") {
					t.Fatal("error disclosed input")
				}
				return
			}
			if !reflect.DeepEqual(c.Models["demo"].RelayModels, tc.want) {
				t.Fatal("normalization changed model IDs")
			}
		})
	}
}

// TestRelayModelsPersistence 保存、读取和重命名保留模型列表；编辑副本不能改动原配置的共享切片。
func TestRelayModelsPersistence(t *testing.T) {
	store := NewConfigStore(filepath.Join(t.TempDir(), "models.json"))
	c := EmptyConfig()
	c.Models["demo"] = Profile{Protocol: "responses", Model: "default", RelayModels: []string{"model-a", "model-b"}, APIKey: "synthetic-key", MaxTokens: 4096, FirstTimeout: 180, IdleTimeout: 120, TaskTimeout: 15}
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	draft := Editable(saved)
	draft.Models["demo"].RelayModels[0] = "draft-model"
	if saved.Models["demo"].RelayModels[0] != "model-a" {
		t.Fatal("editing mutated original slice")
	}
	draft = Editable(saved)
	draft.Models["renamed"] = draft.Models["demo"]
	delete(draft.Models, "demo")
	merged, err := MergeKeys(shared.Marshal(draft), saved, true)
	if err != nil || !reflect.DeepEqual(merged.Models["renamed"].RelayModels, []string{"model-a", "model-b"}) || merged.Models["renamed"].APIKey != "synthetic-key" {
		t.Fatal("rename lost models or credentials", err)
	}
	if err = store.Save(merged); err != nil {
		t.Fatal(err)
	}
	reread, err := store.Read()
	if err != nil || !reflect.DeepEqual(reread.Models["renamed"].RelayModels, []string{"model-a", "model-b"}) {
		t.Fatal("save lost list", err)
	}
}
