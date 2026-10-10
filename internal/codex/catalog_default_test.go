package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"

	toml "github.com/pelletier/go-toml/v2"
)

// catalogDefaultFixture 在独立 Codex 目录开启模拟接入，模型排序刻意不同于星标默认值，不访问真实模型服务。
func catalogDefaultFixture(t *testing.T) (CodexConfig, configstore.Config, []byte) {
	t.Helper()
	disk := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
	original := []byte("# original\nmodel = 'original'\napproval_policy = 'never'\n")
	if err := os.WriteFile(disk.configPath(), original, 0600); err != nil {
		t.Fatal(err)
	}
	config := configstore.EmptyConfig()
	config.Models["日常 / 连接"] = configstore.Profile{Model: "default", RelayModels: []string{"manual", "first"}, ModelOrder: []string{"first", "default", "manual"}, APIKey: "synthetic-key", BaseURL: "http://127.0.0.1:9"}
	config.Models["alpha"] = configstore.Profile{Model: "other", APIKey: "synthetic-other-key", BaseURL: "http://127.0.0.1:9"}
	if err := disk.Enable(config, "日常 / 连接", 12345, "synthetic-local-token"); err != nil {
		t.Fatal(err)
	}
	return disk, config, original
}

// catalogDefaultModel 读取隔离目录的实际 TOML 默认值，确保测试不只验证内存中计算的别名。
func catalogDefaultModel(t *testing.T, disk CodexConfig) string {
	t.Helper()
	data, err := os.ReadFile(disk.configPath())
	if err != nil {
		t.Fatal(err)
	}
	var parsed shared.Object
	if err := toml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	return shared.Str(parsed["model"])
}

// TestCatalogUsesStarredDefault 验证不显示额外默认入口，初始选择取星标模型而非目录首项，关闭后精确恢复。
func TestCatalogUsesStarredDefault(t *testing.T) {
	disk, config, original := catalogDefaultFixture(t)
	if got := catalogDefaultModel(t, disk); got != RelayModelAlias("日常 / 连接", "default") {
		t.Fatalf("default model=%q, want starred model", got)
	}
	data, err := os.ReadFile(disk.catalogPath())
	if err != nil {
		t.Fatal(err)
	}
	var catalog shared.Object
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(shared.Arr(catalog["models"])) != 4 || strings.Contains(string(data), "跟随 App") {
		t.Fatal("catalog still advertises an extra default entry")
	}
	entries := ModelEntries(config, "日常 / 连接")
	if entries[0].Slug != RelayModelAlias("日常 / 连接", "default") {
		t.Fatal("catalog default came from saved ordering instead of the star")
	}
	for _, entry := range entries {
		if entry.Slug == relayModel {
			t.Fatal("legacy default alias advertised")
		}
	}
	if err := disk.Restore(); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(disk.configPath())
	want, err := withInactiveProvider(original)
	if err != nil || !bytes.Equal(restored, want) {
		t.Fatal("original selection was not restored")
	}
}

// TestCatalogDefaultRefresh 验证保存默认值、显式切换连接、保留手动模型及移除模型回退，更新后仍可恢复原配置。
func TestCatalogDefaultRefresh(t *testing.T) {
	for _, tc := range []struct {
		name, selected, connection, want string
		force, removeManual              bool
	}{
		{name: "changed-star", connection: "日常 / 连接", want: "next"},
		{name: "manual-selection", selected: "manual", connection: "日常 / 连接", want: "manual"},
		{name: "removed-manual-selection", selected: "manual", connection: "日常 / 连接", want: "next", removeManual: true},
		{name: "switch-connection", selected: "manual", connection: "alpha", want: "other", force: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disk, config, original := catalogDefaultFixture(t)
			current, _ := os.ReadFile(disk.configPath())
			if tc.selected != "" {
				var err error
				current, err = withCatalogDefault(current, RelayModelAlias("日常 / 连接", tc.selected))
				if err != nil {
					t.Fatal(err)
				}
			}
			current = append(current, []byte("\n[features]\nkeep = true\n")...)
			if err := os.WriteFile(disk.configPath(), current, 0600); err != nil {
				t.Fatal(err)
			}
			profile := config.Models["日常 / 连接"]
			profile.Model, profile.RelayModels = "next", []string{"default", "first", "manual"}
			if tc.removeManual {
				profile.RelayModels = []string{"default", "first"}
			}
			config.Models["日常 / 连接"] = profile
			for range 2 {
				if err := disk.RefreshCatalog(config, tc.connection, tc.force); err != nil {
					t.Fatal(err)
				}
				if got := catalogDefaultModel(t, disk); got != RelayModelAlias(tc.connection, tc.want) {
					t.Fatalf("selected model=%q, want %q", got, tc.want)
				}
			}
			if err := disk.Restore(); err != nil {
				t.Fatal(err)
			}
			restored, _ := os.ReadFile(disk.configPath())
			if !bytes.Contains(restored, original) || !bytes.Contains(restored, []byte("keep = true")) || bytes.Contains(restored, []byte("synthetic-local-token")) {
				t.Fatal("refresh broke restore or discarded an unrelated edit")
			}
		})
	}
}

// TestLegacyCatalogDefaultMigration 验证旧虚拟入口仅作为历史路由兼容，刷新时转成真实星标模型且不破坏恢复备份。
func TestLegacyCatalogDefaultMigration(t *testing.T) {
	disk, config, original := catalogDefaultFixture(t)
	backupData, _ := os.ReadFile(disk.backupPath())
	var backup codexBackup
	if err := json.Unmarshal(backupData, &backup); err != nil {
		t.Fatal(err)
	}
	legacy, err := withCatalogDefault(backup.Written, relayModel)
	if err != nil {
		t.Fatal(err)
	}
	backup.Written = legacy
	if err := os.WriteFile(disk.backupPath(), shared.Marshal(backup), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk.configPath(), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if err := disk.RefreshCatalog(config, "日常 / 连接", false); err != nil {
		t.Fatal(err)
	}
	if got := catalogDefaultModel(t, disk); got != RelayModelAlias("日常 / 连接", "default") {
		t.Fatal("legacy default was not migrated")
	}
	if _, profile, err := ResolveCatalogModel(config, "日常 / 连接", relayModel); err != nil || profile.Model != "default" {
		t.Fatal("legacy conversations lost their default route", err)
	}
	if err := disk.Restore(); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(disk.configPath())
	want, err := withInactiveProvider(original)
	if err != nil || !bytes.Equal(restored, want) {
		t.Fatal("migration broke exact restore")
	}
}

// TestCatalogRefreshConflict 验证托管路由被外部改写时不覆盖配置、备份或目录，错误也不泄露本地令牌。
func TestCatalogRefreshConflict(t *testing.T) {
	disk, config, _ := catalogDefaultFixture(t)
	current, _ := os.ReadFile(disk.configPath())
	current = bytes.ReplaceAll(current, []byte("synthetic-local-token"), []byte("edited-token"))
	if err := os.WriteFile(disk.configPath(), current, 0600); err != nil {
		t.Fatal(err)
	}
	backup, _ := os.ReadFile(disk.backupPath())
	catalog, _ := os.ReadFile(disk.catalogPath())
	err := disk.RefreshCatalog(config, "alpha", true)
	if err == nil || strings.Contains(err.Error(), "edited-token") {
		t.Fatal("conflicting route was overwritten or leaked a token")
	}
	for path, want := range map[string][]byte{disk.configPath(): current, disk.backupPath(): backup, disk.catalogPath(): catalog} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(got, want) {
			t.Fatal("conflict changed a managed file", readErr)
		}
	}
}

// TestCatalogRefreshPreservesMissingCredentials 验证目录刷新不因手选模型缺失 Key 而静默改用其他连接，真实请求仍明确拒绝。
func TestCatalogRefreshPreservesMissingCredentials(t *testing.T) {
	t.Setenv("API_SUBAGENTS_TEST_MISSING_KEY", "")
	disk, config, _ := catalogDefaultFixture(t)
	profile := config.Models["alpha"]
	profile.APIKeyEnv = "API_SUBAGENTS_TEST_MISSING_KEY"
	config.Models["alpha"] = profile
	selected := RelayModelAlias("alpha", "other")
	current, _ := os.ReadFile(disk.configPath())
	updated, err := withCatalogDefault(current, selected)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disk.configPath(), updated, 0600); err != nil {
		t.Fatal(err)
	}
	if err := disk.RefreshCatalog(config, "日常 / 连接", false); err != nil {
		t.Fatal(err)
	}
	if got := catalogDefaultModel(t, disk); got != selected {
		t.Fatal("missing credentials silently switched model or connection")
	}
	if _, _, err := ResolveCatalogModel(config, "日常 / 连接", selected); err == nil {
		t.Fatal("missing credentials fell back to a saved key")
	}
}
