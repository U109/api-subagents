package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	"github.com/U109/api-subagents/internal/shared"
)

// TestRelayModelSwitching 使用两条隔离连接验证默认、同连接多模型、跨连接同名模型与错误拦截。
// HTTP 列表必须和磁盘目录一致，模型变更只影响新请求，不能写回连接默认值或泄露 Key。
func TestRelayModelSwitching(t *testing.T) {
	type forwarded struct{ model, key, path string }
	requests := make(chan forwarded, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body shared.Object
		json.NewDecoder(req.Body).Decode(&body)
		requests <- forwarded{shared.Str(body["model"]), req.Header.Get("Authorization"), req.URL.Path}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamReply("responses", true))
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL+"/first", true)
	c, err := r.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	p := c.Models["demo"]
	p.RelayModels = []string{"model/a", "模型二"}
	c.Models["demo"] = p
	p.BaseURL, p.APIKey = server.URL+"/second", "synthetic-second-key"
	c.Models["另一连接 / Gemini #2"] = p
	if err = r.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	if err = r.RefreshCatalog(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ alias, model, connection, key, path string }{
		{"api-subagents", "mock-model", "demo", "synthetic-relay-key", "/first/responses"},
		{"api-subagents/demo", "mock-model", "demo", "synthetic-relay-key", "/first/responses"},
		{codexconfig.RelayModelAlias("demo", "model/a"), "model/a", "demo", "synthetic-relay-key", "/first/responses"},
		{codexconfig.RelayModelAlias("demo", "模型二"), "模型二", "demo", "synthetic-relay-key", "/first/responses"},
		{codexconfig.RelayModelAlias("另一连接 / Gemini #2", "model/a"), "model/a", "另一连接 / Gemini #2", "synthetic-second-key", "/second/responses"},
	} {
		status, body := requestRelay(t, r, shared.Object{"model": tc.alias, "input": "Reply OK", "stream": true})
		if status != 200 {
			t.Fatal("forward failed", status, body)
		}
		got := <-requests
		if got.model != tc.model || got.key != "Bearer "+tc.key || got.path != tc.path {
			t.Fatal("wrong upstream model or credentials")
		}
		if r.Snapshot().ActiveModel != tc.connection || r.Snapshot().ActiveModelID != tc.model {
			t.Fatal("active model status lost")
		}
	}
	saved, err := r.Store.Read()
	if err != nil || !reflect.DeepEqual(saved, c) {
		t.Fatal("switching overwrote default configuration", err)
	}
	req, _ := http.NewRequest("GET", r.Snapshot().Address+"/models", nil)
	req.Header.Set("X-Api-Subagents-Token", r.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var listed, catalog shared.Object
	if err = json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.Codex.DataRoot, "codex-model-catalog.json"))
	if err != nil || json.Unmarshal(data, &catalog) != nil {
		t.Fatal("catalog not refreshed", err)
	}
	items, models := shared.Arr(listed["data"]), shared.Arr(catalog["models"])
	if res.StatusCode != 200 || len(items) != 7 || len(items) != len(models) {
		t.Fatal("model list differs from Codex catalog")
	}
	for i := range items {
		if shared.Obj(items[i])["id"] != shared.Obj(models[i])["slug"] {
			t.Fatal("catalog order or aliases differ")
		}
	}
	p = c.Models["demo"]
	p.RelayModels = []string{"model/a"}
	c.Models["demo"] = p
	if err = r.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	if err = r.RefreshCatalog(); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []any{codexconfig.RelayModelAlias("demo", "模型二"), codexconfig.RelayModelAlias("demo", "unknown"), "api-subagents/demo/model/a", 42} {
		if status, _ := requestRelay(t, r, shared.Object{"model": alias, "input": "x"}); status != 400 {
			t.Fatal("removed or unknown model accepted", status)
		}
	}
	if len(requests) != 0 {
		t.Fatal("invalid model reached upstream")
	}
}

// TestCodexHijackModelList 用真实 app-server 验证中文和斜线连接名的目录及同一对话内模型切换，模型均由本地服务模拟。
func TestCodexHijackModelList(t *testing.T) {
	bin := codexBinary(t)
	requests := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body shared.Object
		json.NewDecoder(req.Body).Decode(&body)
		requests <- shared.Str(body["model"])
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamReply("compatible", true))
	}))
	defer server.Close()
	r := testRelay(t, "compatible", server.URL, true)
	c, err := r.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	p := c.Models["demo"]
	p.RelayModels = []string{"fast-model", "deep-model"}
	connection := "日常助手 / Gemini #1"
	c.Models[connection] = p
	if err = r.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	if err = r.RefreshCatalog(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	client := startCodexRPC(t, bin, r.Codex.Home, root)
	listed := client.call(t, "model/list", shared.Object{"limit": 100})
	models := map[string]bool{}
	for _, item := range shared.Arr(listed["data"]) {
		models[shared.Str(shared.Obj(item)["model"])] = true
	}
	for _, name := range p.RelayModels {
		if !models[codexconfig.RelayModelAlias(connection, name)] {
			t.Fatal("Codex model list omitted configured model", name, listed)
		}
	}
	if len(requests) != 0 {
		t.Fatal("listing models triggered generation")
	}
	thread := client.call(t, "thread/start", shared.Object{"model": "api-subagents/" + url.PathEscape(connection), "modelProvider": "api_subagents", "cwd": root, "approvalPolicy": "never", "sandbox": "read-only", "ephemeral": true})
	id := shared.Str(shared.Obj(thread["thread"])["id"])
	if id == "" {
		t.Fatal("missing thread ID")
	}
	for _, name := range p.RelayModels {
		result := client.call(t, "turn/start", shared.Object{"threadId": id, "model": codexconfig.RelayModelAlias(connection, name), "input": []any{shared.Object{"type": "text", "text": "Reply OK without using tools.", "text_elements": []any{}}}})
		client.waitTurn(t, shared.Str(shared.Obj(result["turn"])["id"]))
		select {
		case model := <-requests:
			if model != name {
				t.Fatal("Codex switched to wrong model", model)
			}
		default:
			t.Fatal("Codex turn did not reach local model")
		}
	}
}

// waitTurn 等待指定对话轮次完成并确认没有模型错误，启动响应前到达的通知从队列补读。
func (c *codexRPC) waitTurn(t *testing.T, turnID string) {
	t.Helper()
	for {
		var event shared.Object
		if len(c.notifications) > 0 {
			event, c.notifications = c.notifications[0], c.notifications[1:]
		} else if err := c.decoder.Decode(&event); err != nil {
			t.Fatal("turn completion", err)
		}
		if event["method"] != "turn/completed" {
			continue
		}
		turn := shared.Obj(shared.Obj(event["params"])["turn"])
		if turn["id"] != turnID {
			continue
		}
		if turn["status"] != "completed" || turn["error"] != nil {
			t.Fatal("Codex turn failed", turn)
		}
		return
	}
}
