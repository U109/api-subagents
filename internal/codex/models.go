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
// 当前连接的星标模型置顶作为 Codex 目录默认项，其余模型保持已保存顺序；旧默认别名仅用于历史兼容。
func ModelEntries(config configstore.Config, defaultName string) []ModelEntry {
	entries := []ModelEntry{}
	if selected, exists := config.Models[defaultName]; exists && selected.Model != "" {
		entries = append(entries, modelEntry(defaultName, selected, selected.Model))
	}
	names := make([]string, 0, len(config.Models))
	for name := range config.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		profile := config.Models[name]
		for _, model := range profile.OrderedModels() {
			if name == defaultName && model == profile.Model {
				continue
			}
			entries = append(entries, modelEntry(name, profile, model))
		}
	}
	return entries
}

// modelEntry 将一个具体模型转成无凭据的目录项，置顶默认项和普通项共用相同稳定别名、名称及容量规则。
func modelEntry(connection string, profile configstore.Profile, model string) ModelEntry {
	return ModelEntry{Slug: RelayModelAlias(connection, model), Name: connection + " · " + profile.ModelName(model), Description: profile.Description, ReasoningEffort: profile.ReasoningEffort, ContextWindow: profile.ContextWindow(model), SupportsImages: profile.SupportsImages(model)}
}

// ResolveCatalogModel 解析本地别名；旧对话的真实模型名跟随 App 当前连接，不再尝试使用旧上游模型或凭据。
// 连接名仅解码一次；返回请求独享的副本，切换模型不会修改默认模型、地址或 Key。
func ResolveCatalogModel(config configstore.Config, defaultName, alias string) (string, configstore.Profile, error) {
	connection, model, err := resolveCatalogSelection(config, defaultName, alias)
	if err != nil {
		return "", configstore.Profile{}, err
	}
	resolved, err := configstore.ResolveProfile(config, connection)
	if err != nil {
		return "", configstore.Profile{}, err
	}
	resolved.Model = model
	return connection, resolved, nil
}

// resolveCatalogSelection 只验证目录别名及对应模型，不读取 Key 环境变量，避免将缺失凭据误判为模型已删除。
func resolveCatalogSelection(config configstore.Config, defaultName, alias string) (string, string, error) {
	connection, suffix := defaultName, ""
	if strings.HasPrefix(alias, relayModel+"/") {
		var hasSuffix bool
		connection, suffix, hasSuffix = strings.Cut(strings.TrimPrefix(alias, relayModel+"/"), "/")
		if hasSuffix && suffix == "" {
			return "", "", errors.New("挟持模型标识无效，请重启 Codex 刷新模型列表。")
		}
		var err error
		connection, err = url.PathUnescape(connection)
		if err != nil {
			return "", "", errors.New("挟持模型标识无效，请重启 Codex 刷新模型列表。")
		}
	}
	profile, exists := config.Models[connection]
	if !exists {
		return "", "", errors.New("该连接已移除，请重启 Codex 刷新模型列表。")
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
			return "", "", errors.New("该挟持模型已移除，请重启 Codex 刷新模型列表。")
		}
	}
	return connection, model, nil
}
