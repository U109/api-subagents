package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestRelayReasoningPriority 检查转换协议的默认等级与覆盖；透传仅保留 Codex 已发送的等级，不补写参数。
func TestRelayReasoningPriority(t *testing.T) {
	for _, protocol := range []string{"responses", "compatible", "anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			var actual shared.Object
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				json.NewDecoder(req.Body).Decode(&actual)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, upstreamReply(protocol, false))
			}))
			defer server.Close()
			r := testRelay(t, protocol, server.URL, false)
			for _, tc := range []struct{ saved, chosen, want string }{{"", "", ""}, {"low", "", "low"}, {"high", "low", "low"}, {"high", "none", "none"}, {"max", "", "max"}, {"ultra", "", "ultra"}, {"high", "max", "max"}, {"max", "ultra", "ultra"}, {"ultra", "low", "low"}} {
				c, _ := r.Store.Read()
				p := c.Models["demo"]
				p.ReasoningEffort = tc.saved
				c.Models["demo"] = p
				if err := r.Store.Save(c); err != nil {
					t.Fatal(err)
				}
				input := shared.Object{"model": "api-subagents/demo", "input": "Reply OK"}
				if tc.chosen != "" {
					input["reasoning"] = shared.Object{"effort": tc.chosen}
				}
				actual = nil
				status, response := requestRelay(t, r, input)
				if status != 200 {
					t.Fatal(status, response)
				}
				switch protocol {
				case "responses":
					if shared.Str(shared.Obj(actual["reasoning"])["effort"]) != tc.chosen {
						t.Fatal(actual)
					}
				case "compatible":
					if shared.Str(actual["reasoning_effort"]) != tc.want {
						t.Fatal(actual)
					}
				case "anthropic":
					thinking := shared.Obj(actual["thinking"])
					if tc.want == "" && len(thinking) != 0 || tc.want == "none" && thinking["type"] != "disabled" || tc.want == "low" && shared.Int(thinking["budget_tokens"]) != 1024 {
						t.Fatal(actual)
					}
				case "gemini":
					thinking := shared.Obj(shared.Obj(actual["generationConfig"])["thinkingConfig"])
					if tc.want == "" && len(thinking) != 0 || tc.want == "none" && shared.Int(thinking["thinkingBudget"]) != 0 || tc.want == "low" && shared.Int(thinking["thinkingBudget"]) != 1024 {
						t.Fatal(actual)
					}
				}
			}
		})
	}
}
