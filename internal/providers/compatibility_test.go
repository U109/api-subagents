package providers

import (
	"strings"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// TestProviderCompatibilityMappings 对照厂商协议验证档位、默认、关闭和不支持场景，不发出任何网络请求。
func TestProviderCompatibilityMappings(t *testing.T) {
	cases := []struct {
		mode, model, effort, expected string
		fails                         bool
	}{
		{"deepseek", "deepseek-v4-pro", "none", `{"thinking":{"type":"disabled"}}`, false},
		{"deepseek", "deepseek-v4-pro", "medium", `{"reasoning_effort":"high","thinking":{"type":"enabled"}}`, false},
		{"deepseek", "deepseek-v4-pro", "xhigh", `{"reasoning_effort":"high","thinking":{"type":"enabled"}}`, false},
		{"kimi", "kimi-k2.5", "high", `{"thinking":{"type":"enabled"}}`, false},
		{"kimi", "kimi-k2.6", "none", `{"thinking":{"type":"disabled"}}`, false},
		{"kimi", "kimi-k3", "xhigh", `{"reasoning_effort":"max"}`, false},
		{"kimi", "kimi-k3", "none", "", true},
		{"kimi", "kimi-k2-thinking", "none", "", true},
		{"kimi", "kimi-k2-thinking", "high", `{}`, false},
		{"kimi-coding", "k3", "xhigh", `{"thinking":{"effort":"max","type":"enabled"}}`, false},
		{"kimi-coding", "k3", "none", `{"thinking":{"type":"disabled"}}`, false},
		{"doubao", "doubao-seed-1-6", "low", `{"reasoning_effort":"low","thinking":{"type":"enabled"}}`, false},
		{"doubao", "ep-synthetic", "none", `{"thinking":{"type":"disabled"}}`, false},
		{"minimax", "MiniMax-M2.7", "high", `{"reasoning_split":true}`, false},
		{"minimax", "MiniMax-M2.7", "none", "", true},
		{"minimax", "MiniMax-M3", "none", `{"reasoning_split":true,"thinking":{"type":"disabled"}}`, false},
		{"minimax", "MiniMax-M3", "high", `{"reasoning_split":true,"thinking":{"type":"adaptive"}}`, false},
		{"glm", "glm-4.7", "high", `{"thinking":{"type":"enabled"}}`, false},
		{"glm", "glm-5.2", "minimal", `{"reasoning_effort":"minimal","thinking":{"type":"enabled"}}`, false},
		{"glm", "glm-5.3-flash", "medium", `{"reasoning_effort":"high","thinking":{"type":"enabled"}}`, false},
		{"glm", "glm-5.3", "xhigh", `{"reasoning_effort":"max","thinking":{"type":"enabled"}}`, false},
		{"glm", "glm-4.7", "none", `{"thinking":{"type":"disabled"}}`, false},
		{"gemini", "gemini-3.8-flash", "none", `{"reasoning_effort":"minimal"}`, false},
		{"gemini", "gemini-3-pro", "medium", `{"reasoning_effort":"high"}`, false},
		{"generic", "any-upstream-alias", "high", `{"reasoning_effort":"high"}`, false},
		{"deepseek", "deepseek-v4-pro", "ultra", `{"reasoning_effort":"high","thinking":{"type":"enabled"}}`, false},
		{"kimi", "kimi-k3", "max", `{"reasoning_effort":"max"}`, false},
		{"kimi-coding", "k3", "ultra", `{"thinking":{"effort":"max","type":"enabled"}}`, false},
		{"glm", "glm-5.3", "ultra", `{"reasoning_effort":"max","thinking":{"type":"enabled"}}`, false},
		{"gemini", "gemini-3-pro", "max", `{"reasoning_effort":"high"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.mode+"/"+tc.model+"/"+tc.effort, func(t *testing.T) {
			p := configstore.Profile{Protocol: "compatible", Model: tc.model, ModelCompatibility: map[string]string{tc.model: tc.mode}}
			body := shared.Object{"model": tc.model}
			err := ApplyReasoning(body, p, tc.effort)
			if (err != nil) != tc.fails {
				t.Fatalf("error = %v", err)
			}
			if body["model"] != tc.model {
				t.Fatal("changed upstream model ID")
			}
			delete(body, "model")
			if !tc.fails && string(shared.Marshal(body)) != tc.expected {
				t.Fatalf("got %s; want %s", shared.Marshal(body), tc.expected)
			}
			body = shared.Object{}
			if err := ApplyReasoning(body, p, ""); err != nil {
				t.Fatal(err)
			}
			delete(body, "reasoning_split")
			if len(body) != 0 {
				t.Fatal("default enabled thinking", body)
			}
		})
	}
}

// TestCompatibilityHostIsolation 验证只自动识别完整官方主机名，别名、自定义网关及相似域名不能触发厂商参数。
func TestCompatibilityHostIsolation(t *testing.T) {
	for host, want := range map[string]string{"api.deepseek.com": "deepseek", "api.moonshot.cn": "kimi", "api.kimi.com": "kimi-coding", "api.minimax.io": "minimax", "ark.cn-beijing.volces.com": "doubao", "open.bigmodel.cn": "glm", "generativelanguage.googleapis.com": "gemini", "api.deepseek.com.example.org": "generic", "127.0.0.1": "generic"} {
		p := configstore.Profile{BaseURL: "https://" + host + "/v1", Model: "gemini-alias"}
		if got := Compatibility(p); got != want {
			t.Fatalf("%s = %s", host, got)
		}
		p.ModelCompatibility = map[string]string{p.Model: "generic"}
		if Compatibility(p) != "generic" {
			t.Fatal("explicit mode ignored")
		}
	}
}

// TestMiniMaxDetailsStreaming 验证累计思考快照不重复、签名结构保留、同帧双字段不双写，正文标签不被误删。
func TestMiniMaxDetailsStreaming(t *testing.T) {
	p := configstore.Profile{Model: "MiniMax-M3", ModelCompatibility: map[string]string{"MiniMax-M3": "minimax"}}
	for _, both := range []bool{false, true} {
		s := NewChatStream(p)
		for index, text := range []string{"先看", "先看代码"} {
			delta := shared.Object{"reasoning_details": []any{shared.Object{"index": 0, "type": "reasoning.text", "text": text, "signature": "opaque"}}}
			if both {
				delta["reasoning_content"] = []string{"先看", "代码"}[index]
			}
			if index == 1 {
				delta["content"] = "示例 <thinking> 原文"
			}
			chunk := shared.Object{"choices": []any{shared.Object{"index": 0, "delta": delta, "finish_reason": "stop"}}}
			if _, err := s.Accept(string(shared.Marshal(chunk))); err != nil {
				t.Fatal(err)
			}
		}
		if s.Message() != nil {
			t.Fatal("accepted unfinished stream")
		}
		if _, err := s.Accept("[DONE]"); err != nil {
			t.Fatal(err)
		}
		message := s.Message()
		if message["reasoning_content"] != "先看代码" || !strings.Contains(shared.Str(message["content"]), "<thinking>") {
			t.Fatal("reasoning repeated or content rewritten", message)
		}
		if shared.Obj(shared.Arr(message["reasoning_details"])[0])["signature"] != "opaque" {
			t.Fatal("signature lost")
		}
	}
}
