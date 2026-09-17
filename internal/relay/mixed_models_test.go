package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	"github.com/U109/api-subagents/internal/shared"
)

// TestCodexSingleWorkerMixedModels 用隔离 Codex 在同一会话逐项切换一个 worker 的厂商模型，不修改用户配置也不调用付费接口。
func TestCodexSingleWorkerMixedModels(t *testing.T) {
	bin := codexBinary(t)
	requests := make(chan shared.Object, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body shared.Object
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if req.Header.Get("Authorization") != "Bearer synthetic-relay-key" {
			t.Error("worker credential changed")
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamReply("compatible", true))
	}))
	defer server.Close()
	r := testRelay(t, "compatible", server.URL, true)
	c, _ := r.Store.Read()
	p := c.Models["demo"]
	p.Model = "gpt-6-astra"
	p.RelayModels = []string{"gemini-3.8-flash-high", "deepseek-v4-flash", "doubao-seed-2.1-turbo", "glm-5.3", "kimi-k2.5", "MiniMax-M3"}
	p.ModelCompatibility = map[string]string{"gemini-3.8-flash-high": "gemini", "deepseek-v4-flash": "deepseek", "doubao-seed-2.1-turbo": "doubao", "glm-5.3": "glm", "kimi-k2.5": "kimi", "MiniMax-M3": "minimax"}
	p.ModelContextWindows = map[string]int{p.Model: 128000, "gemini-3.8-flash-high": 1000000, "deepseek-v4-flash": 128000, "doubao-seed-2.1-turbo": 256000, "glm-5.3": 200000, "kimi-k2.5": 256000, "MiniMax-M3": 256000}
	p.ReasoningEffort = "high"
	c.Models["demo"] = p
	if err := r.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	if err := r.RefreshCatalog(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	client := startCodexRPC(t, bin, r.Codex.Home, root)
	listed := client.call(t, "model/list", shared.Object{"limit": 100})
	models := map[string]bool{}
	for _, raw := range shared.Arr(listed["data"]) {
		models[shared.Str(shared.Obj(raw)["model"])] = true
	}
	selection := append([]string{p.Model}, p.RelayModels...)
	for _, model := range selection {
		if !models[codexconfig.RelayModelAlias("demo", model)] {
			t.Fatal("Codex omitted selected worker model", model)
		}
	}
	for _, entry := range codexconfig.ModelEntries(c, "demo") {
		_, resolved, err := codexconfig.ResolveCatalogModel(c, "demo", entry.Slug)
		if err != nil || entry.ContextWindow != p.ContextWindow(resolved.Model) {
			t.Fatal("model context did not follow selection", entry, err)
		}
	}
	if len(requests) != 0 {
		t.Fatal("model list generated a paid request")
	}
	thread := client.call(t, "thread/start", shared.Object{"model": codexconfig.RelayModelAlias("demo", p.Model), "modelProvider": "api_subagents", "cwd": root, "approvalPolicy": "never", "sandbox": "read-only", "ephemeral": true})
	id := shared.Str(shared.Obj(thread["thread"])["id"])
	if id == "" {
		t.Fatal("missing thread")
	}
	for _, model := range selection {
		result := client.call(t, "turn/start", shared.Object{"threadId": id, "model": codexconfig.RelayModelAlias("demo", model), "input": []any{shared.Object{"type": "text", "text": "Reply OK without using tools.", "text_elements": []any{}}}})
		client.waitTurn(t, shared.Str(shared.Obj(result["turn"])["id"]))
		select {
		case body := <-requests:
			if body["model"] != model {
				t.Fatal("selected model did not reach upstream", model, body["model"])
			}
			mode := p.ModelCompatibility[model]
			if mode == "deepseek" || mode == "doubao" || mode == "glm" || mode == "kimi" {
				if shared.Obj(body["thinking"])["type"] != "enabled" {
					t.Fatal("wrong model thinking mode", model)
				}
			}
			if (body["reasoning_split"] == true) != (mode == "minimax") {
				t.Fatal("MiniMax format leaked across models", model)
			}
		default:
			t.Fatal("Codex failed to request selected model", model)
		}
	}
	saved, _ := r.Store.Read()
	if saved.Models["demo"].Model != p.Model || len(saved.Models) != 1 {
		t.Fatal("switching changed worker configuration")
	}
}
