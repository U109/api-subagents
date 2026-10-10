package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
	toml "github.com/pelletier/go-toml/v2"
)

// TestHistoryProviderBridges 验证活动和归档旧身份补齐、消息完全不变，以及退出后移除临时身份并恢复原路由。
func TestHistoryProviderBridges(t *testing.T) {
	c := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
	original := []byte("model='old'\nmodel_provider='custom'\n[model_providers.custom]\nname='Original'\nbase_url='http://127.0.0.1:9/original'\nrequires_openai_auth=true\n")
	if err := os.WriteFile(c.configPath(), original, 0600); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for path, id := range map[string]string{"sessions/2026/01/01/rollout-active.jsonl": "removed", "archived_sessions/rollout-archived.jsonl": "旧身份.custom"} {
		full := filepath.Join(c.Home, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		data := append(shared.Marshal(shared.Object{"type": "session_meta", "payload": shared.Object{"model_provider": id}}), []byte("\nchat content must not be parsed or rewritten\n")...)
		if err := os.WriteFile(full, data, 0600); err != nil {
			t.Fatal(err)
		}
		files[full] = data
	}
	ids, err := c.historyProviderIDs()
	if err != nil || !reflect.DeepEqual(ids, []string{"removed", "旧身份.custom"}) {
		t.Fatal("historical identities missing", ids, err)
	}
	store, _ := testutil.Config(t, "responses", "http://127.0.0.1:9/mock")
	config, _ := store.Read()
	if err := c.Enable(config, "demo", 12345, "synthetic-local-token"); err != nil {
		t.Fatal(err)
	}
	written, _ := os.ReadFile(c.configPath())
	var parsed shared.Object
	if err := toml.Unmarshal(written, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{providerID, "custom", "removed", "旧身份.custom"} {
		provider := shared.Obj(shared.Obj(parsed["model_providers"])[id])
		if provider["base_url"] != "http://127.0.0.1:12345/v1" || provider["requires_openai_auth"] != false {
			t.Fatal("historical identity was not bridged", id)
		}
	}
	if parsed["openai_base_url"] != "http://127.0.0.1:12345/v1/builtin/synthetic-local-token" {
		t.Fatal("built-in identity was not redirected")
	}
	if err := c.Restore(); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(c.configPath())
	want, _ := withInactiveProvider(original)
	if !sameConfig(restored, want) {
		t.Fatal("original routes were not fully restored")
	}
	for path, before := range files {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("chat record changed", err)
		}
	}
}

// TestUnreadableHistoryDoesNotPatch 验证历史元数据读取失败时既不接管配置也不留下未完成备份。
func TestUnreadableHistoryDoesNotPatch(t *testing.T) {
	c := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
	original := []byte("model='old'\n")
	if err := os.WriteFile(c.configPath(), original, 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(c.Home, "sessions")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rollout-broken.jsonl"), []byte("invalid metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	store, _ := testutil.Config(t, "responses", "http://127.0.0.1:9/mock")
	config, _ := store.Read()
	if err := c.Enable(config, "demo", 12345, "token"); err == nil {
		t.Fatal("history read failure was ignored")
	}
	current, _ := os.ReadFile(c.configPath())
	if !bytes.Equal(current, original) {
		t.Fatal("configuration changed after history read failure")
	}
	if _, err := os.Stat(c.backupPath()); !os.IsNotExist(err) {
		t.Fatal("unexpected pending backup", err)
	}
}
