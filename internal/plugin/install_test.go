package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/U109/api-subagents/internal/shared"
)

// fixturePayload 创建完整的合成插件包，不读取本机模型、市场和已安装插件。
func fixturePayload() fstest.MapFS {
	files := fstest.MapFS{}
	for _, path := range PluginFiles {
		files[path] = &fstest.MapFile{Data: []byte("fixture")}
	}
	files[".codex-plugin/plugin.json"] = &fstest.MapFile{Data: []byte(`{"name":"api-subagents","version":"0.3.0"}`)}
	files[".mcp.json"] = &fstest.MapFile{Data: []byte(`{"mcpServers":{"api-subagents":{"command":"./bin/api-subagents-worker.exe","args":[],"tool_timeout_sec":660}}}`)}
	files["bin/api-subagents-worker.exe"] = &fstest.MapFile{Data: []byte("MZ synthetic worker")}
	return files
}

// TestPluginIsolation 验证升级保留其他插件和环境透传，运行文件独立缓存且检测安装状态需有 Codex 缓存。
func TestPluginIsolation(t *testing.T) {
	root := t.TempDir()
	opts := InstallOptions{ProfileRoot: root, DataRoot: t.TempDir(), CodexHome: t.TempDir(), SkipCodex: true}
	marketPath := filepath.Join(root, ".agents", "plugins", "marketplace.json")
	shared.AtomicWrite(marketPath, []byte(`{"name":"my-market","plugins":[{"name":"other","source":{"source":"local","path":"./plugins/other"}}]}`), 0600)
	target := filepath.Join(root, "plugins", "api-subagents")
	shared.AtomicWrite(filepath.Join(target, ".mcp.json"), []byte(`{"mcpServers":{"api-subagents":{"command":"old","tool_timeout_sec":35,"env_vars":["CUSTOM_MODEL_KEY","BAD-NAME"],"env":{"OLD_KEY":"synthetic-secret"}}}}`), 0600)
	result, err := InstallPlugin(context.Background(), fixturePayload(), opts)
	if err != nil || result.Installed || result.Marketplace != "my-market" {
		t.Fatal(result, err)
	}
	data, _ := os.ReadFile(filepath.Join(target, ".mcp.json"))
	var mcp shared.Object
	json.Unmarshal(data, &mcp)
	server := shared.Obj(shared.Obj(mcp["mcpServers"])["api-subagents"])
	if shared.Int(server["tool_timeout_sec"]) != 660 {
		t.Fatal("upgrade retained the old short host timeout")
	}
	path := shared.Str(server["command"])
	if !shared.Inside(opts.DataRoot, path) || strings.Contains(string(data), "synthetic-secret") || !strings.Contains(string(data), "CUSTOM_MODEL_KEY") || strings.Contains(string(data), "BAD-NAME") {
		t.Fatal("runtime or environment isolation failed")
	}
	worker, _ := os.ReadFile(path)
	if string(worker) != "MZ synthetic worker" {
		t.Fatal("worker missing")
	}
	market, _ := os.ReadFile(marketPath)
	if !strings.Contains(string(market), `"other"`) {
		t.Fatal("other plugin lost")
	}
	if InstalledVersion(opts) != "" {
		t.Fatal("uninstalled plugin shown as installed")
	}
	manifest, _ := os.ReadFile(filepath.Join(target, ".codex-plugin", "plugin.json"))
	shared.AtomicWrite(filepath.Join(opts.CodexHome, "plugins", "cache", result.Marketplace, "api-subagents", result.Version, ".codex-plugin", "plugin.json"), manifest, 0600)
	if InstalledVersion(opts) != "0.3.0" {
		t.Fatal("installed version not detected")
	}
	if _, err := InstallPlugin(context.Background(), fixturePayload(), opts); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(worker) {
		t.Fatal("running worker overwritten")
	}
}

// TestInvalidPluginPreservesExisting 包不完整或市场来源冲突时，在修改已安装文件之前报错。
func TestInvalidPluginPreservesExisting(t *testing.T) {
	for _, kind := range []string{"missing", "market-conflict"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			opts := InstallOptions{ProfileRoot: root, DataRoot: t.TempDir(), CodexHome: t.TempDir(), SkipCodex: true}
			path := filepath.Join(root, "plugins", "api-subagents", ".codex-plugin", "plugin.json")
			shared.AtomicWrite(path, []byte("original"), 0600)
			payload := fixturePayload()
			if kind == "missing" {
				delete(payload, "skills/api-workers/SKILL.md")
			} else {
				shared.AtomicWrite(filepath.Join(root, ".agents", "plugins", "marketplace.json"), []byte(`{"name":"personal","plugins":[{"name":"api-subagents","source":{"source":"local","path":"./plugins/different"}}]}`), 0600)
			}
			if _, err := InstallPlugin(context.Background(), payload, opts); err == nil {
				t.Fatal("invalid install accepted")
			}
			if data, _ := os.ReadFile(path); string(data) != "original" {
				t.Fatal("existing plugin changed")
			}
		})
	}
}
