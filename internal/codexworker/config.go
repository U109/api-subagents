package codexworker

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/plugin"
	"github.com/U109/api-subagents/internal/shared"
	toml "github.com/pelletier/go-toml/v2"
)

const instructions = `Complete only this assigned task in the specified workspace. Read applicable AGENTS.md. You are an execution worker: you may edit files and run commands only within the configured sandbox and user-authorized scope. Preserve existing uncommitted changes. Never spawn agents, change provider, read credentials, commit, push, deploy or bypass permission restrictions. If a command requires unavailable permission, report the blocker instead of escalating. Run focused tests when appropriate. Your final answer must identify changed files, actual verification with outcomes, and remaining risks. Never claim a command or test ran without tool evidence. Use the task's language.`

// findBinary 只接受本机原生 Codex；环境变量可指定安装路径，不通过 cmd/PowerShell shim 执行。
func findBinary(explicit string) (string, error) {
	path := explicit
	if path == "" {
		path = os.Getenv("API_SUBAGENTS_CODEX_BIN")
	}
	if path == "" {
		if runtime.GOOS == "windows" {
			path = plugin.FindCodex()
		} else {
			path, _ = exec.LookPath("codex")
		}
	}
	if path == "" || !filepath.IsAbs(path) || (runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(path), ".exe")) {
		return "", errors.New("未找到原生 Codex CLI；请安装 Codex，或设置 API_SUBAGENTS_CODEX_BIN 为绝对可执行文件路径。")
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", errors.New("指定的 Codex 可执行文件不存在。")
	}
	return path, nil
}

// workerEnvironment 使用运行工具所需的白名单，不继承父任务的 Key、Codex 配置或模型凭据。
// 用户目录和临时目录均重定向到本次运行，避免自动加载个人插件、认证文件和全局 Git 配置。
func workerEnvironment(home string) []string {
	allowed := map[string]bool{}
	for _, key := range strings.Fields("PATH PATHEXT SYSTEMROOT WINDIR COMSPEC PROGRAMFILES PROGRAMFILES(X86) PROGRAMDATA NUMBER_OF_PROCESSORS PROCESSOR_ARCHITECTURE LANG LC_ALL GOROOT GOPATH GOCACHE GOMODCACHE CGO_ENABLED SSL_CERT_FILE SSL_CERT_DIR") {
		allowed[key] = true
	}
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if allowed[strings.ToUpper(key)] {
			env = append(env, value)
		}
	}
	for key, value := range map[string]string{"CODEX_HOME": home, "HOME": home, "USERPROFILE": home, "APPDATA": filepath.Join(home, "appdata"), "LOCALAPPDATA": filepath.Join(home, "local"), "TEMP": filepath.Join(home, "tool-tmp"), "TMP": filepath.Join(home, "tool-tmp"), "TMPDIR": filepath.Join(home, "tool-tmp")} {
		env = append(env, key+"="+value)
	}
	return env
}

// sandboxPolicy 固定可写工作区和私有工具临时目录，不接受全盘写入或提权；网络默认关闭。
func sandboxPolicy(req Request, home string) shared.Object {
	if req.Access == "read-only" {
		return shared.Object{"type": "readOnly", "networkAccess": false}
	}
	return shared.Object{"type": "workspaceWrite", "writableRoots": []string{req.Workspace, filepath.Join(home, "tool-tmp")}, "networkAccess": req.NetworkAccess, "excludeTmpdirEnvVar": true, "excludeSlashTmp": true}
}

// prepareConfig 生成只含本地令牌的临时配置和通用模型目录，真实上游 Key 只由 Go 网关持有。
func prepareConfig(req Request, home, address, token string) (shared.Object, error) {
	p := req.Profile
	p.APIKey, p.APIKeyEnv, p.RelayModels = "", "", nil
	c := configstore.Config{Version: 1, Models: map[string]configstore.Profile{"worker": p}}
	if err := (codexconfig.CodexConfig{Home: home, DataRoot: home}).WriteCatalog(c, "worker"); err != nil {
		return nil, err
	}
	for _, path := range []string{"appdata", "local", "tool-tmp"} {
		if err := os.MkdirAll(filepath.Join(home, path), 0700); err != nil {
			return nil, err
		}
	}
	config := shared.Object{
		"model": codexconfig.RelayModelAlias("worker", p.Model), "model_provider": "api_subagents_worker",
		"model_catalog_json":   filepath.Join(home, "codex-model-catalog.json"),
		"model_context_window": p.ContextWindow(p.Model), "model_auto_compact_token_limit": p.ContextWindow(p.Model) * 9 / 10,
		"approval_policy": "never", "sandbox_mode": req.Access, "web_search": "disabled", "check_for_update_on_startup": false,
		"history": shared.Object{"persistence": "none"}, "agents": shared.Object{"enabled": false},
		"features":    shared.Object{"plugins": false, "apps": false, "multi_agent": false, "hooks": false, "memories": false},
		"mcp_servers": shared.Object{}, "shell_environment_policy": shared.Object{"inherit": "all"},
		"sandbox_workspace_write": shared.Object{"writable_roots": []string{req.Workspace, filepath.Join(home, "tool-tmp")}, "network_access": req.NetworkAccess, "exclude_tmpdir_env_var": true, "exclude_slash_tmp": true},
		"model_providers":         shared.Object{"api_subagents_worker": shared.Object{"name": "API Subagents execution worker", "base_url": address, "wire_api": "responses", "requires_openai_auth": false, "supports_websockets": false, "request_max_retries": 0, "stream_max_retries": 0, "stream_idle_timeout_ms": 650000, "http_headers": shared.Object{"X-Api-Subagents-Token": token}}},
	}
	if p.ReasoningEffort != "" {
		config["model_reasoning_effort"] = p.ReasoningEffort
	}
	if runtime.GOOS == "windows" {
		config["windows"] = shared.Object{"sandbox": "unelevated"}
	}
	data, err := toml.Marshal(config)
	if err != nil {
		return nil, err
	}
	return config, shared.AtomicWrite(filepath.Join(home, "config.toml"), data, 0600)
}

// verifyThread 在首次模型请求之前校验实际 provider、model、cwd 和沙箱，拒绝静默回退或放宽权限。
func verifyThread(value shared.Object, req Request, home string) error {
	policy := shared.Obj(value["sandbox"])
	want := sandboxPolicy(req, home)
	valid := shared.Str(shared.Obj(value["thread"])["id"]) != "" && value["modelProvider"] == "api_subagents_worker" && value["model"] == codexconfig.RelayModelAlias("worker", req.Profile.Model) && shared.SamePath(shared.Str(value["cwd"]), req.Workspace) && value["approvalPolicy"] == "never" && policy["type"] == want["type"] && (policy["networkAccess"] == true) == req.NetworkAccess
	if req.Access == "workspace-write" {
		valid = valid && policy["excludeTmpdirEnvVar"] == true && policy["excludeSlashTmp"] == true
		for _, raw := range shared.Arr(policy["writableRoots"]) {
			path, err := filepath.EvalSymlinks(shared.Str(raw))
			valid = valid && err == nil && (shared.Inside(req.Workspace, path) || shared.SamePath(path, filepath.Join(home, "tool-tmp")))
		}
	}
	if !valid {
		return &shared.OpError{Code: "WORKER_CONFIG_MISMATCH", Message: "Codex worker 的实际模型、供应商、目录或权限不符合请求，已在派发任务前停止。"}
	}
	return nil
}

// removeHome 只删除本次创建且仍位于固定父目录下的临时 home，拒绝被替换的目录联接。
func removeHome(home, parent string) error {
	actual, err := filepath.EvalSymlinks(home)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(filepath.Base(actual), "worker-") || !shared.SamePath(filepath.Dir(actual), parent) {
		return errors.New("临时目录边界发生变化，未自动清理。")
	}
	return os.RemoveAll(actual)
}
