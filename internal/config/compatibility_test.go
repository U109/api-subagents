package config

import (
	"github.com/U109/api-subagents/internal/shared"
	"testing"
)

// TestModelCompatibilityPersistence 验证每模型策略持久化、清理及编辑副本隔离，旧配置不默认开启厂商改写。
func TestModelCompatibilityPersistence(t *testing.T) {
	p := Profile{Protocol: "compatible", Model: "one", RelayModels: []string{"two"}, BaseURL: "http://127.0.0.1/v1", MaxTokens: 4096, FirstTimeout: 30, IdleTimeout: 30, TaskTimeout: 10, ModelCompatibility: map[string]string{"one": "minimax", "two": "auto", "removed": "invalid"}}
	c, err := ValidateConfig(shared.Marshal(Config{Version: 1, MaxConcurrent: 1, Models: map[string]Profile{"test": p}}), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Models["test"].ModelCompatibility) != 1 {
		t.Fatal("stale settings retained")
	}
	p.ModelCompatibility["one"] = "invalid"
	if _, err := ValidateConfig(shared.Marshal(Config{Version: 1, MaxConcurrent: 1, Models: map[string]Profile{"test": p}}), true); err == nil {
		t.Fatal("invalid mode accepted")
	}
	copy := Editable(c)
	copy.Models["test"].ModelCompatibility["one"] = "deepseek"
	if c.Models["test"].ModelCompatibility["one"] != "minimax" {
		t.Fatal("editable copy changed stored mode")
	}
}
