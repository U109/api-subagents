package codex

import (
	"bytes"
	"os"
	"testing"
)

// TestRepairLegacyHistory 修复旧版遗留入口时不改默认模型、权限和注释，并且重复启动不重复写占位。
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
}

// TestInactiveProviderConflicts 用户改动占位时禁止开启覆盖；已有同名自定义提供商与格式错误的配置也保持原样。
func TestInactiveProviderConflicts(t *testing.T) {
	inactive, _ := withInactiveProvider([]byte("model='original'\n"))
	edited := bytes.Replace(inactive, []byte("127.0.0.1:0"), []byte("127.0.0.1:9999"), 1)
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
