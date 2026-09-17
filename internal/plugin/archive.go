package plugin

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"regexp"

	"github.com/U109/api-subagents/internal/shared"
)

// PayloadVersion 读取插件自身的稳定版本，桌面内嵌版本和在线版本均不借用 App 版本号。
func PayloadVersion(source fs.FS) (string, error) {
	data, err := fs.ReadFile(source, ".codex-plugin/plugin.json")
	var manifest struct{ Name, Version string }
	if err != nil || json.Unmarshal(data, &manifest) != nil || manifest.Name != "api-subagents" || !regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`).MatchString(manifest.Version) {
		return "", errors.New("插件版本信息无效。")
	}
	return manifest.Version, nil
}

// OpenUpdateArchive 在写入任何文件前验证完整白名单、CRC、展开大小、版本与固定 MCP 入口；不解压到磁盘。
func OpenUpdateArchive(data []byte, expectedVersion string) (fs.FS, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("插件更新包不是有效的 ZIP。")
	}
	allowed := map[string]bool{"bin/api-subagents-worker.exe": true}
	for _, name := range PluginFiles {
		allowed[name] = true
	}
	if len(reader.File) != len(allowed) {
		return nil, errors.New("插件更新包的文件清单不完整。")
	}
	var total uint64
	for _, file := range reader.File {
		limit := uint64(2 * 1024 * 1024)
		if file.Name == "bin/api-subagents-worker.exe" {
			limit = 64 * 1024 * 1024
		}
		total += file.UncompressedSize64
		if !allowed[file.Name] || !file.Mode().IsRegular() || file.UncompressedSize64 > limit || total > 80*1024*1024 {
			return nil, errors.New("插件更新包包含不允许的文件或超出大小限制。")
		}
		delete(allowed, file.Name)
		stream, openErr := file.Open()
		if openErr != nil {
			return nil, errors.New("无法读取插件更新包。")
		}
		size, readErr := io.Copy(io.Discard, io.LimitReader(stream, int64(limit)+1))
		stream.Close()
		if readErr != nil || uint64(size) != file.UncompressedSize64 {
			return nil, errors.New("插件更新包内容校验失败。")
		}
	}
	version, err := PayloadVersion(reader)
	if err != nil || version != expectedVersion {
		return nil, errors.New("插件更新包与发布版本不一致。")
	}
	mcp, err := fs.ReadFile(reader, ".mcp.json")
	var value shared.Object
	if err != nil || json.Unmarshal(mcp, &value) != nil {
		return nil, errors.New("插件 MCP 配置无效。")
	}
	servers := shared.Obj(value["mcpServers"])
	server := shared.Obj(servers["api-subagents"])
	if len(servers) != 1 || server["command"] != "./bin/api-subagents-worker.exe" || server["env"] != nil || len(shared.Arr(server["args"])) != 0 || server["cwd"] != "." {
		return nil, errors.New("插件更新包必须使用固定的本地 Worker 入口。")
	}
	for key := range server {
		if !shared.Contains([]string{"command", "args", "cwd", "env_vars", "startup_timeout_sec", "tool_timeout_sec"}, key) {
			return nil, errors.New("插件更新包包含不支持的 MCP 选项。")
		}
	}
	return reader, nil
}
