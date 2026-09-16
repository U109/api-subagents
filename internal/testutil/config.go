package testutil

import (
	"path/filepath"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// testConfig 用合成 Key 创建隔离配置，所有测试均不调用真实模型服务。
func Config(t *testing.T, protocol, base string) (*configstore.ConfigStore, configstore.Profile) {
	t.Helper()
	store := configstore.NewConfigStore(filepath.Join(t.TempDir(), "models.json"))
	c, err := configstore.ValidateConfig(shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": shared.Object{"protocol": protocol, "model": "mock-model", "baseUrl": base, "apiKey": "synthetic-private-key"}}}), true)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(c); err != nil {
		t.Fatal(err)
	}
	return store, c.Models["demo"]
}
