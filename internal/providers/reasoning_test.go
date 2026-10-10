package providers

import (
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// TestReasoningNativeBoundaries 确认默认不添加字段、显式关闭有效，且预算和原有生成选项不会被转换破坏。
func TestReasoningNativeBoundaries(t *testing.T) {
	for _, protocol := range []string{"responses", "compatible", "anthropic", "gemini"} {
		p := configstore.Profile{Protocol: protocol, Model: "mock", MaxTokens: 4096}
		_, _, body := RequestSpec(p, nil, "", nil)
		before := string(shared.Marshal(body))
		if err := ApplyReasoning(body, p, ""); err != nil || before != string(shared.Marshal(body)) {
			t.Fatal("default changed request", protocol)
		}
		if err := ApplyReasoning(body, p, "invalid"); err == nil {
			t.Fatal("invalid effort accepted")
		}
	}
	p := configstore.Profile{Protocol: "anthropic", Model: "claude-sonnet-4-5"}
	body := shared.Object{"max_tokens": 4096, "output_config": shared.Object{"format": shared.Object{"type": "json_schema"}}}
	if err := ApplyReasoning(body, p, "high"); err != nil {
		t.Fatal(err)
	}
	if shared.Int(shared.Obj(body["thinking"])["budget_tokens"]) != 4095 {
		t.Fatal("thinking exceeds output budget")
	}
	body["max_tokens"] = 1024
	if err := ApplyReasoning(body, p, "low"); err == nil {
		t.Fatal("invalid Claude budget accepted")
	}
	if err := ApplyReasoning(body, p, "none"); err != nil || shared.Obj(body["thinking"])["type"] != "disabled" {
		t.Fatal("cannot disable thinking")
	}
	p.Model = "claude-opus-4-6"
	if err := ApplyReasoning(body, p, "high"); err != nil || shared.Obj(body["thinking"])["type"] != "adaptive" || shared.Obj(body["output_config"])["effort"] != "high" || shared.Obj(body["output_config"])["format"] == nil {
		t.Fatal("adaptive thinking or existing output option lost")
	}
	p.Protocol, p.Model = "gemini", "gemini-3-flash-preview"
	body = shared.Object{"generationConfig": shared.Object{"maxOutputTokens": 4096, "temperature": 0.2}}
	if err := ApplyReasoning(body, p, "medium"); err != nil {
		t.Fatal(err)
	}
	generation := shared.Obj(body["generationConfig"])
	if shared.Obj(generation["thinkingConfig"])["thinkingLevel"] != "medium" || generation["temperature"] != 0.2 {
		t.Fatal("Gemini thinking or generation option lost")
	}
}

// TestExtendedReasoningLevels 验证新增档位直接透传，且原生协议保持最高映射与输出预算边界。
func TestExtendedReasoningLevels(t *testing.T) {
	for _, effort := range []string{"max", "ultra"} {
		for _, protocol := range []string{"responses", "compatible", "anthropic", "gemini"} {
			t.Run(protocol+"/"+effort, func(t *testing.T) {
				p := configstore.Profile{Protocol: protocol, Model: "mock"}
				body := shared.Object{"max_tokens": 4096, "generationConfig": shared.Object{"maxOutputTokens": 4096}}
				if err := ApplyReasoning(body, p, effort); err != nil {
					t.Fatal(err)
				}
				switch protocol {
				case "responses":
					if shared.Obj(body["reasoning"])["effort"] != effort {
						t.Fatal(body)
					}
				case "compatible":
					if body["reasoning_effort"] != effort {
						t.Fatal(body)
					}
				case "anthropic":
					if shared.Int(shared.Obj(body["thinking"])["budget_tokens"]) != 4095 {
						t.Fatal(body)
					}
					for _, model := range []string{"claude-opus-4-6", "claude-sonnet-4-6"} {
						p.Model = model
						want := "high"
						if model == "claude-opus-4-6" {
							want = "max"
						}
						if err := ApplyReasoning(body, p, effort); err != nil || shared.Obj(body["output_config"])["effort"] != want {
							t.Fatal(body, err)
						}
					}
				case "gemini":
					if shared.Int(shared.Obj(shared.Obj(body["generationConfig"])["thinkingConfig"])["thinkingBudget"]) != 4096 {
						t.Fatal(body)
					}
					p.Model = "gemini-3-pro"
					if err := ApplyReasoning(body, p, effort); err != nil || shared.Obj(shared.Obj(body["generationConfig"])["thinkingConfig"])["thinkingLevel"] != "high" {
						t.Fatal(body, err)
					}
				}
			})
		}
	}
}
