package config

import (
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestLegacyConnectionKeyMigration 验证旧协议和非流式设置不会被编辑覆盖，显式新 Key 可替换旧环境变量来源。
func TestLegacyConnectionKeyMigration(t *testing.T) {
	t.Setenv("API_SUBAGENTS_LEGACY_KEY_TEST", "old-test-key")
	old := Config{Version: 1, Models: map[string]Profile{"legacy": {
		Protocol: "compatible", Model: "demo", BaseURL: "http://127.0.0.1:9/v1",
		APIKeyEnv: "API_SUBAGENTS_LEGACY_KEY_TEST", MaxTokens: 4096,
		FirstTimeout: 180, IdleTimeout: 120, TaskTimeout: 15,
	}}}
	draft := Editable(old)
	p := draft.Models["legacy"]
	p.APIKeyEnv, p.APIKey = "", "new-test-key"
	draft.Models["legacy"] = p
	merged, err := MergeKeys(shared.Marshal(draft), old, true)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveProfile(merged, "legacy")
	if err != nil || resolved.APIKey != "new-test-key" || resolved.APIKeyEnv != "" || resolved.Protocol != "compatible" || resolved.Stream {
		t.Fatal("legacy connection changed unexpectedly", resolved, err)
	}
}

// TestLegacyConcurrentFieldIgnored 确认旧并发数字既不进入新配置也不影响配置读取。
func TestLegacyConcurrentFieldIgnored(t *testing.T) {
	c, err := ValidateConfig([]byte(`{"version":1,"maxConcurrent":1,"models":{}}`), true)
	if err != nil || string(shared.Marshal(c)) != `{"version":1,"models":{}}` {
		t.Fatal("legacy concurrency setting should be discarded", c, err)
	}
}

// TestModelCompatibilityPersistence 验证每模型策略持久化、清理及编辑副本隔离，旧配置不默认开启厂商改写。
func TestModelCompatibilityPersistence(t *testing.T) {
	p := Profile{Protocol: "compatible", Model: "one", RelayModels: []string{"two"}, BaseURL: "http://127.0.0.1/v1", MaxTokens: 4096, FirstTimeout: 30, IdleTimeout: 30, TaskTimeout: 10, ModelCompatibility: map[string]string{"one": "minimax", "two": "auto", "removed": "invalid"}}
	c, err := ValidateConfig(shared.Marshal(Config{Version: 1, Models: map[string]Profile{"test": p}}), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Models["test"].ModelCompatibility) != 1 {
		t.Fatal("stale settings retained")
	}
	p.ModelCompatibility["one"] = "invalid"
	if _, err := ValidateConfig(shared.Marshal(Config{Version: 1, Models: map[string]Profile{"test": p}}), true); err == nil {
		t.Fatal("invalid mode accepted")
	}
	copy := Editable(c)
	copy.Models["test"].ModelCompatibility["one"] = "deepseek"
	if c.Models["test"].ModelCompatibility["one"] != "minimax" {
		t.Fatal("editable copy changed stored mode")
	}
}
