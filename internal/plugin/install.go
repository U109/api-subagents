package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/platform"
	"github.com/U109/api-subagents/internal/shared"
)

var PluginFiles = []string{".codex-plugin/plugin.json", ".mcp.json", "skills/api-workers/SKILL.md", "skills/api-workers/agents/openai.yaml", "Configure.cmd", "Install.cmd", "scripts/configure.ps1", "scripts/source-install.ps1", "THIRD-PARTY-NOTICES.txt"}

type InstallOptions struct {
	ProfileRoot, DataRoot, CodexHome string
	SkipCodex                        bool
	Execute                          func(context.Context, string, ...string) error
}

type InstallResult struct {
	Installed   bool   `json:"installed"`
	PluginRoot  string `json:"pluginRoot"`
	Version     string `json:"version"`
	Marketplace string `json:"marketplace"`
}

// DefaultInstallOptions 使用用户目录保存插件、独立程序和缓存，并支持测试隔离。
func DefaultInstallOptions() InstallOptions {
	home, _ := os.UserHomeDir()
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	return InstallOptions{ProfileRoot: home, DataRoot: configstore.DataDir(), CodexHome: codexHome, Execute: executeCLI}
}

// executeCLI 只执行已确定的 Codex 命令和参数，避免把页面输入拼入 shell。
func executeCLI(ctx context.Context, path string, args ...string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	platform.HideProcess(cmd)
	if err := cmd.Run(); err != nil {
		return errors.New("Codex 插件安装未完成，请启动 Codex 后重试。")
	}
	return nil
}

// FindCodex 优先寻找原生 CLI，其次检查桌面 App 管理的版本目录。
func FindCodex() string {
	if path, err := exec.LookPath("codex.exe"); err == nil {
		return path
	}
	root := filepath.Join(os.Getenv("LOCALAPPDATA"), "OpenAI", "Codex", "bin")
	type candidate struct {
		path string
		time time.Time
	}
	items := []candidate{}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.IsDir() && strings.EqualFold(entry.Name(), "codex.exe") {
			if info, err := entry.Info(); err == nil {
				items = append(items, candidate{path, info.ModTime()})
			}
		}
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return items[i].time.After(items[j].time) })
	if len(items) > 0 {
		return items[0].path
	}
	return ""
}

// PrepareWorker 将插件可执行文件保存到内容寻址缓存；桌面 App 更新或关闭不会覆盖正在运行的版本。
func PrepareWorker(data []byte, dataRoot string) (string, error) {
	if len(data) < 2 || string(data[:2]) != "MZ" {
		return "", errors.New("插件运行文件无效，请重新安装 App。")
	}
	path := filepath.Join(dataRoot, "runtime", "worker-"+shared.Hash(data)+".exe")
	if current, err := os.ReadFile(path); err == nil && shared.Hash(current) == shared.Hash(data) {
		return path, nil
	}
	if err := shared.AtomicWrite(path, data, 0700); err != nil {
		return "", errors.New("无法准备独立插件运行程序。")
	}
	return path, nil
}

// InstallPlugin 只复制发布白名单；保留个人市场、环境变量透传和用户数据，成功调用 CLI 才报告安装完成。
func InstallPlugin(ctx context.Context, source fs.FS, opts InstallOptions) (InstallResult, error) {
	root, err := filepath.Abs(opts.ProfileRoot)
	if err != nil {
		return InstallResult{}, err
	}
	target := filepath.Join(root, "plugins", "api-subagents")
	if !shared.Inside(root, target) {
		return InstallResult{}, errors.New("插件安装目录无效。")
	}
	marketPath := filepath.Join(root, ".agents", "plugins", "marketplace.json")
	market := shared.Object{"name": "personal", "interface": shared.Object{"displayName": "Personal"}, "plugins": []any{}}
	previous, err := os.ReadFile(marketPath)
	if err == nil {
		if json.Unmarshal(previous, &market) != nil {
			return InstallResult{}, errors.New("现有个人市场配置无效，未进行修改。")
		}
	} else if !os.IsNotExist(err) {
		return InstallResult{}, errors.New("无法读取个人插件市场。")
	}
	name := shared.Str(market["name"])
	plugins, ok := market["plugins"].([]any)
	if !shared.MarketPattern.MatchString(name) || !ok {
		return InstallResult{}, errors.New("现有个人市场名称或结构无效。")
	}
	found := 0
	for _, v := range plugins {
		entry := shared.Obj(v)
		if entry["name"] == "api-subagents" {
			found++
			source := shared.Obj(entry["source"])
			if source["source"] != "local" || source["path"] != "./plugins/api-subagents" {
				return InstallResult{}, errors.New("已有同名插件使用其他来源，未覆盖。")
			}
		}
	}
	if found > 1 {
		return InstallResult{}, errors.New("个人市场存在重复插件条目。")
	}
	// 在写入前读取全部白名单文件，避免源文件缺失时破坏现有插件。
	files := map[string][]byte{}
	for _, path := range PluginFiles {
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return InstallResult{}, errors.New("插件包缺少必要文件，请重新安装 App。")
		}
		files[path] = data
	}
	var manifest shared.Object
	if json.Unmarshal(files[".codex-plugin/plugin.json"], &manifest) != nil || manifest["name"] != "api-subagents" {
		return InstallResult{}, errors.New("插件清单无效。")
	}
	version := strings.Split(shared.Str(manifest["version"]), "+")[0] + "+codex." + time.Now().UTC().Format("20060102150405.000000000")
	manifest["version"] = version
	files[".codex-plugin/plugin.json"] = shared.Marshal(manifest)
	worker, err := fs.ReadFile(source, "bin/api-subagents-worker.exe")
	if err != nil {
		return InstallResult{}, errors.New("插件包缺少 Go 运行程序。")
	}
	runtimePath, err := PrepareWorker(worker, opts.DataRoot)
	if err != nil {
		return InstallResult{}, err
	}
	var mcpConfig shared.Object
	if json.Unmarshal(files[".mcp.json"], &mcpConfig) != nil {
		return InstallResult{}, errors.New("MCP 配置无效。")
	}
	server := shared.Obj(shared.Obj(mcpConfig["mcpServers"])["api-subagents"])
	if len(server) == 0 {
		return InstallResult{}, errors.New("MCP 配置缺少服务定义。")
	}
	envs := []string{"API_SUBAGENTS_HOME", "API_SUBAGENTS_CODEX_BIN"}
	existing, err := os.ReadFile(filepath.Join(target, ".mcp.json"))
	if err == nil {
		var prior shared.Object
		if json.Unmarshal(existing, &prior) != nil {
			return InstallResult{}, errors.New("现有插件 MCP 配置无效，未覆盖透传设置。")
		}
		for _, value := range shared.Arr(shared.Obj(shared.Obj(prior["mcpServers"])["api-subagents"])["env_vars"]) {
			variable := shared.Str(value)
			if shared.EnvPattern.MatchString(variable) && !shared.Contains(envs, variable) {
				envs = append(envs, variable)
			}
		}
	} else if !os.IsNotExist(err) {
		return InstallResult{}, errors.New("无法读取现有 MCP 配置。")
	}
	server["command"] = runtimePath
	server["args"] = []string{}
	server["env_vars"] = envs
	delete(server, "env")
	files[".mcp.json"] = shared.Marshal(mcpConfig)
	for path, data := range files {
		if err = shared.AtomicWrite(filepath.Join(target, filepath.FromSlash(path)), data, 0600); err != nil {
			return InstallResult{}, errors.New("无法写入插件文件，请检查权限。")
		}
	}
	if found == 0 {
		market["plugins"] = append(plugins, shared.Object{"name": "api-subagents", "source": shared.Object{"source": "local", "path": "./plugins/api-subagents"}, "policy": shared.Object{"installation": "AVAILABLE", "authentication": "ON_INSTALL"}, "category": "Productivity"})
		if len(previous) > 0 {
			if err = shared.AtomicWrite(marketPath+".backup-"+time.Now().UTC().Format("20060102150405"), previous, 0600); err != nil {
				return InstallResult{}, err
			}
		}
		if err = shared.AtomicWrite(marketPath, shared.Marshal(market), 0600); err != nil {
			return InstallResult{}, errors.New("无法注册个人插件市场。")
		}
	}
	result := InstallResult{PluginRoot: target, Version: version, Marketplace: name}
	if !opts.SkipCodex {
		cli := FindCodex()
		if cli != "" {
			execute := opts.Execute
			if execute == nil {
				execute = executeCLI
			}
			if err = execute(ctx, cli, "plugin", "add", "api-subagents@"+name); err != nil {
				return result, err
			}
			result.Installed = true
		}
	}
	return result, nil
}

// InstalledVersion 同时确认源插件与 Codex 缓存版本，区分文件准备好与实际安装完成。
func InstalledVersion(opts InstallOptions) string {
	manifestData, err := os.ReadFile(filepath.Join(opts.ProfileRoot, "plugins", "api-subagents", ".codex-plugin", "plugin.json"))
	if err != nil {
		return ""
	}
	marketData, err := os.ReadFile(filepath.Join(opts.ProfileRoot, ".agents", "plugins", "marketplace.json"))
	if err != nil {
		return ""
	}
	var manifest, market shared.Object
	if json.Unmarshal(manifestData, &manifest) != nil || json.Unmarshal(marketData, &market) != nil {
		return ""
	}
	name := shared.Str(market["name"])
	version := shared.Str(manifest["version"])
	if !shared.MarketPattern.MatchString(name) || !regexp.MustCompile(`^[A-Za-z0-9.+_-]+$`).MatchString(version) {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(opts.CodexHome, "plugins", "cache", name, "api-subagents", version, ".codex-plugin", "plugin.json"))
	if err != nil {
		return ""
	}
	var cached shared.Object
	if json.Unmarshal(data, &cached) != nil || cached["version"] != version {
		return ""
	}
	return strings.Split(version, "+")[0]
}
