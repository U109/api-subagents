package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// accessFixture 创建两个共享连接但模型范围不同的配置，所有凭据只使用测试合成值。
func accessFixture(t *testing.T) Config {
	t.Helper()
	p := Profile{Protocol: "responses", Model: "one", RelayModels: []string{"two"}, BaseURL: "http://127.0.0.1:9/v1", APIKey: "synthetic-only", MaxTokens: 4096, Stream: true, FirstTimeout: 180, IdleTimeout: 120, TaskTimeout: 15}
	c := EmptyConfig()
	c.Models["cpa"], c.Models["worker"], c.Models["review"] = p, p, p
	c, err := ValidateConfig(shared.Marshal(c), true)
	if err != nil {
		t.Fatal(err)
	}
	c.AccessConfigs = []AccessConfig{{ID: "a", Name: "配置 A", ConnectionIDs: []string{"cpa", "worker"}, ModelIDs: map[string][]string{"cpa": {"two"}, "worker": {"one"}}, DefaultKey: `["cpa","two"]`}, {ID: "b", Name: "配置 B", ConnectionIDs: []string{"cpa", "review"}, ModelIDs: map[string][]string{"cpa": {"one"}, "review": {"one"}}, DefaultKey: `["review","one"]`}}
	return c
}

// TestAccessScopeIsIndependent 验证配置默认、子集和空连接选择不会回写或扩大共享连接目录。
func TestAccessScopeIsIndependent(t *testing.T) {
	c := accessFixture(t)
	before := shared.Marshal(c)
	scope, defaultName, err := c.ScopedConfig(c.AccessConfigs[0])
	if err != nil || defaultName != "cpa" || len(scope.Models) != 2 || scope.Models["cpa"].Model != "two" || len(scope.Models["cpa"].RelayModels) != 0 {
		t.Fatal("incorrect scoped catalogue", err)
	}
	if string(before) != string(shared.Marshal(c)) {
		t.Fatal("scope mutated connection defaults")
	}
	p := *CloneAccess(&c.AccessConfigs[0])
	p.ModelIDs["worker"] = nil
	scope, _, err = c.ScopedConfig(p)
	if err != nil || len(scope.Models) != 1 {
		t.Fatal("empty selection expanded", err)
	}
	for _, edit := range []func(*AccessConfig){func(p *AccessConfig) { p.ModelIDs["cpa"] = []string{"missing"} }, func(p *AccessConfig) { p.ConnectionIDs = nil }, func(p *AccessConfig) { p.DefaultKey = `["worker","two"]` }, func(p *AccessConfig) { p.ModelIDs["review"] = []string{"one"} }} {
		p := *CloneAccess(&c.AccessConfigs[0])
		edit(&p)
		if c.ValidateAccess(p) == nil {
			t.Fatal("invalid scope accepted", p)
		}
	}
}

// TestAccessMigrationAndEditable 验证旧配置迁移、显式空配置和脱敏深复制；重命名稳定身份后配置引用仍有效。
func TestAccessMigrationAndEditable(t *testing.T) {
	c := accessFixture(t)
	c.LastAccess = CloneAccess(&c.AccessConfigs[0])
	editable := Editable(c)
	if editable.Models["cpa"].APIKey != "" {
		t.Fatal("credentials leaked")
	}
	editable.AccessConfigs[0].ModelIDs["cpa"][0] = "changed"
	editable.LastAccess.ConnectionIDs[0] = "changed"
	if c.AccessConfigs[0].ModelIDs["cpa"][0] != "two" || c.LastAccess.ConnectionIDs[0] != "cpa" {
		t.Fatal("editable shared access memory")
	}
	c.Models["renamed"] = c.Models["cpa"]
	delete(c.Models, "cpa")
	if err := c.ValidateAccess(c.AccessConfigs[0]); err != nil {
		t.Fatal("rename broke stable reference", err)
	}
	store := NewConfigStore(filepath.Join(t.TempDir(), "models.json"))
	fields := shared.Object{"version": 1, "models": c.Models}
	_ = os.WriteFile(store.Path, shared.Marshal(fields), 0600)
	legacy, err := store.Read()
	if err != nil || len(legacy.AccessConfigs) != 1 || legacy.LastAccess != nil {
		t.Fatal("legacy migration failed", err)
	}
	c.AccessConfigs = []AccessConfig{}
	c.LastAccess = nil
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	empty, err := store.Read()
	if err != nil || len(empty.AccessConfigs) != 0 {
		t.Fatal("explicit empty configuration injected a plan", err)
	}
	c = accessFixture(t)
	c.RemoveAccessConnection("cpa")
	if len(c.AccessConfigs[0].ConnectionIDs) != 1 || c.AccessConfigs[0].DefaultKey != "" {
		t.Fatal("removal did not clean scope")
	}
}

// TestBOMExplicitEmptyAccess 验证 Windows UTF-8 BOM 文件仍区分显式空配置和旧版缺少字段的迁移，避免重新生成已删除配置。
func TestBOMExplicitEmptyAccess(t *testing.T) {
	c := accessFixture(t)
	c.AccessConfigs = []AccessConfig{}
	store := NewConfigStore(filepath.Join(t.TempDir(), "models.json"))
	data := append([]byte{0xef, 0xbb, 0xbf}, shared.Marshal(c)...)
	if err := os.WriteFile(store.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Read()
	if err != nil || len(loaded.AccessConfigs) != 0 || loaded.LastAccess != nil {
		t.Fatal("BOM file injected an access configuration", err)
	}
	legacy := shared.Object{"version": 1, "models": c.Models}
	data = append([]byte{0xef, 0xbb, 0xbf}, shared.Marshal(legacy)...)
	if err := os.WriteFile(store.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Read()
	if err != nil || len(loaded.AccessConfigs) != 1 || loaded.LastAccess != nil {
		t.Fatal("BOM legacy migration did not remain disabled", err)
	}
}

// TestContextOverrideNativeAndUnknown 验证默认容量限幅、显式关闭采用原生、未知不编造、开关持久化及编辑副本隔离。
func TestContextOverrideNativeAndUnknown(t *testing.T) {
	c := accessFixture(t)
	p := c.Models["cpa"]
	p.ModelOfficialContexts = map[string]int{"one": 1000000, "two": 128000}
	p.ModelContextOverrides = map[string]bool{"one": false, "two": true}
	p.ModelContextWindows = map[string]int{"two": 64000}
	c.Models["cpa"] = p
	checked, err := ValidateConfig(shared.Marshal(c), true)
	if err != nil {
		t.Fatal(err)
	}
	p = checked.Models["cpa"]
	if p.ContextWindow("one") != 1000000 || p.ContextWindow("two") != 64000 {
		t.Fatal("context policy ignored")
	}
	p.ModelContextOverrides["unknown"] = false
	if p.ContextWindow("unknown") != 0 {
		t.Fatal("unknown native context was fabricated")
	}
	copy := Editable(checked)
	cp := copy.Models["cpa"]
	cp.ModelContextOverrides["one"] = true
	if checked.Models["cpa"].ModelContextOverrides["one"] {
		t.Fatal("context settings share mutable storage")
	}
	data := shared.Marshal(checked)
	var round Config
	if json.Unmarshal(data, &round) != nil || !reflect.DeepEqual(round.ModelValuesForTest(), checked.ModelValuesForTest()) {
		t.Fatal("round trip lost context settings")
	}
}

// ModelValuesForTest 返回上下文测试所关注的布尔映射，避免比较反序列化产生的空切片差异。
func (c Config) ModelValuesForTest() map[string]bool { return c.Models["cpa"].ModelContextOverrides }
