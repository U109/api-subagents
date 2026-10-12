package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/U109/api-subagents/internal/shared"
	"maps"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// legacyConnectionID 为旧自由名称提供稳定身份；过长或含控制字符的名称使用摘要，避免迁移拒绝既有连接。
func legacyConnectionID(name string) string {
	if len(name) <= 200 && strings.IndexFunc(name, unicode.IsControl) < 0 {
		return name
	}
	digest := sha256.Sum256([]byte(name))
	return "legacy-" + hex.EncodeToString(digest[:])
}

// AccessConfig 保存配置级连接及模型子集；默认项使用 JSON 二元组，避免连接名和模型 ID 的分隔符冲突。
type AccessConfig struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	ConnectionIDs []string            `json:"connectionIDs"`
	ModelIDs      map[string][]string `json:"modelIDs"`
	DefaultKey    string              `json:"defaultKey"`
}

// CloneAccess 深复制独立配置，避免草稿或启用快照与磁盘配置共享切片。
func CloneAccess(p *AccessConfig) *AccessConfig {
	if p == nil {
		return nil
	}
	next := *p
	next.ConnectionIDs = slices.Clone(p.ConnectionIDs)
	next.ModelIDs = make(map[string][]string, len(p.ModelIDs))
	for id, models := range p.ModelIDs {
		next.ModelIDs[id] = slices.Clone(models)
	}
	return &next
}

// CloneAccessConfigs 隔离配置列表及每项的模型范围，空列表保持 JSON 数组。
func CloneAccessConfigs(plans []AccessConfig) []AccessConfig {
	result := make([]AccessConfig, 0, len(plans))
	for _, p := range plans {
		result = append(result, *CloneAccess(&p))
	}
	return result
}

// FindConnection 按稳定身份查找连接；名称修改不改变接入配置中的引用。
func (c Config) FindConnection(id string) (string, Profile, bool) {
	for name, p := range c.Models {
		if p.ID == id || p.ID == "" && name == id {
			return name, p, true
		}
	}
	return "", Profile{}, false
}

// validateAccessStructure 校验可持久化结构而非引用可用性；移除模型后允许保留失效引用供用户修复，不会扩大为全部模型。
func validateAccessStructure(c Config) error {
	if len(c.AccessConfigs) > 500 {
		return errors.New("最多保存 500 个接入配置。")
	}
	ids := map[string]bool{}
	for _, p := range append(CloneAccessConfigs(c.AccessConfigs), dereferenceAccess(c.LastAccess)...) {
		if p.ID == "" || len(p.ID) > 200 || strings.IndexFunc(p.ID, unicode.IsControl) >= 0 || strings.TrimSpace(p.Name) == "" || len([]rune(p.Name)) > 60 || strings.IndexFunc(p.Name, unicode.IsControl) >= 0 {
			return errors.New("接入配置名称或标识无效。")
		}
		if len(p.ConnectionIDs) > 50 || len(p.ModelIDs) > 50 {
			return errors.New("接入配置包含过多连接。")
		}
		seen := map[string]bool{}
		for _, id := range p.ConnectionIDs {
			if id == "" || seen[id] {
				return errors.New("接入配置包含重复或无效连接。")
			}
			seen[id] = true
			modelSeen := map[string]bool{}
			if len(p.ModelIDs[id]) > 500 {
				return errors.New("接入配置包含过多模型。")
			}
			for _, model := range p.ModelIDs[id] {
				if model == "" || len([]rune(model)) > 200 || strings.IndexFunc(model, unicode.IsControl) >= 0 || modelSeen[model] {
					return errors.New("接入配置包含重复或无效模型。")
				}
				modelSeen[model] = true
			}
		}
		for id := range p.ModelIDs {
			if !seen[id] {
				return errors.New("接入配置包含未加入的连接模型。")
			}
		}
	}
	for _, p := range c.AccessConfigs {
		if ids[p.ID] {
			return errors.New("接入配置标识重复。")
		}
		ids[p.ID] = true
	}
	return nil
}

// dereferenceAccess 将可选快照转为列表，只用于统一结构校验。
func dereferenceAccess(p *AccessConfig) []AccessConfig {
	if p == nil {
		return nil
	}
	return []AccessConfig{*p}
}

// ValidateAccess 检查明确选择的连接、模型和默认项；空集合或失效引用必须修复，不能隐式回退。
func (c Config) ValidateAccess(p AccessConfig) error {
	if err := validateAccessStructure(Config{AccessConfigs: []AccessConfig{p}}); err != nil {
		return err
	}
	if len(p.ConnectionIDs) == 0 {
		return errors.New("请至少添加一个连接。")
	}
	var key []string
	if json.Unmarshal([]byte(p.DefaultKey), &key) != nil || len(key) != 2 || !slices.Contains(p.ConnectionIDs, key[0]) || !slices.Contains(p.ModelIDs[key[0]], key[1]) {
		return errors.New("请星标一个已选模型作为此配置的默认模型。")
	}
	count := 0
	for _, id := range p.ConnectionIDs {
		_, profile, ok := c.FindConnection(id)
		if !ok {
			return errors.New("包含未保存或已删除的连接，请先保存连接。")
		}
		for _, model := range p.ModelIDs[id] {
			if !slices.Contains(profile.OrderedModels(), model) {
				return errors.New("有模型已不在连接的可用范围内，请取消这些模型或恢复连接。")
			}
			count++
		}
	}
	if count == 0 {
		return errors.New("请在 Codex 模型预览中至少选择一个模型。")
	}
	return nil
}

// ScopedConfig 复制配置选择的模型及默认项；连接模型目录保持原样，配置级默认不回写连接的星标。
func (c Config) ScopedConfig(p AccessConfig) (Config, string, error) {
	if err := c.ValidateAccess(p); err != nil {
		return Config{}, "", err
	}
	var key []string
	_ = json.Unmarshal([]byte(p.DefaultKey), &key)
	result := EmptyConfig()
	defaultName := ""
	for _, id := range p.ConnectionIDs {
		models := p.ModelIDs[id]
		if len(models) == 0 {
			continue
		}
		name, profile, _ := c.FindConnection(id)
		profile.Model = models[0]
		if id == key[0] {
			profile.Model, defaultName = key[1], name
		}
		profile.RelayModels = slices.DeleteFunc(slices.Clone(models), func(model string) bool { return model == profile.Model })
		profile.ModelOrder = slices.Clone(models)
		result.Models[name] = profile
	}
	return result, defaultName, nil
}

// AccessFingerprint 计算所选范围的非凭据参数指纹；新增未选模型或轮换 Key 不影响恢复，已选参数改变则需要明确重新启用。
func (c Config) AccessFingerprint(p AccessConfig) (string, error) {
	scope, _, err := c.ScopedConfig(p)
	if err != nil {
		return "", err
	}
	for name, profile := range scope.Models {
		profile.ModelCatalog = nil
		profile.APIKey, profile.APIKeyEnv = "", ""
		profile.HasKey = false
		profile.SavedName, profile.CopySource = nil, nil
		scope.Models[name] = profile
	}
	scope, err = ValidateConfig(shared.Marshal(scope), true)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(shared.Marshal(scope))
	return hex.EncodeToString(digest[:]), nil
}

// LastAccessCurrent 判断磁盘连接参数是否仍与最后启用快照一致，供重载页面时正确标注使用状态，不将保存误报为生效。
func (c Config) LastAccessCurrent() bool {
	if c.LastAccess == nil || c.LastAccessFingerprint == "" {
		return true
	}
	fingerprint, err := c.AccessFingerprint(*c.LastAccess)
	return err == nil && fingerprint == c.LastAccessFingerprint
}

// RemoveAccessConnection 清理被删除连接的配置引用及默认项；最后启用快照受影响时作废，不扩大剩余范围。
func (c *Config) RemoveAccessConnection(id string) {
	for i := range c.AccessConfigs {
		p := &c.AccessConfigs[i]
		p.ConnectionIDs = slices.DeleteFunc(p.ConnectionIDs, func(value string) bool { return value == id })
		p.ModelIDs = maps.Clone(p.ModelIDs)
		delete(p.ModelIDs, id)
		var key []string
		if json.Unmarshal([]byte(p.DefaultKey), &key) == nil && len(key) == 2 && key[0] == id {
			p.DefaultKey = ""
		}
	}
	if c.LastAccess != nil && slices.Contains(c.LastAccess.ConnectionIDs, id) {
		c.LastAccess = nil
		c.LastAccessFingerprint = ""
	}
}

// MigrateAccess 为旧版已有连接生成一份未启用的配置；首次使用和显式空配置不注入示例连接。
func (c *Config) MigrateAccess() {
	if len(c.Models) == 0 || len(c.AccessConfigs) != 0 {
		return
	}
	names := make([]string, 0, len(c.Models))
	for name := range c.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	p := AccessConfig{ID: "legacy", Name: "默认配置", ModelIDs: map[string][]string{}}
	for _, name := range names {
		profile := c.Models[name]
		id := profile.ID
		if id == "" {
			id = name
		}
		p.ConnectionIDs = append(p.ConnectionIDs, id)
		p.ModelIDs[id] = profile.OrderedModels()
		if p.DefaultKey == "" && profile.Model != "" {
			key, _ := json.Marshal([]string{id, profile.Model})
			p.DefaultKey = string(key)
		}
	}
	c.AccessConfigs = []AccessConfig{p}
}
