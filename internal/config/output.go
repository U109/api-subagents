package config

import (
	"errors"
	"maps"
	"net/url"
	"strings"
)

const MaxModelOutputTokens = 1000000

// ModelOutputSetting 区分厂商资料、火山 Coding Plan、上游默认和自定义预算；只用于 Codex Responses。
type ModelOutputSetting struct {
	Mode   string `json:"mode"`
	Tokens int    `json:"tokens,omitempty"`
}

// OutputPreset 记录自动请求预算和出处，K 单位按十进制保守换算，不宣称是平台精确硬上限。
type OutputPreset struct {
	Tokens int    `json:"tokens"`
	Source string `json:"source"`
}

var modelOutputDefaults = buildModelOutputDefaults()

// buildModelOutputDefaults 建立有官方出处的预算表；平台不同的型号分别维护，未核实的型号不补参数。
func buildModelOutputDefaults() map[string]map[string]OutputPreset {
	const ark = "https://docs.volcengine.com/docs/ark/coding-plan-personal-plan-overview?lang=zh"
	official := map[string]OutputPreset{}
	for alias, context := range officialModelContexts {
		limit := 0
		switch context.Model {
		case "gpt-6-astra", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "glm-5.3", "claude-fable-5-1", "claude-opus-5", "claude-sonnet-5":
			limit = 128000
		case "claude-haiku-4-5-20251001":
			limit = 64000
		case "gemini-3.8-flash", "gemini-3.1-pro-preview":
			limit = 65536
		case "deepseek-v4-flash", "deepseek-v4-pro":
			limit = 384000
		}
		if limit > 0 {
			official[alias] = OutputPreset{Tokens: limit, Source: context.Source}
		}
	}
	volcengine := map[string]OutputPreset{}
	for model, limit := range map[string]int{"deepseek-v4-flash": 384000, "deepseek-v4-pro": 384000, "glm-5.3": 128000, "glm-5.3-flash": 128000, "kimi-k3": 128000, "minimax-m3": 128000, "doubao-seed-2.1-turbo": 64000, "kimi-k2.7-code": 32000} {
		volcengine[model] = OutputPreset{Tokens: limit, Source: ark}
	}
	return map[string]map[string]OutputPreset{"official": official, "volcengine": volcengine}
}

// OfficialModelOutputs 返回独立副本供界面展示，避免调用方修改转发共用的资料。
func OfficialModelOutputs() map[string]map[string]OutputPreset {
	return map[string]map[string]OutputPreset{"official": maps.Clone(modelOutputDefaults["official"]), "volcengine": maps.Clone(modelOutputDefaults["volcengine"])}
}

// OutputLimit 只为 Responses 返回缺省预算；显式上游模式和未核实型号返回零，调用方不得覆盖请求已有参数。
func (p Profile) OutputLimit(model string) int {
	if p.Protocol != "responses" {
		return 0
	}
	setting := p.ModelOutputs[model]
	switch setting.Mode {
	case "upstream":
		return 0
	case "custom":
		return setting.Tokens
	}
	mode := setting.Mode
	if mode == "" || mode == "official" {
		mode = "official"
		if endpoint, err := url.Parse(p.BaseURL); err == nil && strings.EqualFold(endpoint.Hostname(), "ark.cn-beijing.volces.com") && strings.HasPrefix(endpoint.Path, "/api/coding") {
			mode = "volcengine"
		}
	}
	return modelOutputDefaults[mode][strings.ToLower(strings.TrimSpace(model))].Tokens
}

// normalizeModelOutputs 校验预算模式和整数范围，只保留已选模型的独立设置；自动模式不写入用户配置。
func normalizeModelOutputs(p Profile) (map[string]ModelOutputSetting, error) {
	result := make(map[string]ModelOutputSetting)
	for _, model := range append([]string{p.Model}, p.RelayModels...) {
		setting, exists := p.ModelOutputs[model]
		if !exists || model == "" {
			continue
		}
		switch setting.Mode {
		case "", "official":
			if setting.Tokens != 0 {
				return nil, errors.New("自动输出额度不能同时填写自定义值")
			}
			continue
		case "volcengine", "upstream":
			if setting.Tokens != 0 {
				return nil, errors.New("请切换到自定义模式后填写输出额度")
			}
		case "custom":
			if setting.Tokens < 128 || setting.Tokens > MaxModelOutputTokens {
				return nil, errors.New("输出额度必须为 128–1000000 的整数 tokens")
			}
		default:
			return nil, errors.New("输出额度模式无效")
		}
		result[model] = setting
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}
