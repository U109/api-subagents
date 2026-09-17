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

// inactiveProvider 只为历史对话保留提供商身份；保留端口 0、不含凭据，不会继续转发外部模型请求。
const inactiveProvider = inactiveBegin + `
[model_providers.api_subagents]
name = "API Subagents（已关闭，重新开启后继续）"
base_url = "http://127.0.0.1:0/v1"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
` + inactiveEnd + "\n"

// withoutInactiveProvider 再次开启时仅移除本应用未被修改的占位块；手动修改或重复标记均保留并报错。
func withoutInactiveProvider(data []byte) ([]byte, error) {
	if !bytes.Contains(data, []byte(inactiveBegin)) && !bytes.Contains(data, []byte(inactiveEnd)) {
		return data, nil
	}
	start, end, err := managedBlock(data, inactiveBegin, inactiveEnd)
	if err != nil || !bytes.Equal(data[start:end], []byte(inactiveProvider)) {
		return nil, errors.New("历史对话的提供商占位配置已被修改，请先检查 config.toml。")
	}
	return append(append([]byte{}, data[:start]...), data[end:]...), nil
}

// withInactiveProvider 保留恢复后的默认模型和所有原配置，只补足旧对话引用的提供商定义。
func withInactiveProvider(data []byte) ([]byte, error) {
	var parsed shared.Object
	if toml.Unmarshal(data, &parsed) != nil {
		return nil, errors.New("Codex 配置格式无效，未添加历史对话兼容设置。")
	}
	if shared.Obj(parsed["model_providers"])[providerID] != nil {
		return data, nil
	}
	result := append(append([]byte{}, data...), []byte("\n"+inactiveProvider)...)
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
	if bytes.Contains(current, []byte(defaultsBegin)) || bytes.Contains(current, []byte(providerBegin)) {
		return errors.New("发现未恢复的挟持配置，请保留备份并检查。")
	}
	repaired, err := withInactiveProvider(current)
	if err != nil || bytes.Equal(current, repaired) {
		return err
	}
	latest, err := os.ReadFile(c.configPath())
	if (err != nil && !os.IsNotExist(err)) || !bytes.Equal(current, latest) {
		return errors.New("Codex 配置刚刚发生变化，请重新打开 App 后重试。")
	}
	return shared.AtomicWrite(c.configPath(), repaired, 0600)
}
