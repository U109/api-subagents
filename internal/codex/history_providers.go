package codex

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// historyProviderIDs 只读收集会话索引和旧版 rollout 首条元数据中的提供商身份，不读取消息或修改记录。
// 优先最新版本索引并兼容未迁移的活动、归档会话；无法读取时停止接管，避免悄悄遗漏旧对话。
func (c CodexConfig) historyProviderIDs() ([]string, error) {
	entries, err := os.ReadDir(c.Home)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("无法读取 Codex 会话索引，未修改配置。")
	}
	database, version := "", -1
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "state_") || !strings.HasSuffix(name, ".sqlite") {
			continue
		}
		value, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "state_"), ".sqlite"))
		if err == nil && value > version {
			database, version = filepath.Join(c.Home, name), value
		}
	}
	providers := map[string]bool{}
	if database != "" {
		ids, err := readDatabaseProviderIDs(database)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			providers[id] = true
		}
	}
	for _, folder := range []string{"sessions", "archived_sessions"} {
		root := filepath.Join(c.Home, folder)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if os.IsNotExist(walkErr) && path == root {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "rollout-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			id, err := rolloutProviderID(path)
			if err != nil {
				return err
			}
			providers[id] = true
			return nil
		})
		if err != nil {
			return nil, errors.New("无法读取 Codex 历史会话元数据，未修改配置或聊天记录。")
		}
	}
	delete(providers, "")
	ids := make([]string, 0, len(providers))
	for id := range providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// rolloutProviderID 仅解析旧会话文件第一条有界元数据，后面的聊天消息即使损坏也不读取或改写。
func rolloutProviderID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var header struct {
		Type    string `json:"type"`
		Payload struct {
			Provider string `json:"model_provider"`
		} `json:"payload"`
	}
	if err := json.NewDecoder(io.LimitReader(file, 1024*1024)).Decode(&header); err != nil {
		return "", err
	}
	if header.Type != "session_meta" {
		return "", errors.New("历史会话缺少首条元数据。")
	}
	return header.Payload.Provider, nil
}
