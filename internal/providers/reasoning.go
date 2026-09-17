package providers

import (
	"errors"
	"strings"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/cpacompat"
	"github.com/U109/api-subagents/internal/shared"
)

// ApplyReasoning 将有效档位写入实际出站协议；空值不改变服务默认，原生预算不突破最大输出限制。
// 具体模型支持哪些档位由服务商决定，不能通过一次失败后重试来探测，以免重复产生费用。
func ApplyReasoning(body shared.Object, p configstore.Profile, effort string) error {
	if !configstore.ValidReasoningEffort(effort) {
		return errors.New("不支持此思考等级，请在模型配置中重新选择。")
	}
	if p.Protocol == "compatible" {
		return applyChatReasoning(body, p, effort)
	}
	if effort == "" {
		return nil
	}
	switch p.Protocol {
	case "responses":
		reasoning := shared.Obj(body["reasoning"])
		reasoning["effort"] = effort
		body["reasoning"] = reasoning
	case "anthropic":
		delete(body, "thinking")
		output := shared.Obj(body["output_config"])
		delete(output, "effort")
		if len(output) == 0 {
			delete(body, "output_config")
		}
		if effort == "none" {
			body["thinking"] = shared.Object{"type": "disabled"}
			return nil
		}
		model := strings.ReplaceAll(strings.ToLower(p.Model), ".", "-")
		if strings.Contains(model, "opus-4-6") || strings.Contains(model, "sonnet-4-6") {
			level, _ := cpacompat.MapToClaudeEffort(effort, strings.Contains(model, "opus-4-6"))
			body["thinking"] = shared.Object{"type": "adaptive"}
			output["effort"] = level
			body["output_config"] = output
			return nil
		}
		limit := shared.Int(body["max_tokens"])
		if limit < 1025 {
			return errors.New("Claude 预算式思考需要最大输出长度至少为 1025；请在高级设置中调整。")
		}
		budget := min(max(reasoningBudget(effort), 1024), limit-1)
		body["thinking"] = shared.Object{"type": "enabled", "budget_tokens": budget}
	case "gemini":
		generation := shared.Obj(body["generationConfig"])
		thinking := shared.Obj(generation["thinkingConfig"])
		delete(thinking, "thinkingLevel")
		delete(thinking, "thinkingBudget")
		if strings.HasPrefix(strings.TrimPrefix(strings.ToLower(p.Model), "models/"), "gemini-3") {
			thinking["thinkingLevel"] = geminiEffort(strings.ToLower(p.Model), effort)
		} else {
			budget := min(reasoningBudget(effort), 24576)
			if limit := shared.Int(generation["maxOutputTokens"]); limit > 0 {
				budget = min(budget, limit)
			}
			thinking["thinkingBudget"] = budget
		}
		generation["thinkingConfig"] = thinking
		body["generationConfig"] = generation
	}
	return nil
}

// reasoningBudget 沿用 CPA 的常见档位预算，供不接受文本等级的原生协议使用。
func reasoningBudget(effort string) int {
	return map[string]int{"none": 0, "minimal": 512, "low": 1024, "medium": 8192, "high": 24576, "xhigh": 32768}[effort]
}
