//go:build windows

package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// TestDesktopAccessScopeLifecycle 使用独立 CODEX_HOME 验证真实固定绑定的保存、启用、恢复及删除，不发送付费模型请求。
func TestDesktopAccessScopeLifecycle(t *testing.T) {
	a := closeTestApp(t, "http://127.0.0.1:9/v1")
	c, _ := a.service.Store.Read()
	p := c.Models["demo"]
	p.RelayModels = []string{"only-a", "only-b"}
	c.Models["demo"] = p
	plans := []configstore.AccessConfig{{ID: "a", Name: "配置 A", ConnectionIDs: []string{p.ID}, ModelIDs: map[string][]string{p.ID: {"only-a"}}, DefaultKey: `["demo","only-a"]`}, {ID: "b", Name: "配置 B", ConnectionIDs: []string{p.ID}, ModelIDs: map[string][]string{p.ID: {"only-b"}}, DefaultKey: `["demo","only-b"]`}}
	c.AccessConfigs = plans
	if err := a.service.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnableAccess("a"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(configstore.DataDir(), "codex-model-catalog.json"))
	var catalogue struct {
		Models []shared.Object `json:"models"`
	}
	if err != nil || json.Unmarshal(data, &catalogue) != nil || len(catalogue.Models) != 1 {
		t.Fatal("desktop catalogue not scoped", err)
	}
	updated := *configstore.CloneAccess(&plans[1])
	updated.Name = "新的 B"
	if _, err := a.API("/api/access/save", string(shared.Marshal(shared.Object{"plan": updated}))); err != nil {
		t.Fatal(err)
	}
	if a.relay.Snapshot().Access.ID != "a" {
		t.Fatal("saving B activated it")
	}
	if _, err := a.DisableRelay(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnableAccess(""); err != nil {
		t.Fatal(err)
	}
	if a.relay.Snapshot().Access.ID != "a" {
		t.Fatal("global toggle selected B")
	}
	if _, err := a.API("/api/access/remove", string(shared.Marshal(shared.Object{"id": "a"}))); err != nil {
		t.Fatal(err)
	}
	if state := a.relay.Snapshot(); state.Enabled || state.Access != nil {
		t.Fatal("deleted active config without restoring")
	}
	c, err = a.service.Store.Read()
	if err != nil || c.LastAccess != nil {
		t.Fatal("removed configuration remained as the persisted access target", err)
	}
	if _, err := a.EnableAccess(""); err == nil {
		t.Fatal("global switch resumed a removed configuration")
	}
}
