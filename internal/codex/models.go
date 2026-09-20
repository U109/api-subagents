package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"

	configstore "github.com/U109/api-subagents/internal/config"
)

// ModelEntry 只携带展示和本地路由信息，两个模型目录共用它，不能包含地址或凭据。
type ModelEntry struct {
	Slug, Name, Description, ReasoningEffort string
	ContextWindow                            int
	SupportsImages                           bool
}

// relayConnectionAlias 将自由文本名称编码为单个路由段；旧版连接名称保持原别名不变。
func relayConnectionAlias(connection string) string {
	return relayModel + "/" + url.PathEscape(connection)
}

// RelayModelAlias 由连接名与模型 ID 生成稳定别名，模型重排、描述或密钥变化不会使历史对话失效。
// 连接名按路径段编码，模型 ID 使用完整摘要，斜线、百分号和 Unicode 不会混淆路由边界。
func RelayModelAlias(connection, model string) string {
	digest := sha256.Sum256([]byte(model))
	return relayConnectionAlias(connection) + "/" + hex.EncodeToString(digest[:])
}

// ModelEntries 为 Codex 和本地 /models 提供同一有序目录，使用自定义显示名称且保留稳定路由。
// 默认模型与额外模型都使用固定模型别名；只有“跟随 App”追踪默认值，改默认不误切已选具体模型。
func ModelEntries(config configstore.Config, defaultName string) []ModelEntry {
	selected := config.Models[defaultName]
	entries := []ModelEntry{{Slug: relayModel, Name: "跟随 App 选择", Description: "使用 API Subagents 当前选中的连接", ReasoningEffort: selected.ReasoningEffort, ContextWindow: selected.ContextWindow(selected.Model), SupportsImages: selected.SupportsImages(selected.Model)}}
	names := make([]string, 0, len(config.Models))
	for name := range config.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		profile := config.Models[name]
		for _, model := range profile.OrderedModels() {
			entries = append(entries, ModelEntry{Slug: RelayModelAlias(name, model), Name: name + " · " + profile.ModelName(model), Description: profile.Description, ReasoningEffort: profile.ReasoningEffort, ContextWindow: profile.ContextWindow(model), SupportsImages: profile.SupportsImages(model)})
		}
	}
	return entries
}

// ResolveCatalogModel 解析当前配置中的本地别名；旧对话的真实模型 ID 只允许匹配已配置模型。
// 连接名仅解码一次；返回请求独享的副本，切换模型不会修改默认模型、地址或 Key。
func ResolveCatalogModel(config configstore.Config, defaultName, alias string) (string, configstore.Profile, error) {
	connection, suffix := defaultName, ""
	if alias != "" && alias != relayModel {
		if !strings.HasPrefix(alias, relayModel+"/") {
			return resolveOriginalModel(config, defaultName, alias)
		}
		var hasSuffix bool
		connection, suffix, hasSuffix = strings.Cut(strings.TrimPrefix(alias, relayModel+"/"), "/")
		if hasSuffix && suffix == "" {
			return "", configstore.Profile{}, errors.New("挟持模型标识无效，请重启 Codex 刷新模型列表。")
		}
		var err error
		connection, err = url.PathUnescape(connection)
		if err != nil {
			return "", configstore.Profile{}, errors.New("挟持模型标识无效，请重启 Codex 刷新模型列表。")
		}
	}
	profile, exists := config.Models[connection]
	if !exists {
		return "", configstore.Profile{}, errors.New("该连接已移除，请重启 Codex 刷新模型列表。")
	}
	model := profile.Model
	if suffix != "" {
		matched := alias == RelayModelAlias(connection, model)
		for _, candidate := range profile.RelayModels {
			if alias == RelayModelAlias(connection, candidate) {
				model, matched = candidate, true
				break
			}
		}
		if !matched {
			return "", configstore.Profile{}, errors.New("该挟持模型已移除，请重启 Codex 刷新模型列表。")
		}
	}
	resolved, err := configstore.ResolveProfile(config, connection)
	if err != nil {
		return "", configstore.Profile{}, err
	}
	resolved.Model = model
	return connection, resolved, nil
}

// resolveOriginalModel 让原自定义提供商的旧对话继续使用已配置模型；优先当前连接，其余重名拒绝猜测凭据。
func resolveOriginalModel(config configstore.Config, defaultName, model string) (string, configstore.Profile, error) {
	connection := ""
	for name, profile := range config.Models {
		matched := profile.Model == model
		for _, candidate := range profile.RelayModels {
			matched = matched || candidate == model
		}
		if !matched {
			continue
		}
		if name == defaultName {
			connection = name
			break
		}
		if connection != "" {
			connection = "\x00"
		} else {
			connection = name
		}
	}
	if connection == "" || connection == "\x00" {
		return "", configstore.Profile{}, errors.New("请在 Codex 中选择具体的挟持模型；原模型未配置或对应多个连接")
	}
	profile, err := configstore.ResolveProfile(config, connection)
	profile.Model = model
	return connection, profile, err
}
