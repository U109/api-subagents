package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// TestHijackCatalogMappings 验证目录去重、跨连接唯一性、思考等级和密钥隔离；列表重排不改变模型别名。
func TestHijackCatalogMappings(t *testing.T) {
	c := configstore.EmptyConfig()
	for _, name := range []string{"alpha", "beta"} {
		c.Models[name] = configstore.Profile{Model: "default", RelayModels: []string{"vendor/模型", "other", "default", "other"}, APIKey: "synthetic-key-" + name, BaseURL: "https://private.invalid", ReasoningEffort: "low"}
	}
	entries := ModelEntries(c, "beta")
	if len(entries) != 7 || entries[0].Slug != relayModel {
		t.Fatal("missing or duplicate catalog entries")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if seen[entry.Slug] || entry.ReasoningEffort != "low" {
			t.Fatal("collision or effort lost")
		}
		seen[entry.Slug] = true
		name, p, err := ResolveCatalogModel(c, "beta", entry.Slug)
		if err != nil || p.APIKey != "synthetic-key-"+name {
			t.Fatal("wrong credential routing", err)
		}
	}
	if RelayModelAlias("alpha", "vendor/模型") == RelayModelAlias("beta", "vendor/模型") {
		t.Fatal("connections share alias")
	}
	p := c.Models["alpha"]
	p.RelayModels = []string{"other", "vendor/模型"}
	c.Models["alpha"] = p
	_, resolved, err := ResolveCatalogModel(c, "beta", entries[2].Slug)
	if err != nil || resolved.Model != "vendor/模型" || c.Models["alpha"].Model != "default" {
		t.Fatal("reordering changed route or default", err)
	}
	disk := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
	if err = disk.WriteCatalog(c, "beta"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(disk.catalogPath())
	if err != nil || strings.Contains(string(data), "synthetic-key") || strings.Contains(string(data), "private.invalid") {
		t.Fatal("catalog privacy failure", err)
	}
	var parsed shared.Object
	if json.Unmarshal(data, &parsed) != nil || len(shared.Arr(parsed["models"])) != 7 {
		t.Fatal("catalog unusable")
	}
}

// TestHijackSelectionGuards 未配置、已删除和畸形别名不得覆盖上游模型；旧连接别名和跟随 App 保持可用。
func TestHijackSelectionGuards(t *testing.T) {
	c := configstore.EmptyConfig()
	c.Models["demo"] = configstore.Profile{Model: "default", RelayModels: []string{"alternate"}, APIKey: "synthetic-key"}
	for _, alias := range []string{"", relayModel, relayModel + "/demo", RelayModelAlias("demo", "default"), RelayModelAlias("demo", "alternate")} {
		name, _, err := ResolveCatalogModel(c, "demo", alias)
		if err != nil || name != "demo" {
			t.Fatal("supported alias rejected", alias, err)
		}
	}
	for _, alias := range []string{"other", relayModel + "/", relayModel + "/demo/", relayModel + "/demo/alternate", relayModel + "/demo/../other", RelayModelAlias("missing", "alternate"), RelayModelAlias("demo", "removed")} {
		if _, _, err := ResolveCatalogModel(c, "demo", alias); err == nil {
			t.Fatal("unknown alias accepted", alias)
		}
	}
	p := c.Models["demo"]
	p.RelayModels = nil
	c.Models["demo"] = p
	if _, _, err := ResolveCatalogModel(c, "demo", RelayModelAlias("demo", "alternate")); err == nil {
		t.Fatal("removed model still accepted")
	}
	// Codex 在新别名间切换后关闭模式，仍恢复原来的默认模型和提供商。
	disk := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
	if err := disk.Enable(c, "demo", 12345, "synthetic-local-token"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(disk.Home, "config.toml")
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	current = []byte(strings.Replace(string(current), `model = "api-subagents"`, `model = "`+RelayModelAlias("demo", "alternate")+`"`, 1))
	if err := os.WriteFile(path, current, 0600); err != nil {
		t.Fatal(err)
	}
	if err := disk.Restore(); err != nil {
		t.Fatal("new alias broke restore", err)
	}
}
