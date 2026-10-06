package codex

import (
	"bytes"
	"os"
	"testing"
)

// TestRepairLegacyHistory 修复缺失或端口 0 的旧入口，保留默认模型、权限和注释，重复启动不重复写入。
func TestRepairLegacyHistory(t *testing.T) {
	c := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
	original := []byte("# preserve\nmodel='original'\napproval_policy='never'\n")
	if err := os.WriteFile(c.configPath(), original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Restore(); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := os.ReadFile(c.configPath())
	if !bytes.Equal(unchanged, original) {
		t.Fatal("never-used installation changed Codex")
	}
	if err := os.WriteFile(c.catalogPath(), []byte(`{"models":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Restore(); err != nil {
		t.Fatal(err)
	}
	want, _ := withInactiveProvider(original)
	first, _ := os.ReadFile(c.configPath())
	if !bytes.Equal(first, want) {
		t.Fatal("legacy provider not repaired")
	}
	if err := c.Restore(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(c.configPath())
	if !bytes.Equal(first, second) {
		t.Fatal("repair not idempotent")
	}
	legacy := append(append([]byte{}, original...), []byte("\n"+legacyInactiveProvider)...)
	if err := os.WriteFile(c.configPath(), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Restore(); err != nil {
		t.Fatal(err)
	}
	upgraded, _ := os.ReadFile(c.configPath())
	if !bytes.Equal(upgraded, want) {
		t.Fatal("legacy port-zero provider was not upgraded")
	}
}

// TestInactiveProviderConflicts 用户改动占位时禁止开启覆盖；已有同名自定义提供商与格式错误的配置也保持原样。
func TestInactiveProviderConflicts(t *testing.T) {
	inactive, _ := withInactiveProvider([]byte("model='original'\n"))
	edited := bytes.Replace(inactive, []byte("requires_openai_auth = true"), []byte("requires_openai_auth = false"), 1)
	for _, data := range [][]byte{edited, append(inactive, []byte(inactiveProvider)...), []byte("[model_providers.api_subagents]\nname='mine'\n"), []byte("bad = [")} {
		before := append([]byte{}, data...)
		if _, err := PatchCodexConfig(data, "C:/catalog.json", 12345, "local-token"); err == nil {
			t.Fatal("conflict overwritten")
		}
		if !bytes.Equal(data, before) {
			t.Fatal("input mutated on failure")
		}
	}
}

// TestInactiveProviderMigration 确认旧占位升级时保留外部配置，开启时只删除原样占位，手动改动仍拒绝覆盖。
func TestInactiveProviderMigration(t *testing.T) {
	original := []byte("model='account-model'\n# keep this\n")
	legacy := append(append([]byte{}, original...), []byte("\n"+legacyInactiveProvider)...)
	upgraded, err := withInactiveProvider(legacy)
	if err != nil || !bytes.Equal(upgraded, append(append([]byte{}, original...), []byte("\n"+inactiveProvider)...)) {
		t.Fatal("legacy placeholder was not upgraded without preserving settings", err)
	}
	if bytes.Contains(upgraded, []byte("127.0.0.1:0")) || !bytes.Contains(upgraded, []byte("requires_openai_auth = true")) || bytes.Contains(upgraded, []byte("base_url")) {
		t.Fatal("inactive provider cannot use Codex login", string(upgraded))
	}
	for _, value := range [][]byte{legacy, upgraded} {
		clean, err := withoutInactiveProvider(value)
		if err != nil || !bytes.Equal(clean, append(append([]byte{}, original...), '\n')) {
			t.Fatal("unmodified placeholder was not removed on enable", err)
		}
	}
	edited := bytes.Replace(legacy, []byte("127.0.0.1:0"), []byte("127.0.0.1:9999"), 1)
	if kept, err := withInactiveProvider(edited); err != nil || !bytes.Equal(kept, edited) {
		t.Fatal("custom provider was changed or blocked closing", err)
	}
	if _, err := withoutInactiveProvider(edited); err == nil {
		t.Fatal("custom provider was overwritten on enable")
	}
}
