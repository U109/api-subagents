package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	"github.com/U109/api-subagents/internal/shared"
)

// TestRelayModelFastRouting 通过真实本地网关验证按模型 Fast、关闭覆盖、跨模型隔离与 compact 排除档位。
func TestRelayModelFastRouting(t *testing.T) {
	for _, protocol := range []string{"responses", "compatible", "anthropic"} {
		// 每次请求用独立对象并经通道交给测试；JSON 解码到旧 map 会保留上一请求的 Fast 字段。
		requests := make(chan struct {
			body shared.Object
			beta string
		}, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			var actual shared.Object
			_ = json.NewDecoder(req.Body).Decode(&actual)
			requests <- struct {
				body shared.Object
				beta string
			}{actual, req.Header.Get("anthropic-beta")}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, upstreamReply(protocol, false))
		}))
		r := testRelay(t, protocol, server.URL, false)
		c, _ := r.Store.Read()
		p := c.Models["demo"]
		p.RelayModels = []string{"other"}
		p.ModelFastModes = map[string]bool{p.Model: true}
		c.Models["demo"] = p
		if err := r.Store.Save(c); err != nil {
			t.Fatal(err)
		}
		for _, model := range []string{p.Model, "other"} {
			status, body := requestRelay(t, r, shared.Object{"model": codexconfig.RelayModelAlias("demo", model), "input": "LOCAL ONLY"})
			received := <-requests
			actual, beta := received.body, received.beta
			if status != http.StatusOK {
				t.Fatal(protocol, status, body)
			}
			if model == p.Model {
				if protocol == "anthropic" {
					if actual["speed"] != "fast" || beta != "fast-mode-2026-02-01" {
						t.Fatal(actual, beta)
					}
				} else if actual["service_tier"] != "priority" {
					t.Fatal(actual)
				}
			} else if actual["service_tier"] != nil || actual["speed"] != nil || beta != "" {
				t.Fatal("Fast leaked across models", actual, beta)
			}
		}
		if protocol == "responses" {
			status, _ := requestRelay(t, r, shared.Object{"model": codexconfig.RelayModelAlias("demo", "other"), "input": "LOCAL ONLY", "service_tier": "priority"})
			actual := (<-requests).body
			if status != http.StatusOK || actual["service_tier"] != "default" {
				t.Fatal("disabled model followed client Fast tier", status, actual)
			}
			request, _ := http.NewRequest(http.MethodPost, r.Snapshot().Address+"/responses/compact", strings.NewReader(string(shared.Marshal(shared.Object{"model": codexconfig.RelayModelAlias("demo", p.Model), "input": "LOCAL ONLY", "service_tier": "priority"}))))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Api-Subagents-Token", r.token)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.ReadAll(response.Body)
			_ = response.Body.Close()
			actual = (<-requests).body
			if actual["service_tier"] != nil {
				t.Fatal("compact received unsupported Fast tier", actual)
			}
		}
		server.Close()
	}
}
