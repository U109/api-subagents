//go:build !windows

package codex

import "errors"

// readDatabaseProviderIDs 明确阻止非 Windows 环境遗漏仅保存在 SQLite 中的旧身份；无索引时仍支持 rollout 元数据。
func readDatabaseProviderIDs(path string) ([]string, error) {
	return nil, errors.New("当前平台不支持读取 Codex 会话索引；此桌面 App 仅支持 Windows，未修改配置。")
}
