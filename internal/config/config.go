package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/U109/api-subagents/internal/shared"
)

var Defaults = map[string]string{"compatible": "https://api.openai.com/v1", "responses": "https://api.openai.com/v1", "anthropic": "https://api.anthropic.com/v1", "gemini": "https://generativelanguage.googleapis.com/v1beta"}

var bearerPattern = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/\-]+`)

type Profile struct {
	Protocol            string            `json:"protocol"`
	Model               string            `json:"model"`
	BaseURL             string            `json:"baseUrl"`
	APIKey              string            `json:"apiKey"`
	APIKeyEnv           string            `json:"apiKeyEnv"`
	Description         string            `json:"description"`
	ReasoningEffort     string            `json:"reasoningEffort,omitempty"`
	RelayModels         []string          `json:"relayModels,omitempty"`
	ModelNames          map[string]string `json:"modelNames,omitempty"`
	ModelContextWindows map[string]int    `json:"modelContextWindows,omitempty"`
	ModelCompatibility  map[string]string `json:"modelCompatibility,omitempty"`
	ModelImageInputs    map[string]bool   `json:"modelImageInputs,omitempty"`
	MaxTokens           int               `json:"maxTokens"`
	Stream              bool              `json:"stream"`
	FirstTimeout        int               `json:"firstResponseTimeoutSeconds"`
	IdleTimeout         int               `json:"streamIdleTimeoutSeconds"`
	TaskTimeout         int               `json:"taskTimeoutMinutes"`
	HasKey              bool              `json:"hasKey,omitempty"`
	SavedName           *string           `json:"savedName,omitempty"`
}

type Config struct {
	Version       int                `json:"version"`
	MaxConcurrent int                `json:"maxConcurrent"`
	Models        map[string]Profile `json:"models"`
}

type ConfigStore struct {
	Path string
}

// DataDir 沿用旧版目录和环境变量，使升级无需移动或覆盖用户的连接配置。
func DataDir() string {
	if value := os.Getenv("API_SUBAGENTS_HOME"); value != "" {
		return value
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "CodexApiSubagents")
}

// NewConfigStore 创建配置访问器；空路径使用与旧版一致的本机位置。
func NewConfigStore(path string) *ConfigStore {
	if path == "" {
		path = filepath.Join(DataDir(), "models.json")
	}
	return &ConfigStore{Path: path}
}

// EmptyConfig 返回首次使用时的空配置，不预置模型或密钥。
func EmptyConfig() Config { return Config{Version: 1, MaxConcurrent: 3, Models: map[string]Profile{}} }

// ValidateConfig 补齐旧版默认值并严格校验输入；目录查询可暂不要求模型 ID。
func ValidateConfig(data []byte, requireModel bool) (Config, error) {
	var raw struct {
		Version       int                        `json:"version"`
		MaxConcurrent *int                       `json:"maxConcurrent"`
		Models        map[string]json.RawMessage `json:"models"`
	}
	if json.Unmarshal(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}), &raw) != nil || raw.Version != 1 || raw.Models == nil {
		return Config{}, errors.New("配置需要 version: 1 和 models 对象。")
	}
	c := EmptyConfig()
	if raw.MaxConcurrent != nil {
		c.MaxConcurrent = *raw.MaxConcurrent
	}
	if c.MaxConcurrent < 1 || c.MaxConcurrent > 8 {
		return c, errors.New("并发数必须为 1–8。")
	}
	if len(raw.Models) > 50 {
		return c, errors.New("最多配置 50 个模型。")
	}
	for name, item := range raw.Models {
		if strings.TrimSpace(name) == "" {
			return c, errors.New("请填写连接名称。")
		}
		p := Profile{MaxTokens: 4096, Stream: true, FirstTimeout: 180, IdleTimeout: 120, TaskTimeout: 15}
		if bytes.Equal(item, []byte("null")) || json.Unmarshal(item, &p) != nil {
			return c, errors.New("模型配置字段类型无效。")
		}
		base, ok := Defaults[p.Protocol]
		if !ok {
			return c, fmt.Errorf("%s: 不支持的接口类型。", name)
		}
		p.Model = strings.TrimSpace(p.Model)
		if !ValidReasoningEffort(p.ReasoningEffort) {
			return c, errors.New("思考等级需为服务默认、none、minimal、low、medium、high 或 xhigh。")
		}
		if (requireModel && p.Model == "") || len([]rune(p.Model)) > 200 || strings.IndexFunc(p.Model, unicode.IsControl) >= 0 {
			return c, fmt.Errorf("%s: 请填写有效模型 ID。", name)
		}
		var err error
		p.RelayModels, err = normalizeRelayModels(p.RelayModels)
		if err != nil {
			return c, fmt.Errorf("%s: %w", name, err)
		}
		p.ModelNames, err = normalizeModelNames(p)
		if err != nil {
			return c, fmt.Errorf("%s: %w", name, err)
		}
		p.ModelContextWindows, err = normalizeModelContextWindows(p)
		if err != nil {
			return c, fmt.Errorf("%s: %w", name, err)
		}
		p.ModelCompatibility, err = normalizeModelCompatibility(p)
		if err != nil {
			return c, fmt.Errorf("%s: %w", name, err)
		}
		p.ModelImageInputs = normalizeModelImageInputs(p)
		if p.BaseURL == "" {
			p.BaseURL = base
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return c, errors.New("API 地址必须是无密码、查询参数的 HTTP(S) 地址。")
		}
		p.BaseURL = strings.TrimSuffix(u.String(), "/")
		if p.APIKeyEnv != "" && !shared.EnvPattern.MatchString(p.APIKeyEnv) {
			return c, errors.New("环境变量名称不合法。")
		}
		if p.MaxTokens < 128 || p.MaxTokens > 32000 || p.FirstTimeout < 10 || p.FirstTimeout > 600 || p.IdleTimeout < 10 || p.IdleTimeout > 600 || p.TaskTimeout < 1 || p.TaskTimeout > 60 {
			return c, errors.New("输出长度或等待时间超出允许范围。")
		}
		p.Description = shared.Clip(p.Description, 300)
		c.Models[name] = p
	}
	return c, nil
}

// ValidReasoningEffort 限定可保存的推理档位；空值保持旧配置行为，不向服务强加推理参数。
func ValidReasoningEffort(effort string) bool {
	switch effort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh":
		return true
	default:
		return false
	}
}

// Read 读取最新配置，错误只返回固定说明，不泄露文件片段或 Key。
func (s *ConfigStore) Read() (Config, error) {
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return EmptyConfig(), nil
	}
	if err != nil {
		return Config{}, errors.New("无法读取模型配置，请检查文件权限。")
	}
	return ValidateConfig(data, true)
}

// Save 校验并保存纯配置，不把界面的 hasKey 和 savedName 标记写入磁盘。
func (s *ConfigStore) Save(c Config) error {
	c, err := ValidateConfig(shared.Marshal(c), true)
	if err != nil {
		return err
	}
	for name, p := range c.Models {
		p.HasKey = false
		p.SavedName = nil
		c.Models[name] = p
	}
	return shared.AtomicWrite(s.Path, append(shared.Marshal(c), '\n'), 0600)
}

// Editable 返回隐藏 Key 的副本，原名称用于改名和复制时追溯已保存凭据。
func Editable(c Config) Config {
	result := c
	result.Models = map[string]Profile{}
	for name, p := range c.Models {
		p.RelayModels = append([]string(nil), p.RelayModels...)
		p.ModelNames = maps.Clone(p.ModelNames)
		p.ModelContextWindows = maps.Clone(p.ModelContextWindows)
		p.ModelCompatibility = maps.Clone(p.ModelCompatibility)
		p.ModelImageInputs = maps.Clone(p.ModelImageInputs)
		p.HasKey = p.APIKey != "" || (p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) != "")
		p.APIKey = ""
		source := name
		p.SavedName = &source
		result.Models[name] = p
	}
	return result
}

// ResolveProfile 环境变量优先；变量缺失时明确失败，不回退到保存的旧 Key。
func ResolveProfile(c Config, name string) (Profile, error) {
	p, ok := c.Models[name]
	if !ok {
		return p, errors.New("未配置该模型，请打开配置页面添加。")
	}
	if p.APIKeyEnv != "" {
		p.APIKey = os.Getenv(p.APIKeyEnv)
		if p.APIKey == "" {
			return p, errors.New("未找到配置的 Key 环境变量。")
		}
	}
	return p, nil
}

// MergeKeys 沿用原连接的已保存密钥，修改地址或协议无需重填；显式输入的新 Key 优先。
// savedName 只用于追溯同一连接的改名，不能重复引用来源或覆盖另一连接。
func MergeKeys(data []byte, previous Config, requireModel bool) (Config, error) {
	c, err := ValidateConfig(data, requireModel)
	if err != nil {
		return c, err
	}
	sources := map[string]bool{}
	for name, p := range c.Models {
		source := name
		if p.SavedName != nil {
			source = *p.SavedName
			if _, ok := previous.Models[source]; !ok {
				return c, errors.New("原模型配置已不存在，请刷新后重试。")
			}
			if _, taken := previous.Models[name]; source != name && taken {
				return c, errors.New("连接名称已被另一连接使用。")
			}
			if sources[source] {
				return c, errors.New("同一连接不能重复改名，请通过复制创建副本。")
			}
			sources[source] = true
		}
		old, ok := previous.Models[source]
		if !ok {
			continue
		}
		keep := p.APIKey == "" && p.HasKey && old.APIKey != ""
		if keep {
			p.APIKey = old.APIKey
		}
		c.Models[name] = p
	}
	return c, nil
}

// Redact 清理已知密钥及 Bearer 凭据，任务记录和界面错误均通过此边界。
func Redact(text string, profiles ...Profile) string {
	for _, p := range profiles {
		if p.APIKey != "" {
			text = strings.ReplaceAll(text, p.APIKey, "[REDACTED]")
		}
	}
	return bearerPattern.ReplaceAllString(text, "Bearer [REDACTED]")
}

// RedactValue 仅清理值，保留字段名与结构；修改建议另行标记为不可直接应用。
func RedactValue(value any, p Profile) any {
	switch v := value.(type) {
	case string:
		return Redact(v, p)
	case []any:
		for i, x := range v {
			v[i] = RedactValue(x, p)
		}
	case map[string]any:
		for k, x := range v {
			v[k] = RedactValue(x, p)
		}
	}
	return value
}
