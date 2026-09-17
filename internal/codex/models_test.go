package codex

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// TestFreeTextConnectionRouting 验证自由名称进入目录后可准确恢复，斜线与转义文本不能串到另一连接。
func TestFreeTextConnectionRouting(t *testing.T) {
	c := configstore.EmptyConfig()
	names := []string{"demo", "日常助手 / Gemini #1", "a/b", "a%2Fb", "a%252Fb", "__proto__", "constructor", "123", strings.Repeat("长名称", 40)}
	for _, name := range names {
		c.Models[name] = configstore.Profile{Model: "default", RelayModels: []string{"vendor/extra"}, APIKey: "synthetic-" + name}
	}
	entries := ModelEntries(c, "demo")
	if len(entries) != 1+2*len(names) {
		t.Fatal("free-text connection missing from catalog")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if seen[entry.Slug] {
			t.Fatal("free-text connection alias collision")
		}
		seen[entry.Slug] = true
		name, profile, err := ResolveCatalogModel(c, "demo", entry.Slug)
		if err != nil || profile.APIKey != "synthetic-"+name {
			t.Fatal("catalog routed to wrong connection", err)
		}
		if entry.Slug != relayModel && entry.Name != name+" · "+profile.Model {
			t.Fatal("catalog label and upstream model disagree")
		}
	}
	for _, name := range names {
		alias := relayModel + "/" + url.PathEscape(name)
		resolved, profile, err := ResolveCatalogModel(c, "demo", alias)
		if err != nil || resolved != name || profile.Model != "default" || profile.APIKey != "synthetic-"+name {
			t.Fatal("connection alias decoded incorrectly", err)
		}
	}
	if !seen[relayModel+"/demo"] {
		t.Fatal("legacy connection alias changed")
	}
	for _, alias := range []string{relayModel + "/bad%escape", relayModel + "/demo%", relayModel + "/a%2Fb/not-configured"} {
		if _, _, err := ResolveCatalogModel(c, "demo", alias); err == nil {
			t.Fatal("malformed alias accepted")
		}
	}
}

// TestModelDisplayNames 通过公开目录和解析入口确认改名只改变显示，历史别名与上游模型仍稳定。
func TestModelDisplayNames(t *testing.T) {
	c := configstore.EmptyConfig()
	c.Models["demo"] = configstore.Profile{Model: "vendor/default", RelayModels: []string{"vendor/extra"}, APIKey: "synthetic-key"}
	before := ModelEntries(c, "demo")
	p := c.Models["demo"]
	p.ModelNames = map[string]string{"vendor/default": "日常编码", "vendor/extra": "深入分析"}
	c.Models["demo"] = p
	after := ModelEntries(c, "demo")
	if after[1].Name != "demo · 日常编码" || after[2].Name != "demo · 深入分析" {
		t.Fatal("display names absent from catalog")
	}
	for index, entry := range after {
		if entry.Slug != before[index].Slug {
			t.Fatal("rename changed persistent alias")
		}
		_, resolved, err := ResolveCatalogModel(c, "demo", entry.Slug)
		model := "vendor/default"
		if index == 2 {
			model = "vendor/extra"
		}
		if err != nil || resolved.Model != model || resolved.APIKey != "synthetic-key" {
			t.Fatal("display name leaked into routing", err)
		}
	}
	disk := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
	if err := disk.WriteCatalog(c, "demo"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(disk.catalogPath())
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []struct {
			DisplayName string `json:"display_name"`
		} `json:"models"`
	}
	if json.Unmarshal(data, &catalog) != nil || len(catalog.Models) != 3 || catalog.Models[2].DisplayName != "demo · 深入分析" {
		t.Fatal("Codex catalog omitted model name")
	}
}

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
