package codexworker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
	toml "github.com/pelletier/go-toml/v2"
)

// TestWorkerProtocolHelper 在独立测试进程中模拟 App Server，不调用真实模型或用户的 Codex 配置。
func TestWorkerProtocolHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--worker-helper" {
		return
	}
	scenario := os.Args[len(os.Args)-1]
	defer os.Exit(0)
	home := os.Getenv("CODEX_HOME")
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil || strings.Contains(string(data), "synthetic-private-key") || os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("CUSTOM_MODEL_KEY") != "" {
		os.Exit(4)
	}
	var config shared.Object
	if toml.Unmarshal(data, &config) != nil {
		os.Exit(5)
	}
	if scenario == "child" || scenario == "no-read" {
		time.Sleep(time.Minute)
		return
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	var cwd string
	for {
		var request shared.Object
		if decoder.Decode(&request) != nil {
			return
		}
		params := shared.Obj(request["params"])
		reply := shared.Object{"id": request["id"], "result": shared.Object{}}
		switch request["method"] {
		case "initialize":
		case "initialized":
			continue
		case "thread/start":
			cwd = shared.Str(params["cwd"])
			policy := shared.Object{"type": "readOnly", "networkAccess": false}
			if params["sandbox"] == "workspace-write" {
				policy = shared.Object{"type": "workspaceWrite", "networkAccess": false, "excludeTmpdirEnvVar": true, "excludeSlashTmp": true, "writableRoots": []string{cwd, filepath.Join(home, "tool-tmp")}}
			}
			value := shared.Object{"thread": shared.Object{"id": "test-thread"}, "model": config["model"], "modelProvider": "api_subagents_worker", "cwd": cwd, "approvalPolicy": "never", "sandbox": policy}
			if scenario == "wrong-provider" {
				value["modelProvider"] = "openai"
			}
			if scenario == "wrong-sandbox" {
				policy["type"] = "dangerFullAccess"
			}
			reply["result"] = value
		case "turn/start":
			if scenario == "approval" {
				_ = encoder.Encode(shared.Object{"id": 900, "method": "item/commandExecution/requestApproval", "params": shared.Object{"threadId": "test-thread"}})
				continue
			}
			if scenario == "spawn-child" {
				exe, _ := os.Executable()
				child := exec.Command(exe, "-test.run=^TestWorkerProtocolHelper$", "--", "--worker-helper", "child")
				if child.Start() != nil {
					os.Exit(7)
				}
				_ = os.WriteFile(filepath.Join(cwd, "child.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0600)
			}
			_ = encoder.Encode(shared.Object{"method": "turn/started", "params": shared.Object{"threadId": "test-thread", "turn": shared.Object{"id": "test-turn"}}})
			reply["result"] = shared.Object{"turn": shared.Object{"id": "test-turn"}}
			_ = encoder.Encode(reply)
			if scenario == "cancel" || scenario == "spawn-child" {
				continue
			}
			provider := shared.Obj(shared.Obj(config["model_providers"])["api_subagents_worker"])
			body := shared.Object{"model": config["model"], "input": []any{shared.Object{"role": "user", "content": shared.Obj(shared.Arr(params["input"])[0])["text"]}}, "stream": false}
			upstream, _ := http.NewRequest("POST", shared.Str(provider["base_url"])+"/responses", strings.NewReader(string(shared.Marshal(body))))
			upstream.Header.Set("Content-Type", "application/json")
			upstream.Header.Set("X-Api-Subagents-Token", shared.Str(shared.Obj(provider["http_headers"])["X-Api-Subagents-Token"]))
			res, err := http.DefaultClient.Do(upstream)
			if err != nil {
				os.Exit(6)
			}
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			// 用协议事件验证证据采集，不把它当成真实 Codex 执行测试。
			for _, item := range []shared.Object{
				{"id": "cmd1", "type": "commandExecution", "command": "synthetic-test", "cwd": cwd, "status": "completed", "exitCode": 0},
				{"id": "patch1", "type": "fileChange", "status": "completed", "changes": []any{shared.Object{"path": filepath.Join(cwd, "example.txt")}}},
				{"id": "msg1", "type": "agentMessage", "phase": "final_answer", "text": "verified synthetic result"},
			} {
				_ = encoder.Encode(shared.Object{"method": "item/completed", "params": shared.Object{"threadId": "test-thread", "turnId": "test-turn", "item": item}})
			}
			status := "completed"
			if scenario == "failure" {
				status = "failed"
			}
			_ = encoder.Encode(shared.Object{"method": "turn/completed", "params": shared.Object{"threadId": "test-thread", "turn": shared.Object{"id": "test-turn", "status": status, "error": shared.Object{"message": "synthetic failure"}}}})
			continue
		case "turn/interrupt":
			return
		default:
			continue
		}
		_ = encoder.Encode(reply)
	}
}

// helperRunner 固定原生测试二进制和协议模拟入口，生产路径不会接收这些测试参数。
func helperRunner(t *testing.T, scenario string) *Runner {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{Binary: exe, command: func(_ string, _ ...string) *exec.Cmd {
		return exec.Command(exe, "-test.run=^TestWorkerProtocolHelper$", "--", "--worker-helper", scenario)
	}}
}

// TestRunnerLifecycle 验证明文任务路由、权限校验、证据采集和清理；失败或交互不得被报告为成功。
func TestRunnerLifecycle(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "do-not-inherit")
	t.Setenv("CUSTOM_MODEL_KEY", "do-not-inherit-either")
	for _, scenario := range []string{"success", "failure", "wrong-provider", "wrong-sandbox", "approval", "cancel", "no-read"} {
		t.Run(scenario, func(t *testing.T) {
			requests := make(chan shared.Object, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var body shared.Object
				_ = json.NewDecoder(req.Body).Decode(&body)
				requests <- body
				if req.Header.Get("Authorization") != "Bearer synthetic-private-key" {
					t.Error("wrong upstream credential")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"r","status":"completed","output":[]}`)
			}))
			defer server.Close()
			_, profile := testutil.Config(t, "responses", server.URL)
			parent := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if scenario == "no-read" {
				timer := time.AfterFunc(400*time.Millisecond, cancel)
				defer timer.Stop()
			}
			request := Request{Profile: profile, Task: "unique plaintext assignment", Workspace: t.TempDir(), Access: "workspace-write", HomeRoot: parent, MaxRequests: 3, Progress: func(progress shared.Object) {
				if scenario == "cancel" && progress["phase"] == "executing_codex" {
					cancel()
				}
			}}
			result, err := helperRunner(t, scenario).Run(ctx, request)
			if scenario == "success" {
				if err != nil || result.Text != "verified synthetic result" || result.ToolCalls != 2 || result.Requests != 1 || len(result.Commands) != 1 || len(result.ChangedFiles) != 1 {
					t.Fatal(result, err)
				}
				body := <-requests
				if body["model"] != profile.Model || !strings.Contains(string(shared.Marshal(body)), request.Task) {
					t.Fatal("task or model lost", body)
				}
			} else if err == nil {
				t.Fatal("invalid completion", result)
			}
			if scenario == "wrong-provider" || scenario == "wrong-sandbox" {
				if len(requests) != 0 || result.WorkspaceMayHaveChanges {
					t.Fatal("invalid config reached execution")
				}
			}
			if scenario == "approval" {
				var op *shared.OpError
				if !errors.As(err, &op) || op.Code != "WORKER_INTERACTION_REQUIRED" {
					t.Fatal(err)
				}
			}
			files, err := os.ReadDir(parent)
			if err != nil || len(files) != 0 || result.CleanupWarning != "" {
				t.Fatal("home not cleaned", files, result.CleanupWarning, err)
			}
		})
	}
}

// TestWorkerEnvironmentAndBoundaries 防止继承凭据、宽权限和在工作区内保存运行身份。
func TestWorkerEnvironmentAndBoundaries(t *testing.T) {
	t.Setenv("API_SUBAGENTS_HOME", "secret-parent-config")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "synthetic-secret")
	t.Setenv("CODEX_HOME", "parent-home")
	home := t.TempDir()
	env := strings.Join(workerEnvironment(home), "\n")
	for _, value := range []string{"synthetic-secret", "parent-home", "secret-parent-config"} {
		if strings.Contains(env, value) {
			t.Fatal("environment leak")
		}
	}
	_, profile := testutil.Config(t, "responses", "http://127.0.0.1:1")
	for _, request := range []Request{
		{Profile: profile, Access: "danger-full-access"},
		{Profile: profile, Access: "read-only", NetworkAccess: true},
		{Profile: profile, Access: "read-only", Workspace: home, HomeRoot: filepath.Join(home, "state")},
	} {
		if _, err := helperRunner(t, "success").Run(context.Background(), request); err == nil {
			t.Fatal("invalid boundary accepted")
		}
	}
}
