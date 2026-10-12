package relay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/settings"
	"github.com/U109/api-subagents/internal/shared"
)

// TestAccessLimitsCatalogueAndRequests 验证两个配置的模型范围真正限制目录与 HTTP 路由，保存连接或配置不会扩大正在运行的范围。
func TestAccessLimitsCatalogueAndRequests(t *testing.T) {
	requests := make(chan string, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body shared.Object
		_ = json.NewDecoder(req.Body).Decode(&body)
		requests <- shared.Str(body["model"])
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamReply("responses", true))
	}))
	defer upstream.Close()
	r := testRelay(t, "responses", upstream.URL, true)
	c, _ := r.Store.Read()
	p := c.Models["demo"]
	p.RelayModels = []string{"hidden", "extra"}
	c.Models["demo"] = p
	for _, name := range []string{"worker", "review"} {
		copy := p
		copy.ID = name
		c.Models[name] = copy
	}
	a := configstore.AccessConfig{ID: "a", Name: "配置 A", ConnectionIDs: []string{"demo", "worker"}, ModelIDs: map[string][]string{"demo": {"extra"}, "worker": {"mock-model"}}, DefaultKey: `["demo","extra"]`}
	b := configstore.AccessConfig{ID: "b", Name: "配置 B", ConnectionIDs: []string{"demo", "review"}, ModelIDs: map[string][]string{"demo": {"mock-model"}, "review": {"hidden"}}, DefaultKey: `["review","hidden"]`}
	c.AccessConfigs = []configstore.AccessConfig{a, b}
	if err := r.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	s := settings.NewConfigService(r.Store)
	s.BeforeRemoveConnection = r.RemoveConnection
	s.BeforeRemoveAccess = r.RemoveAccess
	if err := s.ActivateAccess("a", r.EnableAccess); err != nil {
		t.Fatal(err)
	}
	scope, err := r.requestConfig()
	if err != nil || len(scope.Models) != 2 || scope.Models["demo"].Model != "extra" || len(scope.Models["demo"].RelayModels) != 0 {
		t.Fatal("wrong running scope", err)
	}
	data, err := os.ReadFile(filepath.Join(r.Codex.DataRoot, "codex-model-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalogue struct {
		Models []shared.Object `json:"models"`
	}
	if json.Unmarshal(data, &catalogue) != nil || len(catalogue.Models) != 2 {
		t.Fatal("disk catalogue exceeded configuration range")
	}
	if strings.Contains(string(data), "synthetic-relay-key") {
		t.Fatal("catalogue leaked credentials")
	}
	status, _ := requestRelay(t, r, shared.Object{"model": codexconfig.RelayModelAlias("demo", "hidden"), "input": "test", "stream": true})
	if status == 200 {
		t.Fatal("unselected model forwarded")
	}
	status, _ = requestRelay(t, r, shared.Object{"model": codexconfig.RelayModelAlias("review", "hidden"), "input": "test", "stream": true})
	if status == 200 {
		t.Fatal("unselected connection forwarded")
	}
	select {
	case got := <-requests:
		t.Fatal("unauthorized upstream call", got)
	default:
	}
	status, _ = requestRelay(t, r, shared.Object{"model": codexconfig.RelayModelAlias("demo", "extra"), "input": "test", "stream": true})
	if status != 200 || <-requests != "extra" {
		t.Fatal("selected model not routed")
	}
	// 保存同名配置的新范围不会改变 A 的快照；新增连接目录也不得自动刷新成全部模型。
	a.ModelIDs["demo"] = []string{"hidden"}
	a.DefaultKey = `["demo","hidden"]`
	if _, err := s.Handle(context.Background(), "/api/access/save", shared.Marshal(shared.Object{"plan": a})); err != nil {
		t.Fatal(err)
	}
	c, _ = r.Store.Read()
	p = c.Models["demo"]
	p.RelayModels = append(p.RelayModels, "new")
	c.Models["demo"] = p
	if err := r.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	if err := r.RefreshCatalog(); err != nil {
		t.Fatal(err)
	}
	after, _ := r.requestConfig()
	if !reflect.DeepEqual(scope.Models, after.Models) {
		t.Fatal("saving changed running scope")
	}
	if err := r.Disable(); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateAccess("", r.EnableAccess); err != nil {
		t.Fatal(err)
	}
	after, _ = r.requestConfig()
	if after.Models["demo"].Model != "extra" {
		t.Fatal("resume used current saved draft instead of remembered range")
	}
	if err := s.ActivateAccess("b", r.EnableAccess); err != nil {
		t.Fatal(err)
	}
	after, _ = r.requestConfig()
	if _, ok := after.Models["worker"]; ok || len(after.Models) != 2 || after.Models["review"].Model != "hidden" {
		t.Fatal("B scope includes A connections")
	}
	// 已开始的请求携带旧不可变范围；切换后不会把旧请求映射到新配置。
	if _, err := r.requestConfigFor(State{scope: scopePointer(scope)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Handle(context.Background(), "/api/config/remove", shared.Marshal(shared.Object{"name": "review"})); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().Enabled {
		t.Fatal("removing running connection left gateway enabled")
	}
}

// scopePointer 为请求切换测试创建独立配置指针，避免取循环变量或复用可变对象。
func scopePointer(c configstore.Config) *configstore.Config { return &c }

// TestUnknownNativeContextNotWritten 验证显式关闭且未知容量时目录不输出伪造上下文数值。
func TestUnknownNativeContextNotWritten(t *testing.T) {
	c := configstore.EmptyConfig()
	c.Models["demo"] = configstore.Profile{Model: "unknown", ModelContextOverrides: map[string]bool{"unknown": false}}
	disk := codexconfig.CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
	if err := disk.WriteCatalog(c, "demo"); err != nil {
		t.Fatal(err)
	}
	entries := codexconfig.ModelEntries(c, "demo")
	if len(entries) != 1 || entries[0].ContextWindow != 0 {
		t.Fatal("unknown context fabricated")
	}
	// WriteCatalog 产物只有模型元数据；递归检查本测试的独立数据目录，不读取真实 CODEX_HOME。
	checkedFiles := 0
	err := filepath.WalkDir(disk.DataRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		checkedFiles++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), `"context_window"`) || strings.Contains(string(data), `"auto_compact_token_limit"`) {
			t.Fatal("unknown native capacity was emitted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checkedFiles == 0 {
		t.Fatal("catalogue was not inspected")
	}
}
