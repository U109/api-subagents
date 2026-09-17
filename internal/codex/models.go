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
// 默认模型自动加入，额外模型按保存顺序展示；改名不影响已有对话或实际调用的模型 ID。
func ModelEntries(config configstore.Config, defaultName string) []ModelEntry {
	entries := []ModelEntry{{Slug: relayModel, Name: "跟随 App 选择", Description: "使用 API Subagents 当前选中的连接", ReasoningEffort: config.Models[defaultName].ReasoningEffort}}
	names := make([]string, 0, len(config.Models))
	for name := range config.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		profile := config.Models[name]
		entries = append(entries, ModelEntry{Slug: relayConnectionAlias(name), Name: name + " · " + profile.ModelName(profile.Model), Description: profile.Description, ReasoningEffort: profile.ReasoningEffort})
		seen := map[string]bool{profile.Model: true}
		for _, model := range profile.RelayModels {
			if seen[model] {
				continue
			}
			seen[model] = true
			entries = append(entries, ModelEntry{Slug: RelayModelAlias(name, model), Name: name + " · " + profile.ModelName(model), Description: profile.Description, ReasoningEffort: profile.ReasoningEffort})
		}
	}
	return entries
}

// ResolveCatalogModel 只接受当前配置中的本地别名，继续支持跟随 App 和旧版连接别名。
// 连接名仅解码一次；返回请求独享的副本，切换模型不会修改默认模型、地址或 Key。
func ResolveCatalogModel(config configstore.Config, defaultName, alias string) (string, configstore.Profile, error) {
	connection, suffix := defaultName, ""
	if alias != "" && alias != relayModel {
		if !strings.HasPrefix(alias, relayModel+"/") {
			return "", configstore.Profile{}, errors.New("请选择 API Subagents 挟持模型列表中的模型。")
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
