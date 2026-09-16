package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/U109/api-subagents/frontend"
	buildinfo "github.com/U109/api-subagents/internal/buildinfo"
	configstore "github.com/U109/api-subagents/internal/config"
	platform "github.com/U109/api-subagents/internal/platform"
	pluginruntime "github.com/U109/api-subagents/internal/plugin"
	settings "github.com/U109/api-subagents/internal/settings"
	shared "github.com/U109/api-subagents/internal/shared"
	tasks "github.com/U109/api-subagents/internal/tasks"
	workfiles "github.com/U109/api-subagents/internal/workspace"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sourceFS struct {
	fs.FS
	worker string
}

// Open 安装后的插件复用当前独立程序，源码构建优先使用明确的 bin 文件。
func (s sourceFS) Open(name string) (fs.File, error) {
	file, err := s.FS.Open(name)
	if name == "bin/api-subagents-worker.exe" && os.IsNotExist(err) {
		return os.Open(s.worker)
	}
	return file, err
}

// argument 读取固定 CLI 选项，不将参数拼接为 shell 命令。
func argument(name string) string {
	for i, arg := range os.Args {
		if arg == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}

// hasArgument 判断无值开关，保持配置、安装与 stdio 模式相互独立。
func hasArgument(name string) bool {
	for _, arg := range os.Args {
		if arg == name {
			return true
		}
	}
	return false
}

// openBrowser 通过平台浏览器入口打开受令牌保护的回环页面，不打印配置内容。
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	platform.HideProcess(cmd)
	return cmd.Start()
}

// run 按参数启动配置页面、安装插件、应用审查后的建议或独立 stdio MCP 服务。
func run(ctx context.Context) error {
	if hasArgument("--version") {
		fmt.Println(buildinfo.Version)
		return nil
	}
	if hasArgument("--apply") {
		id := argument("--apply")
		workspace := argument("--workspace")
		paths := []string{}
		for i, arg := range os.Args {
			if arg == "--workspace" && i+1 < len(os.Args) {
				paths = append(paths, os.Args[i+2:]...)
				break
			}
		}
		value, err := workfiles.ApplyProposals(id, workspace, paths, filepath.Join(configstore.DataDir(), "tasks"))
		if err != nil {
			return err
		}
		fmt.Println(string(shared.Marshal(value)))
		return nil
	}
	if hasArgument("--install") {
		exe, _ := os.Executable()
		root := argument("--source")
		if root == "" {
			root = filepath.Dir(filepath.Dir(exe))
		}
		opts := pluginruntime.DefaultInstallOptions()
		if profile := argument("--profile"); profile != "" {
			opts.ProfileRoot = profile
			opts.CodexHome = filepath.Join(profile, ".codex")
			opts.DataRoot = filepath.Join(profile, ".local-data")
		}
		opts.SkipCodex = hasArgument("--skip-codex")
		result, err := pluginruntime.InstallPlugin(ctx, sourceFS{os.DirFS(root), exe}, opts)
		if err != nil {
			return err
		}
		fmt.Println(string(shared.Marshal(result)))
		return nil
	}
	if hasArgument("--configure") {
		setup, err := settings.StartSetup(settings.NewConfigService(configstore.NewConfigStore("")), frontend.Files)
		if err != nil {
			return err
		}
		defer setup.Server.Close()
		fmt.Fprintln(os.Stderr, "API Subagents 配置页面："+setup.URL)
		if !hasArgument("--no-open") {
			_ = openBrowser(setup.URL)
		}
		timer := time.NewTimer(30 * time.Minute)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		return nil
	}
	manager := tasks.NewManager(nil, "", nil)
	defer manager.Close()
	return tasks.NewMCPServer(manager).Run(ctx, &mcp.StdioTransport{})
}

// main 确保 stdio 模式只向 stdout 输出 MCP 消息，其他模式的错误使用不含凭据的 JSON。
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		value := shared.Object{"error": shared.SafeFileError(err)}
		var partial *workfiles.ApplyError
		if errors.As(err, &partial) {
			value["applied"] = partial.Applied
		}
		fmt.Fprintln(os.Stderr, string(shared.Marshal(value)))
		os.Exit(1)
	}
}
