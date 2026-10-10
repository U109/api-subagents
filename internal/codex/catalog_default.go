package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"

	toml "github.com/pelletier/go-toml/v2"
)

// withCatalogDefault 仅替换唯一的顶层模型字段，保留其他设置、提供商及恢复标记；缺失或重复时拒绝猜测。
func withCatalogDefault(data []byte, alias string) ([]byte, error) {
	spans, err := RootKeySpans(data, map[string]bool{"model": true})
	if err != nil || len(spans) != 1 {
		return nil, errors.New("Codex 默认模型字段无效，配置和备份已保留。")
	}
	span := spans[0]
	result := append([]byte{}, data[:span[0]]...)
	result = append(result, []byte(fmt.Sprintf("model = %q\n", alias))...)
	return append(result, data[span[1]:]...), nil
}

// RefreshCatalog 更新具体模型目录及托管默认选择；切换连接强制选中星标模型，普通保存保留手动选择的有效具体模型。
// 旧默认别名和已删除模型回退到当前星标模型；先验证恢复边界，备份始终保留开启前的原始配置。
func (c CodexConfig) RefreshCatalog(config configstore.Config, connection string, selectDefault bool) error {
	profile, exists := config.Models[connection]
	if !exists {
		return errors.New("该连接已移除，未更新默认模型。")
	}
	backupData, err := os.ReadFile(c.backupPath())
	if err != nil {
		return errors.New("无法读取 Codex 配置备份，未更新默认模型。")
	}
	var backup codexBackup
	if json.Unmarshal(backupData, &backup) != nil {
		return errors.New("Codex 配置备份格式无效，未更新默认模型。")
	}
	current, err := os.ReadFile(c.configPath())
	if err != nil {
		return errors.New("无法读取 Codex 配置，未更新默认模型。")
	}
	if _, err := RestoreCodexConfig(current, backup); err != nil {
		return err
	}
	var now, previous shared.Object
	if toml.Unmarshal(current, &now) != nil || toml.Unmarshal(backup.Written, &previous) != nil {
		return errors.New("Codex 配置格式无效，未更新默认模型。")
	}
	selected := shared.Str(now["model"])
	managed := selected == relayModel || strings.HasPrefix(selected, relayModel+"/")
	if selectDefault && !managed {
		return errors.New("Codex 已切换其他模型，请先恢复接入配置再切换连接。")
	}
	alias := RelayModelAlias(connection, profile.Model)
	updated := current
	_, _, selectionErr := resolveCatalogSelection(config, connection, selected)
	if managed && (selectDefault || selected == shared.Str(previous["model"]) || selected == relayModel || selected == relayConnectionAlias(backup.Model) || selectionErr != nil) {
		updated, err = withCatalogDefault(current, alias)
		if err != nil {
			return err
		}
	}
	written, err := withCatalogDefault(backup.Written, alias)
	if err != nil {
		return err
	}
	latest, err := os.ReadFile(c.configPath())
	latestBackup, backupErr := os.ReadFile(c.backupPath())
	if err != nil || backupErr != nil || !bytes.Equal(latest, current) || !bytes.Equal(latestBackup, backupData) {
		return errors.New("Codex 配置刚刚发生变化，请重试。")
	}
	if err := c.WriteCatalog(config, connection); err != nil {
		return err
	}
	backup.Written, backup.Model = written, connection
	if err := shared.AtomicWrite(c.backupPath(), shared.Marshal(backup), 0600); err != nil {
		return err
	}
	if !bytes.Equal(updated, current) {
		if err := shared.AtomicWrite(c.configPath(), updated, 0600); err != nil {
			_ = shared.AtomicWrite(c.backupPath(), backupData, 0600)
			return err
		}
	}
	return nil
}
