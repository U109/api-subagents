package relay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/settings"
	"github.com/U109/api-subagents/internal/shared"
)

// TestEditedModelIDForwarding 通过配置保存入口修正错误 ID，确认两种兼容协议向本地上游发送新 ID。
// 显示名称不进入请求，失效的旧模型别名应在本地拦截，默认身份和连接凭据保持原值。
func TestEditedModelIDForwarding(t *testing.T) {
	for _, protocol := range []string{"responses", "compatible"} {
		t.Run(protocol, func(t *testing.T) {
			requests := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				data, _ := io.ReadAll(req.Body)
				var body shared.Object
				if json.Unmarshal(data, &body) != nil || strings.Contains(string(data), "display-only") || req.Header.Get("Authorization") != "Bearer synthetic-relay-key" {
					t.Error("unexpected upstream payload or credentials")
				}
				requests <- shared.Str(body["model"])
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, upstreamReply(protocol, true))
			}))
			defer server.Close()
			r := testRelay(t, protocol, server.URL, true)
			c, err := r.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			p := c.Models["demo"]
			p.RelayModels = []string{"incorrect-extra"}
			c.Models["demo"] = p
			if err := r.Store.Save(c); err != nil {
				t.Fatal(err)
			}
			draft := configstore.Editable(c)
			p = draft.Models["demo"]
			p.Model = "corrected-default"
			p.RelayModels = []string{"corrected-extra"}
			p.ModelNames = map[string]string{"corrected-default": "display-only-default", "corrected-extra": "display-only-extra"}
			draft.Models["demo"] = p
			service := settings.NewConfigService(r.Store)
			if _, err := service.Handle(context.Background(), "/api/config", shared.Marshal(shared.Object{"config": draft})); err != nil {
				t.Fatal(err)
			}
			if err := r.RefreshCatalog(); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct{ alias, model string }{
				{"api-subagents/demo", "corrected-default"},
				{codexconfig.RelayModelAlias("demo", "corrected-extra"), "corrected-extra"},
			} {
				status, _ := requestRelay(t, r, shared.Object{"model": tc.alias, "input": "Reply OK", "stream": true})
				if status != 200 {
					t.Fatal("corrected model rejected", status)
				}
				select {
				case model := <-requests:
					if model != tc.model {
						t.Fatal("upstream received old ID or display name")
					}
				default:
					t.Fatal("request did not reach local upstream")
				}
			}
			if status, _ := requestRelay(t, r, shared.Object{"model": codexconfig.RelayModelAlias("demo", "incorrect-extra"), "input": "Reply OK"}); status != 400 {
				t.Fatal("old ID still active")
			}
			if len(requests) != 0 {
				t.Fatal("old model reached upstream")
			}
		})
	}
}
