package codex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

const defaultsBegin = "# BEGIN API SUBAGENTS DEFAULTS"

const defaultsEnd = "# END API SUBAGENTS DEFAULTS"

const providerBegin = "# BEGIN API SUBAGENTS PROVIDER"

const providerEnd = "# END API SUBAGENTS PROVIDER"

const preservedPrefix = "# API SUBAGENTS PRESERVED "

const relayModel = "api-subagents"

const providerID = "api_subagents"

type codexBackup struct {
	Original []byte `json:"original"`
	Written  []byte `json:"written"`
	Existed  bool   `json:"existed"`
	Model    string `json:"model"`
	Port     int    `json:"port"`
}

type CodexConfig struct{ Home, DataRoot string }

// configPath 将配置限定在指定 CODEX_HOME，测试使用临时目录，不触碰当前会话。
func (c CodexConfig) configPath() string { return filepath.Join(c.Home, "config.toml") }

// backupPath 备份仅保存在本机数据目录，发布清单不读取该目录。
func (c CodexConfig) backupPath() string { return filepath.Join(c.DataRoot, "codex-relay-backup.json") }

// catalogPath 返回 Codex 启动时加载的模型目录，条目只含显示信息与本地路由别名。
func (c CodexConfig) catalogPath() string {
	return filepath.Join(c.DataRoot, "codex-model-catalog.json")
}

// RootKeySpans 通过 TOML 语法树找到指定顶层字段，支持引号和多行字符串，不误改子表。
func RootKeySpans(data []byte, keys map[string]bool) ([][2]int, error) {
	var check shared.Object
	if toml.Unmarshal(data, &check) != nil {
		return nil, errors.New("Codex config.toml 格式无效，未进行修改。")
	}
	parser := unstable.Parser{}
	parser.Reset(data)
	root := true
	spans := [][2]int{}
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind == unstable.Table || node.Kind == unstable.ArrayTable {
			root = false
		}
		if !root || node.Kind != unstable.KeyValue {
			continue
		}
		iterator := node.Key()
		parts := []string{}
		start := -1
		for iterator.Next() {
			n := iterator.Node()
			parts = append(parts, string(n.Data))
			if start < 0 {
				start = int(n.Raw.Offset)
			}
		}
		if len(parts) != 1 || !keys[parts[0]] {
			continue
		}
		value := node.Value()
		end := int(value.Raw.Offset + value.Raw.Length)
		if start < 0 || end < start || end > len(data) {
			return nil, errors.New("无法安全定位 Codex 顶层配置。")
		}
		for start > 0 && data[start-1] != '\n' {
			start--
		}
		for end < len(data) && data[end] != '\n' {
			end++
		}
		if end < len(data) {
			end++
		}
		spans = append(spans, [2]int{start, end})
	}
	if parser.Error() != nil {
		return nil, errors.New("无法解析 Codex 配置。")
	}
	return spans, nil
}

// PatchCodexConfig 接管模型、目录及原自定义提供商；旧对话的提供商身份保持可用，关闭时恢复完整原配置。
func PatchCodexConfig(original []byte, catalog string, port int, token string) ([]byte, error) {
	var err error
	original, err = withoutInactiveProvider(original)
	if err != nil {
		return nil, err
	}
	if bytes.Contains(original, []byte("# BEGIN API SUBAGENTS")) || bytes.Contains(original, []byte(preservedPrefix)) {
		return nil, errors.New("发现上次挟持模式的配置，请先恢复后再开启。")
	}
	var parsed shared.Object
	if toml.Unmarshal(original, &parsed) != nil {
		return nil, errors.New("Codex 配置格式无效。")
	}
	if shared.Obj(parsed["model_providers"])[providerID] != nil {
		return nil, errors.New("Codex 已有同名自定义提供商，未覆盖。")
	}
	original, previousProvider, err := preservePreviousProvider(original, parsed)
	if err != nil {
		return nil, err
	}
	spans, err := RootKeySpans(original, map[string]bool{"model": true, "model_provider": true, "model_catalog_json": true, "model_context_window": true, "model_auto_compact_token_limit": true})
	if err != nil {
		return nil, err
	}
	rest := append([]byte{}, original...)
	for index := len(spans) - 1; index >= 0; index-- {
		span := spans[index]
		saved := preservedPrefix + base64.StdEncoding.EncodeToString(original[span[0]:span[1]]) + "\n"
		rest = append(append(append([]byte{}, rest[:span[0]]...), []byte(saved)...), rest[span[1]:]...)
	}
	prefix := fmt.Sprintf("%s\nmodel = %q\nmodel_provider = %q\nmodel_catalog_json = %s\n%s\n", defaultsBegin, relayModel, providerID, string(shared.Marshal(filepath.ToSlash(catalog))), defaultsEnd)
	suffix := "\n" + providerBegin + "\n" + localProviderConfig(providerID, port, token)
	if previousProvider != "" {
		suffix += "\n" + localProviderConfig(fmt.Sprintf("%q", previousProvider), port, token)
	}
	suffix += providerEnd + "\n"
	patched := append(append([]byte(prefix), rest...), []byte(suffix)...)
	if toml.Unmarshal(patched, &parsed) != nil {
		return nil, errors.New("生成的 Codex 配置校验失败。")
	}
	return patched, nil
}

// managedBlock 精确定位一段托管配置，重复或缺少标记时停止恢复，避免猜测覆盖。
func managedBlock(data []byte, start, end string) (int, int, error) {
	begin := bytes.Index(data, []byte(start+"\n"))
	if begin < 0 || bytes.Count(data, []byte(start)) != 1 || bytes.Count(data, []byte(end)) != 1 {
		return 0, 0, errors.New("Codex 托管配置标记已被修改，请保留备份并检查。")
	}
	tail := bytes.Index(data[begin:], []byte(end+"\n"))
	if tail < 0 {
		return 0, 0, errors.New("Codex 托管配置标记不完整。")
	}
	return begin, begin + tail + len(end) + 1, nil
}

// RestoreCodexConfig 完全未变时逐字节恢复；其他配置被编辑时只还原受控字段，保留用户的新修改。
func RestoreCodexConfig(current []byte, backup codexBackup) ([]byte, error) {
	if bytes.Equal(current, backup.Written) {
		return backup.Original, nil
	}
	rest := append([]byte{}, current...)
	for _, pair := range [][2]string{{providerBegin, providerEnd}, {defaultsBegin, defaultsEnd}} {
		start, end, err := managedBlock(rest, pair[0], pair[1])
		if err != nil {
			return nil, err
		}
		oldStart, oldEnd, err := managedBlock(backup.Written, pair[0], pair[1])
		if err != nil {
			return nil, err
		}
		extra := []byte{}
		if !bytes.Equal(rest[start:end], backup.Written[oldStart:oldEnd]) {
			if pair[0] != defaultsBegin {
				return nil, errors.New("Codex 的托管提供商设置已被修改，未自动覆盖；请检查本机备份。")
			}
			// Codex 自己切换模型会改写 model，并可能在此处新增推理设置；这些是正常操作。
			extra, err = restoreSelectedModel(rest[start:end], backup.Written[oldStart:oldEnd], backup)
			if err != nil {
				return nil, err
			}
		}
		rest = append(append(append([]byte{}, rest[:start]...), extra...), rest[end:]...)
	}
	var result bytes.Buffer
	for _, line := range bytes.SplitAfter(rest, []byte("\n")) {
		if bytes.HasPrefix(line, []byte(preservedPrefix)) {
			original, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(line[len(preservedPrefix):])))
			if err != nil {
				return nil, errors.New("原配置恢复标记损坏。")
			}
			result.Write(original)
		} else {
			result.Write(line)
		}
	}
	var parsed shared.Object
	if toml.Unmarshal(result.Bytes(), &parsed) != nil {
		return nil, errors.New("恢复后配置存在冲突，未写入。")
	}
	return result.Bytes(), nil
}

// restoreSelectedModel 允许 Codex 在托管别名、原默认模型和接管的提供商身份之间切换，保留新增的其他顶层设置。
// 提供商、目录或模型被切换到本功能之外时拒绝猜测恢复，备份和正在运行的服务保持可用。
func restoreSelectedModel(current, written []byte, backup codexBackup) ([]byte, error) {
	var now, old shared.Object
	conflict := errors.New("Codex 的托管模型设置已被手动修改，未自动覆盖；请检查本机备份。")
	if toml.Unmarshal(current, &now) != nil || toml.Unmarshal(written, &old) != nil {
		return nil, conflict
	}
	model := shared.Str(now["model"])
	var original, managed shared.Object
	if toml.Unmarshal(backup.Original, &original) != nil {
		return nil, conflict
	}
	start, end, err := managedBlock(backup.Written, providerBegin, providerEnd)
	if err != nil || toml.Unmarshal(backup.Written[start:end], &managed) != nil {
		return nil, conflict
	}
	provider := shared.Str(now["model_provider"])
	providerOK := provider == shared.Str(old["model_provider"]) || (provider == shared.Str(original["model_provider"]) && shared.Obj(managed["model_providers"])[provider] != nil)
	modelOK := model == relayModel || strings.HasPrefix(model, relayModel+"/") || (model != "" && model == shared.Str(original["model"]))
	if !modelOK || !providerOK || now["model_catalog_json"] != old["model_catalog_json"] {
		return nil, conflict
	}
	spans, err := RootKeySpans(current, map[string]bool{"model": true, "model_provider": true, "model_catalog_json": true})
	if err != nil {
		return nil, err
	}
	extra := append([]byte{}, current...)
	for i := len(spans) - 1; i >= 0; i-- {
		span := spans[i]
		extra = append(extra[:span[0]], extra[span[1]:]...)
	}
	extra = bytes.ReplaceAll(extra, []byte(defaultsBegin+"\n"), nil)
	extra = bytes.ReplaceAll(extra, []byte(defaultsEnd+"\n"), nil)
	return extra, nil
}

// WriteCatalog 将共享的挟持模型列表写入 Codex 启动目录，不包含地址、Key 或凭据。
func (c CodexConfig) WriteCatalog(config configstore.Config, defaultName string) error {
	models := []any{}
	for index, entry := range ModelEntries(config, defaultName) {
		models = append(models, catalogModel(entry.Slug, entry.Name, entry.Description, entry.ReasoningEffort, index, entry.ContextWindow, entry.SupportsImages))
	}
	return shared.AtomicWrite(c.catalogPath(), shared.Marshal(shared.Object{"models": models}), 0600)
}

// catalogModel 按模型声明图片输入与上下文容量并在 90% 时压缩，不注入用于约束思考标签或进度语言的额外指令。
// 未确认的容量由配置层保守回退，不启用远端专属搜索、WebSocket 或付费辅助模型。
func catalogModel(slug, name, description, effort string, priority, contextWindow int, supportsImages bool) shared.Object {
	modalities := []string{"text"}
	if supportsImages {
		modalities = append(modalities, "image")
	}
	var defaultEffort any
	if effort != "" {
		defaultEffort = effort
	}
	instructions := "You are a coding assistant running in Codex. Follow the user's request and the provided system and developer instructions. Use available tools according to their permissions. Keep changes focused, inspect project instructions, and verify your work."
	return shared.Object{
		"slug":                                 slug,
		"display_name":                         name,
		"description":                          description,
		"supported_reasoning_levels":           catalogReasoningLevels(),
		"default_reasoning_level":              defaultEffort,
		"shell_type":                           "unified_exec",
		"visibility":                           "list",
		"supported_in_api":                     true,
		"priority":                             priority,
		"availability_nux":                     nil,
		"upgrade":                              nil,
		"model_messages":                       shared.Object{"instructions_template": instructions, "instructions_variables": nil, "approvals": nil, "collaboration_modes": nil, "auto_review": nil, "permissions": nil, "multi_agent": nil},
		"base_instructions":                    instructions,
		"include_skills_usage_instructions":    true,
		"include_plugin_usage_instructions":    true,
		"include_apps_usage_instructions":      true,
		"support_verbosity":                    false,
		"default_verbosity":                    nil,
		"supports_reasoning_summaries":         true,
		"supports_reasoning_summary_parameter": false,
		"apply_patch_tool_type":                "freeform",
		"truncation_policy":                    shared.Object{"mode": "bytes", "limit": 10000},
		"context_window":                       contextWindow,
		"auto_compact_token_limit":             contextWindow * 9 / 10,
		"effective_context_window_percent":     95,
		"supports_parallel_tool_calls":         true,
		"experimental_supported_tools":         []string{},
		"input_modalities":                     modalities,
		"supports_search_tool":                 false,
		"prefer_websockets":                    false,
		"use_responses_lite":                   false,
		"tool_mode":                            "direct",
	}
}

// Enable 先保存完整本机备份，再原子修改 Codex；并发编辑时拒绝覆盖。
func (c CodexConfig) Enable(config configstore.Config, model string, port int, token string) error {
	if _, err := os.Stat(c.backupPath()); err == nil {
		return errors.New("仍有未恢复的 Codex 配置备份，请先关闭或恢复挟持模式。")
	}
	original, err := os.ReadFile(c.configPath())
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return errors.New("无法读取 Codex 配置。")
	}
	written, err := PatchCodexConfig(original, c.catalogPath(), port, token)
	if err != nil {
		return err
	}
	if err = c.WriteCatalog(config, model); err != nil {
		return err
	}
	backup := codexBackup{Original: original, Written: written, Existed: existed, Model: model, Port: port}
	if err = shared.AtomicWrite(c.backupPath(), shared.Marshal(backup), 0600); err != nil {
		return err
	}
	latest, readErr := os.ReadFile(c.configPath())
	if (readErr != nil && !os.IsNotExist(readErr)) || !bytes.Equal(latest, original) {
		_ = os.Remove(c.backupPath())
		return errors.New("Codex 配置刚刚发生变化，请重试。")
	}
	if err = shared.AtomicWrite(c.configPath(), written, 0600); err != nil {
		_ = os.Remove(c.backupPath())
		return err
	}
	return nil
}

// Restore 恢复原默认设置，并为旧对话保留无凭据的停用提供商；外部编辑冲突时保留备份。
func (c CodexConfig) Restore() error {
	data, err := os.ReadFile(c.backupPath())
	if os.IsNotExist(err) {
		return c.repairInactiveProvider()
	}
	if err != nil {
		return errors.New("无法读取 Codex 配置备份。")
	}
	var backup codexBackup
	if json.Unmarshal(data, &backup) != nil || len(backup.Written) == 0 {
		return errors.New("Codex 配置备份无效。")
	}
	current, err := os.ReadFile(c.configPath())
	if err != nil {
		return errors.New("无法读取当前 Codex 配置，备份已保留。")
	}
	if bytes.Equal(current, backup.Original) {
		repaired, repairErr := withInactiveProvider(current)
		if repairErr != nil {
			return repairErr
		}
		if err = shared.AtomicWrite(c.configPath(), repaired, 0600); err != nil {
			return err
		}
		return os.Remove(c.backupPath())
	}
	restored, err := RestoreCodexConfig(current, backup)
	if err != nil {
		return err
	}
	restored, err = withInactiveProvider(restored)
	if err != nil {
		return err
	}
	latest, readErr := os.ReadFile(c.configPath())
	if readErr != nil || !bytes.Equal(latest, current) {
		return errors.New("Codex 配置刚刚发生变化，请重试关闭挟持模式。")
	}
	err = shared.AtomicWrite(c.configPath(), restored, 0600)
	if err != nil {
		return err
	}
	return os.Remove(c.backupPath())
}
