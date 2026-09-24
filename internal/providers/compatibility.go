package providers

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/cpacompat"
	"github.com/U109/api-subagents/internal/shared"
)

// Compatibility 使用每模型显式策略或官方主机名；自定义地址默认交给 CPA／通用网关转换，不凭模型名误判线路。
func Compatibility(p configstore.Profile) string {
	if mode := p.ModelCompatibility[p.Model]; mode != "" && mode != "auto" {
		return mode
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return "generic"
	}
	switch strings.ToLower(u.Hostname()) {
	case "api.deepseek.com":
		return "deepseek"
	case "api.moonshot.cn", "api.moonshot.ai":
		return "kimi"
	case "api.kimi.com":
		return "kimi-coding"
	case "ark.cn-beijing.volces.com", "ark.cn-shanghai.volces.com":
		return "doubao"
	case "api.minimax.io", "api.minimax.chat", "api.minimaxi.com":
		return "minimax"
	case "open.bigmodel.cn", "api.z.ai":
		return "glm"
	case "generativelanguage.googleapis.com":
		return "gemini"
	}
	return "generic"
}

// applyChatReasoning 为 Chat Completions 写入厂商接受的字段；默认档位不强制思考，模型 ID 始终保持原值。
func applyChatReasoning(body shared.Object, p configstore.Profile, effort string) error {
	mode := Compatibility(p)
	model := strings.ToLower(p.Model)
	if mode == "minimax" {
		// 这是输出格式开关，不会增加思考预算；避免默认把 <think> 混在正文。
		body["reasoning_split"] = true
	}
	if effort == "" {
		return nil
	}
	delete(body, "reasoning_effort")
	switch mode {
	case "deepseek":
		body["thinking"] = thinkingToggle(effort)
		if effort != "none" {
			body["reasoning_effort"] = lowHighMax(effort, false)
		}
	case "kimi-coding":
		if effort != "none" {
			effort = lowHighMax(effort, strings.Contains(model, "k3") || strings.Contains(model, "k2.8"))
		}
		data, err := cpacompat.ApplyKimiCodingThinking(shared.Marshal(body), effort)
		if err != nil {
			return errors.New("无法转换 Kimi Coding 思考设置")
		}
		clear(body)
		return json.Unmarshal(data, &body)
	case "kimi":
		if strings.Contains(model, "k3") {
			if effort == "none" {
				return errors.New("Kimi K3 不支持关闭思考，请选择低、中、高或默认")
			}
			delete(body, "thinking")
			body["reasoning_effort"] = lowHighMax(effort, true)
		} else if strings.Contains(model, "k2-thinking") || strings.Contains(model, "k2.7") {
			if effort == "none" {
				return errors.New("此 Kimi 思考模型不能关闭思考，请选择默认或其他档位")
			}
		} else if strings.Contains(model, "k2.5") || strings.Contains(model, "k2.6") {
			body["thinking"] = thinkingToggle(effort)
			// K2.5/2.6 的采样参数由服务按思考模式固定，不发送客户端残留值。
			delete(body, "temperature")
			delete(body, "top_p")
		} else if effort != "none" {
			return errors.New("此 Kimi 型号的思考参数未知，请选择默认或明确的兼容策略")
		}
	case "doubao":
		body["thinking"] = thinkingToggle(effort)
		// Seed 1.6 及后续支持档位；旧型号与 ep- 接入点只发送通用思考开关。
		if effort != "none" && (strings.Contains(model, "seed-1-6") || strings.Contains(model, "seed-1.6") || strings.Contains(model, "seed-1-8") || strings.Contains(model, "seed-1.8") || strings.Contains(model, "seed-2")) {
			body["reasoning_effort"] = effort
		}
	case "minimax":
		if effort == "none" && !strings.Contains(model, "m3") {
			return errors.New("MiniMax M2 系列不支持关闭思考，请选择默认或其他档位")
		}
		if strings.Contains(model, "m3") {
			kind := "adaptive"
			if effort == "none" {
				kind = "disabled"
			}
			body["thinking"] = shared.Object{"type": kind}
		}
	case "glm":
		body["thinking"] = thinkingToggle(effort)
		major, minor := glmVersion(model)
		if effort != "none" && (major > 5 || major == 5 && minor >= 2) {
			if major == 5 && minor == 2 {
				// 5.2 的 minimal/none 都关闭；其余取值由服务映射到 high/max。
				body["reasoning_effort"] = effort
			} else {
				body["reasoning_effort"] = lowHighMax(effort, true)
			}
		}
	case "gemini":
		body["reasoning_effort"] = geminiEffort(model, effort)
	default:
		body["reasoning_effort"] = effort
	}
	return nil
}

// thinkingToggle 将关闭与非关闭档位转换成厂商通用开关，不覆盖默认模式。
func thinkingToggle(effort string) shared.Object {
	kind := "enabled"
	if effort == "none" {
		kind = "disabled"
	}
	return shared.Object{"type": kind}
}

// lowHighMax 折合仅支持 low/high/max 的模型档位；xhigh/max/ultra 使用该策略允许的最高档。
func lowHighMax(effort string, xhighIsMax bool) string {
	if effort == "low" || effort == "minimal" {
		return "low"
	}
	if (effort == "xhigh" || effort == "max" || effort == "ultra") && xhighIsMax {
		return "max"
	}
	return "high"
}

var glmVersionPattern = regexp.MustCompile(`glm[-_ ]?(\d+)(?:[.-](\d+))?`)

// glmVersion 读取 GLM 明确的代际，无法识别的别名只使用思考开关，不猜测档位支持。
func glmVersion(model string) (int, int) {
	matches := glmVersionPattern.FindStringSubmatch(model)
	if len(matches) != 3 {
		return 0, 0
	}
	major, _ := strconv.Atoi(matches[1])
	minor, _ := strconv.Atoi(matches[2])
	return major, minor
}

// geminiEffort 将 xhigh/max/ultra 折合为 high，并约束 Gemini 3 不能关闭或不支持 medium 的型号档位。
func geminiEffort(model, effort string) string {
	if effort == "xhigh" || effort == "max" || effort == "ultra" {
		effort = "high"
	}
	if strings.Contains(model, "gemini-3") {
		if strings.Contains(model, "flash") {
			if effort == "none" {
				return "minimal"
			}
		} else if effort == "none" || effort == "minimal" {
			return "low"
		} else if effort == "medium" {
			return "high"
		}
	}
	return effort
}
