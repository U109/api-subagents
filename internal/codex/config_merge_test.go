package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
	toml "github.com/pelletier/go-toml/v2"
)

// TestRestoreIgnoresFormattingAndMarkers 验证注释损坏、格式重写和多行字符串不再阻止无歧义恢复。
func TestRestoreIgnoresFormattingAndMarkers(t *testing.T) {
	original := []byte("model='old'\nmodel_provider='custom'\nmodel_context_window=8192\n# retain comment\n[features]\nfoo=false\n[model_providers.custom]\nname='Original'\nbase_url='https://example.invalid/v1'\nenv_key='TEST_KEY'\n")
	written, err := PatchCodexConfig(original, "C:/catalog.json", 12345, "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"missing", "duplicate", "crlf", "reformatted", "renamed", "moved-root", "string-marker", "forged-preserved"} {
		t.Run(change, func(t *testing.T) {
			current := bytes.ReplaceAll(written, []byte("foo=false"), []byte("foo=true"))
			want := bytes.ReplaceAll(original, []byte("foo=false"), []byte("foo=true"))
			switch change {
			case "missing":
				current = stripConfigMarkers(current, []string{defaultsBegin, defaultsEnd, providerBegin, providerEnd}, true)
			case "duplicate":
				current = bytes.ReplaceAll(current, []byte(providerEnd), []byte(providerBegin))
			case "crlf":
				current = bytes.ReplaceAll(current, []byte("\n"), []byte("\r\n"))
			case "reformatted":
				var parsed shared.Object
				_ = toml.Unmarshal(current, &parsed)
				current, err = toml.Marshal(parsed)
				if err != nil {
					t.Fatal(err)
				}
			case "renamed":
				current = bytes.ReplaceAll(current, []byte("API Subagents · 挟持模式"), []byte("My display name"))
			case "moved-root":
				current = bytes.Replace(current, []byte(`model = "api-subagents"`+"\n"), nil, 1)
				current = append([]byte("model = 'api-subagents/demo'\n"), current...)
			case "string-marker":
				text := "description = '''\n" + providerBegin + "\n" + preservedPrefix + "not-base64\n'''\n"
				current = append([]byte(text), current...)
				want = append([]byte(text), want...)
			case "forged-preserved":
				current = bytes.ReplaceAll(current, []byte(preservedPrefix), []byte(preservedPrefix+"corrupted"))
			}
			before := append([]byte{}, current...)
			restored, err := RestoreCodexConfig(current, codexBackup{Original: original, Written: written})
			if err != nil || !sameConfig(restored, want) {
				t.Fatal("safe merge failed", err)
			}
			if !bytes.Equal(current, before) {
				t.Fatal("merge mutated input")
			}
			if change != "reformatted" && !bytes.Contains(restored, []byte("# retain comment")) {
				t.Fatal("unrelated comment lost")
			}
		})
	}
}

// TestRestoreDetachedProvider 重现独立提供商切换、缺失 END 和移动恢复注释的组合，避免 notify 重复或丢失新选择。
func TestRestoreDetachedProvider(t *testing.T) {
	original := []byte("model='old'\nmodel_provider='custom'\nnotify=['old-root']\n[model_providers.custom]\nname='Original'\nbase_url='https://original.invalid/v1'\nnotify=['provider-local']\n[features]\nfoo=false\n")
	written, err := PatchCodexConfig(original, "C:/catalog.json", 12345, "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	current := bytes.Replace(written, []byte(`model = "api-subagents"`), []byte(`model = "new-model"`), 1)
	current = bytes.Replace(current, []byte(`model_provider = "api_subagents"`), []byte(`model_provider = "independent"`), 1)
	current = bytes.Replace(current, []byte(`model_catalog_json = "C:/catalog.json"`), []byte(`# catalog disabled`), 1)
	spans, _ := providerSpans(current, "custom")
	current, _ = takeConfigSpans(current, spans)
	current = append(current, []byte("\n"+providerBegin+"\n[model_providers.independent]\nname='New'\nbase_url='https://new.invalid/v1'\n")...)
	lines := bytes.SplitAfter(current, []byte("\n"))
	for i := 1; i < len(lines); i++ {
		if bytes.HasPrefix(lines[i], []byte(preservedPrefix)) && bytes.HasPrefix(lines[i-1], []byte("notify=")) {
			lines[i-1], lines[i] = lines[i], []byte("notify=['new-root']\n")
			break
		}
	}
	current = bytes.Join(lines, nil)
	restored, err := RestoreCodexConfig(current, codexBackup{Original: original, Written: written})
	if err != nil {
		t.Fatal(err)
	}
	var parsed shared.Object
	if err := toml.Unmarshal(restored, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["model"] != "new-model" || parsed["model_provider"] != "independent" || parsed["model_catalog_json"] != nil {
		t.Fatal("independent selection lost")
	}
	if shared.Arr(parsed["notify"])[0] != "new-root" || shared.Arr(shared.Obj(shared.Obj(parsed["model_providers"])["custom"])["notify"])[0] != "provider-local" {
		t.Fatal("notify changed scope")
	}
	if bytes.Contains(restored, []byte("synthetic-token")) || bytes.Contains(restored, []byte(providerBegin)) || bytes.Contains(restored, []byte(preservedPrefix)) {
		t.Fatal("managed state leaked into restored config")
	}
}

// TestRestoreConflictReportsField 真正的路由、协议和凭据改动须保留原文件，并只输出字段名。
func TestRestoreConflictReportsField(t *testing.T) {
	original := []byte("model='old'\n")
	written, _ := PatchCodexConfig(original, "C:/catalog.json", 12345, "synthetic-token")
	for _, tc := range []struct{ old, value, field string }{
		{`base_url = "http://127.0.0.1:12345/v1"`, `base_url = "http://127.0.0.1:55555/v1"`, "base_url"},
		{`wire_api = "responses"`, `wire_api = "changed"`, "wire_api"},
		{`"X-Api-Subagents-Token" = "synthetic-token"`, `"X-Api-Subagents-Token" = "new-private-token"`, "http_headers"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			current := bytes.Replace(written, []byte(tc.old), []byte(tc.value), 1)
			before := append([]byte{}, current...)
			_, err := RestoreCodexConfig(current, codexBackup{Original: original, Written: written})
			if err == nil || !strings.Contains(err.Error(), "model_providers.api_subagents."+tc.field) || strings.Contains(err.Error(), tc.value) {
				t.Fatal("missing or sensitive conflict diagnostic", err)
			}
			if !bytes.Equal(current, before) {
				t.Fatal("conflicting input modified")
			}
		})
	}
}

// TestInactiveSemanticCompatibility 新旧占位跨格式兼容，标记可缺失或重复；额外路由和凭据字段不被当成已知占位。
func TestInactiveSemanticCompatibility(t *testing.T) {
	for _, source := range []string{inactiveProvider, legacyInactiveProvider} {
		for _, variant := range []string{"crlf", "missing", "duplicate", "reformatted", "renamed"} {
			t.Run(shared.Hash([]byte(source))[:6]+variant, func(t *testing.T) {
				current := []byte("model='old'\n\n" + source)
				switch variant {
				case "crlf":
					current = bytes.ReplaceAll(current, []byte("\n"), []byte("\r\n"))
				case "missing":
					current = stripConfigMarkers(current, []string{inactiveBegin, inactiveEnd}, false)
				case "duplicate":
					current = bytes.ReplaceAll(current, []byte(inactiveEnd), []byte(inactiveBegin))
				case "reformatted":
					var parsed shared.Object
					_ = toml.Unmarshal(current, &parsed)
					current, _ = toml.Marshal(parsed)
				case "renamed":
					current = bytes.ReplaceAll(current, []byte(`name = "OpenAI"`), []byte(`name = 'Renamed'`))
				}
				clean, err := withoutInactiveProvider(current)
				if err != nil || !sameConfig(clean, []byte("model='old'\n")) {
					t.Fatal("known placeholder blocked enable", err)
				}
				upgraded, err := withInactiveProvider(current)
				if err != nil || bytes.Contains(upgraded, []byte("127.0.0.1:0")) {
					t.Fatal("known legacy placeholder not upgraded", err)
				}
				if _, err := PatchCodexConfig(current, "C:/catalog.json", 12345, "local"); err != nil {
					t.Fatal("enable failed after semantic cleanup", err)
				}
			})
		}
	}
	custom := bytes.Replace([]byte(inactiveProvider), []byte(inactiveEnd), []byte("env_key='PRIVATE_KEY'\n"+inactiveEnd), 1)
	if _, err := withoutInactiveProvider(custom); err == nil {
		t.Fatal("custom credential was overwritten")
	}
	if kept, err := withInactiveProvider(custom); err != nil || !bytes.Equal(kept, custom) {
		t.Fatal("custom inactive provider blocked exit or was modified", err)
	}
}

// TestRestoreRecoverySnapshot 修复成功后保留原备份和修复前配置的快照，所有文件只写入临时 CODEX_HOME 与数据目录。
func TestRestoreRecoverySnapshot(t *testing.T) {
	c := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
	t.Setenv("CODEX_HOME", c.Home)
	original := []byte("model='old'\n[features]\nfoo=false\n")
	written, _ := PatchCodexConfig(original, c.catalogPath(), 12345, "synthetic-token")
	current := bytes.Replace(written, []byte(providerEnd), []byte(providerBegin), 1)
	current = bytes.Replace(current, []byte("foo=false"), []byte("foo=true"), 1)
	backup := codexBackup{Original: original, Written: written, Existed: true}
	if err := os.WriteFile(c.configPath(), current, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.backupPath(), shared.Marshal(backup), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.backupPath()); !os.IsNotExist(err) {
		t.Fatal("active backup still blocks next enable")
	}
	files, err := filepath.Glob(filepath.Join(c.DataRoot, "codex-recovery", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("recovery snapshot missing", err)
	}
	data, _ := os.ReadFile(files[0])
	var saved struct {
		Current []byte      `json:"current"`
		Backup  codexBackup `json:"backup"`
	}
	if json.Unmarshal(data, &saved) != nil || !bytes.Equal(saved.Current, current) || !bytes.Equal(saved.Backup.Original, original) || !bytes.Equal(saved.Backup.Written, written) {
		t.Fatal("recovery snapshot incomplete")
	}
	if err := c.Restore(); err != nil {
		t.Fatal("repeat restore failed", err)
	}
}

// TestInactiveRepairKeepsCustomProvider 未启用网关时，用户自定义占位不阻止退出；开启时仍防止同名覆盖。
func TestInactiveRepairKeepsCustomProvider(t *testing.T) {
	c := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
	t.Setenv("CODEX_HOME", c.Home)
	custom := []byte("model='own-model'\nmodel_provider='api_subagents'\n" + inactiveBegin + "\n[model_providers.api_subagents]\nname='Mine'\nbase_url='https://custom.invalid/v1'\nenv_key='TEST_KEY'\n" + inactiveEnd + "\n")
	if err := os.WriteFile(c.catalogPath(), []byte(`{"models":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.configPath(), custom, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Restore(); err != nil {
		t.Fatal("custom placeholder blocks exit", err)
	}
	current, _ := os.ReadFile(c.configPath())
	if !bytes.Equal(current, custom) {
		t.Fatal("custom provider changed")
	}
	if _, err := PatchCodexConfig(current, c.catalogPath(), 12345, "test-token"); err == nil {
		t.Fatal("custom provider overwritten on enable")
	}
}

// TestMarkerTextInsideString 多行字符串里的标记不能触发恢复冲突，也不能被清理逻辑删除。
func TestMarkerTextInsideString(t *testing.T) {
	original := []byte("model='original'\ndescription='''\n" + providerBegin + "\n" + inactiveBegin + "\n" + preservedPrefix + "user text\n'''\n")
	written, err := PatchCodexConfig(original, "C:/catalog.json", 12345, "token")
	if err != nil {
		t.Fatal("string contents mistaken for managed state", err)
	}
	current := append([]byte("model_reasoning_effort='low'\n"), written...)
	restored, err := RestoreCodexConfig(current, codexBackup{Original: original, Written: written})
	if err != nil || !sameConfig(restored, append([]byte("model_reasoning_effort='low'\n"), original...)) {
		t.Fatal("string contents changed during restore", err)
	}
}
