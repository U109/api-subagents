package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"

	toml "github.com/pelletier/go-toml/v2"
)

// TestCodexRoundTrip 检查多种 TOML 写法的精确恢复，权限、登录设置和注释保持不变。
func TestCodexRoundTrip(t *testing.T) {
	for _, original := range []string{"", "# 我的配置\nmodel = 'original' # keep\nmodel_provider = \"existing\"\napproval_policy = 'never'\n[features]\nfoo = true\n", "\"model\" = '''old-model'''\nmodel_catalog_json = 'C:/models/catalog.json'\n[model_providers.custom]\nbase_url = 'https://example.invalid/v1'\n", "model = \"old\"\r\n# Windows\r\n[sandbox_workspace_write]\r\nnetwork_access = false\r\n"} {
		t.Run(string(shared.Hash([]byte(original)))[:8], func(t *testing.T) {
			patched, err := PatchCodexConfig([]byte(original), "C:/a path/models.json", 12345, "synthetic-local-token")
			if err != nil {
				t.Fatal(err)
			}
			var parsed shared.Object
			if toml.Unmarshal(patched, &parsed) != nil || parsed["model_provider"] != "api_subagents" {
				t.Fatal("bad patch")
			}
			restored, err := RestoreCodexConfig(patched, codexBackup{Original: []byte(original), Written: patched})
			if err != nil || string(restored) != original {
				t.Fatal("round trip failed", err)
			}
		})
	}
}

// TestCodexPreservesExternalChanges 托管期间其他字段被编辑时保留修改，托管字段冲突则保留备份而不覆盖。
func TestCodexPreservesExternalChanges(t *testing.T) {
	original := []byte("model = 'original'\n# retained\n[features]\nfoo = false\n")
	patched, err := PatchCodexConfig(original, "C:/catalog.json", 12345, "token")
	if err != nil {
		t.Fatal(err)
	}
	modified := bytes.Replace(patched, []byte("foo = false"), []byte("foo = true"), 1)
	restored, err := RestoreCodexConfig(modified, codexBackup{Original: original, Written: patched})
	if err != nil || !bytes.Contains(restored, []byte("foo = true")) || !bytes.Contains(restored, []byte("model = 'original'")) || bytes.Contains(restored, []byte("api_subagents")) {
		t.Fatal(string(restored), err)
	}
	conflict := bytes.Replace(patched, []byte("model = \"api-subagents\""), []byte("model = \"edited-by-user\""), 1)
	if _, err = RestoreCodexConfig(conflict, codexBackup{Original: original, Written: patched}); err == nil {
		t.Fatal("owned config conflict overwritten")
	}
}

// TestCodexModelSwitchRestores 模型选择器写回连接别名后仍可正常关闭，并保留新添加的推理设置。
func TestCodexModelSwitchRestores(t *testing.T) {
	original := []byte("model='original'\n[features]\nfoo=true\n")
	written, err := PatchCodexConfig(original, "C:/catalog.json", 12345, "token")
	if err != nil {
		t.Fatal(err)
	}
	current := bytes.Replace(written, []byte(`model = "api-subagents"`), []byte("model = \"api-subagents/demo\"\nmodel_reasoning_effort = \"low\""), 1)
	restored, err := RestoreCodexConfig(current, codexBackup{Original: original, Written: written})
	if err != nil || !bytes.Contains(restored, []byte("model='original'")) || !bytes.Contains(restored, []byte(`model_reasoning_effort = "low"`)) {
		t.Fatal(string(restored), err)
	}
}

// TestCodexBackupAndCatalog 验证开关的实际文件备份、目录隐私和原本不存在的配置恢复。
func TestCodexBackupAndCatalog(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{true: "existing", false: "new"}[exists], func(t *testing.T) {
			home, data := t.TempDir(), t.TempDir()
			c := CodexConfig{Home: home, DataRoot: data}
			original := []byte("# original\nmodel='old'\napproval_policy='never'\n")
			if exists {
				os.WriteFile(filepath.Join(home, "config.toml"), original, 0600)
			}
			store, _ := testutil.Config(t, "compatible", "http://127.0.0.1:9/private")
			config, _ := store.Read()
			if err := c.Enable(config, "demo", 12345, "local-token"); err != nil {
				t.Fatal(err)
			}
			catalog, _ := os.ReadFile(c.catalogPath())
			if strings.Contains(string(catalog), "synthetic-private-key") || strings.Contains(string(catalog), "/private") {
				t.Fatal("catalog contains credentials")
			}
			if err := c.Restore(); err != nil {
				t.Fatal(err)
			}
			restored, err := os.ReadFile(c.configPath())
			if exists {
				if err != nil || !bytes.Equal(restored, original) {
					t.Fatal("backup not restored")
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("new config not removed")
			}
		})
	}
}
