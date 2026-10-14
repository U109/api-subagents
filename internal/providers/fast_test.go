package providers

import (
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// TestRequestFastByModel 验证不同协议只给显式开启的路由模型带 Fast，切换模型和关闭不会串用设置。
func TestRequestFastByModel(t *testing.T) {
	for _, protocol := range []string{"responses", "compatible", "anthropic", "gemini"} {
		p := configstore.Profile{Protocol: protocol, Model: "one", BaseURL: "http://127.0.0.1", ModelFastModes: map[string]bool{"one": true}}
		_, headers, body := RequestSpec(p, nil, "", nil)
		switch protocol {
		case "responses", "compatible":
			if body["service_tier"] != "priority" {
				t.Fatal(protocol, body)
			}
		case "anthropic":
			if body["speed"] != "fast" || headers.Get("anthropic-beta") != "fast-mode-2026-02-01" {
				t.Fatal(body, headers)
			}
		case "gemini":
			if body["service_tier"] != nil || body["speed"] != nil {
				t.Fatal("invalid Gemini field", body)
			}
		}
		p.Model = "two-fast"
		_, headers, body = RequestSpec(p, nil, "", nil)
		if body["service_tier"] != nil || body["speed"] != nil || headers.Get("anthropic-beta") != "" {
			t.Fatal("Fast leaked to another model", body)
		}
		if ApplyReasoning(shared.Object{}, p, "ultra") == nil {
			t.Fatal("removed effort accepted")
		}
	}
}
