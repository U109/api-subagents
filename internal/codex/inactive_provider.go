package codex

import (
	"bytes"
	"errors"
	"os"

	"github.com/U109/api-subagents/internal/shared"
	toml "github.com/pelletier/go-toml/v2"
)

const inactiveBegin = "# BEGIN API SUBAGENTS INACTIVE PROVIDER"
const inactiveEnd = "# END API SUBAGENTS INACTIVE PROVIDER"

// inactiveProvider 为历史对话保留提供商身份，并复用 Codex 的账号/API Key 登录与对应官方地址。
// 旧对话须改选该登录支持的模型；不写入凭据或本地网关地址，也不会继续转发外部模型请求。
const inactiveProvider = inactiveBegin + `
[model_providers.api_subagents]
name = "OpenAI"
wire_api = "responses"
requires_openai_auth = true
supports_websockets = false
` + inactiveEnd + "\n"

// legacyInactiveProvider 仅用于识别并升级旧版无服务的占位块，不能把历史端口当作可用网关。
const legacyInactiveProvider = inactiveBegin + `
[model_providers.api_subagents]
name = "API Subagents（已关闭，重新开启后继续）"
base_url = "http://127.0.0.1:0/v1"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
` + inactiveEnd + "\n"

// knownInactiveProvider 识别新旧占位的实际配置值，容许格式、注释和显示名称改变，不接受未知路由或凭据。
func knownInactiveProvider(value any) bool {
	for _, text := range []string{inactiveProvider, legacyInactiveProvider} {
		var parsed shared.Object
		_ = toml.Unmarshal([]byte(text), &parsed)
		if providerEqual(value, shared.Obj(parsed["model_providers"])[providerID]) {
			return true
		}
	}
	return false
}

// withoutInactiveProvider 再次开启时按配置值移除已知新旧占位，标记缺失或重排不影响识别；自定义同名表拒绝覆盖。
func withoutInactiveProvider(data []byte) ([]byte, error) {
	var parsed shared.Object
	if toml.Unmarshal(data, &parsed) != nil {
		return nil, errors.New("Codex config.toml 格式无效，未进行修改。")
	}
	value := shared.Obj(parsed["model_providers"])[providerID]
	if value == nil {
		return stripConfigMarkers(data, []string{inactiveBegin, inactiveEnd}, false), nil
	}
	if !knownInactiveProvider(value) {
		return nil, errors.New("model_providers.api_subagents 已包含自定义设置，未覆盖。请为该自定义提供商改名后再开启挟持模式；关闭 App 不受影响。")
	}
	spans, err := providerSpans(data, providerID)
	if err != nil {
		return nil, err
	}
	result, _ := takeConfigSpans(data, spans)
	return stripConfigMarkers(result, []string{inactiveBegin, inactiveEnd}, false), nil
}

// withInactiveProvider 仅补足缺失入口或升级已知旧占位；用户自定义的同名表保留原样，不阻止退出 App。
func withInactiveProvider(data []byte) ([]byte, error) {
	var parsed shared.Object
	if toml.Unmarshal(data, &parsed) != nil {
		return nil, errors.New("Codex 配置格式无效，未添加历史对话兼容设置。")
	}
	value := shared.Obj(parsed["model_providers"])[providerID]
	var modern shared.Object
	_ = toml.Unmarshal([]byte(inactiveProvider), &modern)
	if value != nil && (!knownInactiveProvider(value) || providerEqual(value, shared.Obj(modern["model_providers"])[providerID])) {
		return data, nil
	}
	clean, err := withoutInactiveProvider(data)
	if err != nil {
		return nil, err
	}
	if value == nil || (len(clean) > 0 && clean[len(clean)-1] != '\n') {
		clean = append(clean, '\n')
	}
	result := append(clean, []byte(inactiveProvider)...)
	if toml.Unmarshal(result, &parsed) != nil {
		return nil, errors.New("历史对话兼容设置校验失败，未写入。")
	}
	return result, nil
}

// repairInactiveProvider 修复旧版本关闭后缺失的提供商；只在本机留有本应用模型目录时生效，不读取聊天记录。
func (c CodexConfig) repairInactiveProvider() error {
	if _, err := os.Stat(c.catalogPath()); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	current, err := os.ReadFile(c.configPath())
	if err != nil && !os.IsNotExist(err) {
		return errors.New("无法读取 Codex 配置，未修复历史对话入口。")
	}
	var parsed shared.Object
	if toml.Unmarshal(current, &parsed) != nil {
		return errors.New("Codex config.toml 格式无效，未修改配置。")
	}
	if hasManagedRoute(parsed) {
		return errors.New("发现未恢复的挟持配置，请保留备份并检查。")
	}
	// 没有磁盘备份时不删除 PRESERVED 注释，其中可能仍有用户需要找回的原始设置。
	clean := stripConfigMarkers(current, []string{defaultsBegin, defaultsEnd, providerBegin, providerEnd}, false)
	repaired, err := withInactiveProvider(clean)
	if err != nil || bytes.Equal(current, repaired) {
		return err
	}
	latest, err := os.ReadFile(c.configPath())
	if (err != nil && !os.IsNotExist(err)) || !bytes.Equal(current, latest) {
		return errors.New("Codex 配置刚刚发生变化，请重新打开 App 后重试。")
	}
	return shared.AtomicWrite(c.configPath(), repaired, 0600)
}
