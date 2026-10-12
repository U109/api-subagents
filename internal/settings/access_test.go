package settings

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
)

// savedAccessFixture 提供隔离服务及一份带两个模型的配置，不发送模型请求。
func savedAccessFixture(t *testing.T) (*ConfigService, configstore.AccessConfig) {
	t.Helper()
	store, p := testutil.Config(t, "responses", "http://127.0.0.1:9/v1")
	p.ID = "demo"
	p.RelayModels = []string{"second"}
	c, _ := store.Read()
	c.Models["demo"] = p
	plan := configstore.AccessConfig{ID: "a", Name: "配置 A", ConnectionIDs: []string{"demo"}, ModelIDs: map[string][]string{"demo": {"mock-model"}}, DefaultKey: `["demo","mock-model"]`}
	c.AccessConfigs = []configstore.AccessConfig{plan}
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	return NewConfigService(store), plan
}

// TestAccessSaveAndResumeSnapshot 验证仅保存不改变上次生效范围，关闭重开使用旧范围而非新保存的同名配置。
func TestAccessSaveAndResumeSnapshot(t *testing.T) {
	s, plan := savedAccessFixture(t)
	if err := s.ActivateAccess(plan.ID, func(c configstore.Config, p configstore.AccessConfig) error { return c.ValidateAccess(p) }); err != nil {
		t.Fatal(err)
	}
	old := *configstore.CloneAccess(&plan)
	plan.ModelIDs["demo"] = []string{"second"}
	plan.DefaultKey = `["demo","second"]`
	if _, err := s.Handle(context.Background(), "/api/access/save", shared.Marshal(shared.Object{"plan": plan})); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Store.Read()
	if !reflect.DeepEqual(*c.LastAccess, old) || !reflect.DeepEqual(c.AccessConfigs[0], plan) {
		t.Fatal("save altered running snapshot")
	}
	if err := s.ActivateAccess("", func(c configstore.Config, p configstore.AccessConfig) error {
		if !reflect.DeepEqual(p, old) {
			t.Fatal("resume used newly saved scope")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateAccess(plan.ID, func(configstore.Config, configstore.AccessConfig) error { return errors.New("synthetic failed apply") }); err == nil {
		t.Fatal("failure hidden")
	}
	c, _ = s.Store.Read()
	if !reflect.DeepEqual(*c.LastAccess, old) {
		t.Fatal("failed apply changed remembered snapshot")
	}
}

// TestAccessRemovalRecoveryAndRename 验证恢复冲突阻止删除，连接改名保持配置身份，复制不会重用旧身份。
func TestAccessRemovalRecoveryAndRename(t *testing.T) {
	s, plan := savedAccessFixture(t)
	s.BeforeRemoveAccess = func(string) error { return errors.New("synthetic restore conflict") }
	if _, err := s.Handle(context.Background(), "/api/access/remove", shared.Marshal(shared.Object{"id": plan.ID})); err == nil {
		t.Fatal("removed despite recovery conflict")
	}
	c, _ := s.Store.Read()
	draft := configstore.Editable(c)
	draft.Models["renamed"] = draft.Models["demo"]
	delete(draft.Models, "demo")
	if _, err := s.Handle(context.Background(), "/api/config/save", shared.Marshal(shared.Object{"name": "renamed", "config": draft})); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Store.Read()
	if err := c.ValidateAccess(plan); err != nil {
		t.Fatal("rename broke scope", err)
	}
	result, err := s.Handle(context.Background(), "/api/config/copy", shared.Marshal(shared.Object{"name": "renamed", "config": configstore.Editable(c)}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ = s.Store.Read()
	if c.Models[shared.Str(result["name"])].ID == c.Models["renamed"].ID {
		t.Fatal("copy reused identity")
	}
	s.BeforeRemoveConnection = func(string) error { return errors.New("synthetic restore conflict") }
	if _, err := s.Handle(context.Background(), "/api/config/remove", shared.Marshal(shared.Object{"name": "renamed"})); err == nil {
		t.Fatal("deleted running connection despite conflict")
	}
}

// TestAccessWriteLock 防止同时持久化配置与接入快照，重复启用不得产生交错写入。
func TestAccessWriteLock(t *testing.T) {
	s, plan := savedAccessFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.ActivateAccess(plan.ID, func(configstore.Config, configstore.AccessConfig) error { close(entered); <-release; return nil })
	}()
	<-entered
	if _, err := s.Handle(context.Background(), "/api/access/save", shared.Marshal(shared.Object{"plan": plan})); err == nil {
		t.Fatal("save raced activation")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateAccess("missing", func(configstore.Config, configstore.AccessConfig) error { return nil }); err == nil {
		t.Fatal("unknown configuration enabled")
	}
	empty := NewConfigService(configstore.NewConfigStore(filepath.Join(t.TempDir(), "models.json")))
	if err := empty.ActivateAccess("", func(configstore.Config, configstore.AccessConfig) error { return nil }); err == nil {
		t.Fatal("empty scope resumed")
	}
}

// TestDraftConnectionCopyCredentials 验证未保存副本沿用来源凭据但不重命名原连接，来源标记不会落盘或出现在可编辑响应中。
func TestDraftConnectionCopyCredentials(t *testing.T) {
	s, _ := savedAccessFixture(t)
	c, _ := s.Store.Read()
	old := c.Models["demo"]
	p := configstore.Editable(c).Models["demo"]
	p.ID = "independent-copy"
	p.SavedName = nil
	source := "demo"
	p.CopySource = &source
	result, err := s.Handle(context.Background(), "/api/config/save", shared.Marshal(shared.Object{"name": "副本", "config": configstore.Config{Version: 1, Models: map[string]configstore.Profile{"副本": p}}}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ = s.Store.Read()
	if c.Models["副本"].APIKey != old.APIKey || c.Models["副本"].ID == old.ID || !reflect.DeepEqual(c.Models["demo"], old) || c.Models["副本"].CopySource != nil {
		t.Fatal("draft copy changed source or lost key")
	}
	if strings.Contains(string(shared.Marshal(result)), old.APIKey) || strings.Contains(string(shared.Marshal(result)), "copySource") {
		t.Fatal("copy response leaked provenance or credential")
	}
}

// TestCopySourceRenameAndAddressBoundary 验证副本按稳定身份追溯改名后的来源，但来源换地址后不得把其密钥复用到旧地址。
func TestCopySourceRenameAndAddressBoundary(t *testing.T) {
	s, _ := savedAccessFixture(t)
	c, _ := s.Store.Read()
	p := configstore.Editable(c).Models["demo"]
	p.ID = "copy-after-rename"
	p.SavedName = nil
	source := "demo"
	p.CopySource = &source
	origin := c.Models["demo"]
	delete(c.Models, "demo")
	c.Models["renamed"] = origin
	if err := s.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Handle(context.Background(), "/api/config/save", shared.Marshal(shared.Object{"name": "改名后副本", "config": configstore.Config{Version: 1, Models: map[string]configstore.Profile{"改名后副本": p}}})); err != nil {
		t.Fatal("stable source identity lost after rename", err)
	}
	c, _ = s.Store.Read()
	if c.Models["改名后副本"].APIKey != origin.APIKey || c.Models["renamed"].ID != origin.ID {
		t.Fatal("copy lost source credentials or changed source identity")
	}
	origin.BaseURL = "http://127.0.0.1:10/v1"
	origin.APIKey = "synthetic-new-address-key"
	c.Models["renamed"] = origin
	if err := s.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	p.ID = "copy-stale-address"
	input := shared.Object{"name": "旧地址副本", "config": configstore.Config{Version: 1, Models: map[string]configstore.Profile{"旧地址副本": p}}}
	if _, err := s.Handle(context.Background(), "/api/config/save", shared.Marshal(input)); err == nil {
		t.Fatal("source credentials reused for the stale destination")
	}
	unchanged, _ := s.Store.Read()
	if _, exists := unchanged.Models["旧地址副本"]; exists {
		t.Fatal("failed copy partially persisted")
	}
	p.APIKey = "synthetic-explicit-destination-key"
	input["config"] = configstore.Config{Version: 1, Models: map[string]configstore.Profile{"旧地址副本": p}}
	if _, err := s.Handle(context.Background(), "/api/config/save", shared.Marshal(input)); err != nil {
		t.Fatal("explicit destination credential rejected", err)
	}
	c, _ = s.Store.Read()
	if c.Models["旧地址副本"].APIKey != p.APIKey || c.Models["renamed"].APIKey != origin.APIKey {
		t.Fatal("explicit destination credential changed the source")
	}
}

// TestResumeRequiresExplicitParameterUpdate 验证接入关闭后连接参数修改不会被总开关静默应用，密钥轮换和新增未选候选不误判。
func TestResumeRequiresExplicitParameterUpdate(t *testing.T) {
	s, plan := savedAccessFixture(t)
	if err := s.ActivateAccess(plan.ID, func(configstore.Config, configstore.AccessConfig) error { return nil }); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Store.Read()
	p := c.Models["demo"]
	p.APIKey = "synthetic-rotated"
	p.ModelCatalog = []configstore.CatalogModel{{ID: "new-candidate", Context: 128000}}
	c.Models["demo"] = p
	if err := s.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateAccess("", func(configstore.Config, configstore.AccessConfig) error { return nil }); err != nil {
		t.Fatal("credential/candidate change blocked scope resume", err)
	}
	c, _ = s.Store.Read()
	p = c.Models["demo"]
	p.Description = "updated settings"
	c.Models["demo"] = p
	if err := s.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateAccess("", func(configstore.Config, configstore.AccessConfig) error { return nil }); err == nil {
		t.Fatal("parameter update silently applied through global toggle")
	}
	result, err := s.Handle(context.Background(), "/api/config", nil)
	if err != nil || result["lastAccessCurrent"] != false {
		t.Fatal("reload reported newly saved parameters as applied", err)
	}
	if err := s.ActivateAccess(plan.ID, func(configstore.Config, configstore.AccessConfig) error { return nil }); err != nil {
		t.Fatal("explicit update rejected", err)
	}
}
