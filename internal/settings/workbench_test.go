package settings

import (
	"context"
	"os"
	"strings"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
)

// TestSaveOneConnection 只保存目标草稿，忽略其他未完成连接；改名追溯凭据、失败保持磁盘不变且响应不含 Key。
func TestSaveOneConnection(t *testing.T) {
	store, p := testutil.Config(t, "responses", "http://127.0.0.1:9/v1")
	service := NewConfigService(store)
	p.Description = "saved-other"
	c, _ := store.Read()
	c.Models["other"] = p
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	draft := configstore.Editable(c)
	target := draft.Models["demo"]
	target.Description = "target-only"
	off := false
	target.AdvancedOverride, target.MaxTokens = &off, 9000
	draft.Models["demo"] = target
	// 其他草稿故意无效；定点保存不得把它验证、持久化或覆盖回 UI。
	draft.Models["other"] = configstore.Profile{}
	draft.Models["incomplete"] = configstore.Profile{}
	result, err := service.Handle(context.Background(), "/api/config/save", shared.Marshal(shared.Object{"name": "demo", "config": draft}))
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := store.Read()
	if len(saved.Models) != 2 || saved.Models["other"].Description != "saved-other" || saved.Models["demo"].Description != "target-only" || saved.Models["demo"].APIKey != p.APIKey || saved.Models["demo"].MaxTokens != 9000 || *saved.Models["demo"].AdvancedOverride {
		t.Fatal("target save affected another draft, lost credentials or raw parameters")
	}
	if strings.Contains(string(shared.Marshal(result)), p.APIKey) {
		t.Fatal("save response leaked credentials")
	}
	rename := configstore.Editable(saved)
	rename.Models["renamed"] = rename.Models["demo"]
	delete(rename.Models, "demo")
	if _, err = service.Handle(context.Background(), "/api/config/save", shared.Marshal(shared.Object{"name": "renamed", "config": rename})); err != nil {
		t.Fatal(err)
	}
	saved, _ = store.Read()
	if _, exists := saved.Models["demo"]; exists || saved.Models["renamed"].APIKey != p.APIKey || len(saved.Models) != 2 {
		t.Fatal("rename did not retain exactly one credential source")
	}
	before, _ := os.ReadFile(store.Path)
	for _, name := range []string{"missing", "other"} {
		bad := configstore.Editable(saved)
		bad.Models["other"] = bad.Models["renamed"]
		if _, err = service.Handle(context.Background(), "/api/config/save", shared.Marshal(shared.Object{"name": name, "config": bad})); err == nil {
			t.Fatal("missing/colliding target accepted", name)
		}
		after, _ := os.ReadFile(store.Path)
		if string(before) != string(after) {
			t.Fatal("failed save changed persisted config")
		}
	}
}
