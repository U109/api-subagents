package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	"github.com/U109/api-subagents/internal/shared"
)

// TestCodexHistoricalProviderAfterDefaultSwitch 重现自定义、内置 OpenAI 和已删除身份的旧对话续聊。
// 使用独立 CODEX_HOME 和本地模拟 Key，验证接管后走当前连接，关闭后恢复各自原配置。
func TestCodexHistoricalProviderAfterDefaultSwitch(t *testing.T) {
	bin := codexBinary(t)
	for _, id := range []string{"custom", "openai", "removed"} {
		t.Run(id, func(t *testing.T) { testHistoricalProviderResume(t, bin, id) })
	}
}

// testHistoricalProviderResume 保存绑定指定身份的模拟对话，切换默认后验证真实 app-server 和 CLI 的接管及恢复。
func testHistoricalProviderResume(t *testing.T, bin, provider string) {
	var originalRequests, relayRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
			replyError(w, http.StatusUpgradeRequired, "The isolated mock uses HTTP/SSE.")
			return
		}
		var body shared.Object
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body["model"] != "mock-model" {
			t.Error("unexpected model request", err)
		}
		switch req.Header.Get("Authorization") {
		case "Bearer synthetic-original-key":
			originalRequests.Add(1)
		case "Bearer synthetic-relay-key":
			relayRequests.Add(1)
		default:
			t.Error("unexpected credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if req.Header.Get("X-Api-Subagents-Token") != "" {
			t.Error("local relay token reached upstream")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamReply("responses", true))
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	if err := r.Disable(); err != nil {
		t.Fatal(err)
	}
	original := "model='current-model'\nmodel_provider='current'\ncli_auth_credentials_store='file'\nopenai_base_url='" + server.URL + "/v1'\n"
	ids := []string{"current"}
	if provider != "openai" {
		ids = append(ids, provider)
	}
	for _, id := range ids {
		original += "[model_providers." + id + "]\nname='Original mock'\nbase_url='" + server.URL + "/v1'\nwire_api='responses'\nsupports_websockets=false\nrequires_openai_auth=false\nhttp_headers={Authorization='Bearer synthetic-original-key'}\n"
	}
	if err := os.WriteFile(filepath.Join(r.Codex.Home, "config.toml"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
	defer cancel()
	if provider == "openai" {
		login := codexTestCommand(ctx, bin, "login", "--with-api-key")
		login.Env = codexTestEnv(r.Codex.Home)
		login.Dir, login.Stdin = root, strings.NewReader("synthetic-original-key")
		if output, err := login.CombinedOutput(); err != nil {
			t.Fatalf("isolated API login failed: %v\n%s", err, output)
		}
	}
	providerSetting := "model_provider=" + string(shared.Marshal(provider))
	cmd := codexTestCommand(ctx, bin, "-c", providerSetting, "exec", "--skip-git-repo-check", "--json", "-s", "read-only", "-m", "mock-model", "-C", root, "Reply OK without tools.")
	cmd.Env, cmd.Dir = codexTestEnv(r.Codex.Home), root
	output, err := cmd.CombinedOutput()
	if err != nil || originalRequests.Load() != 1 {
		t.Fatalf("historical thread setup failed: %v\n%s", err, output)
	}
	threadID := ""
	for _, line := range bytes.Split(output, []byte("\n")) {
		var event shared.Object
		if json.Unmarshal(line, &event) == nil && event["type"] == "thread.started" {
			threadID = shared.Str(event["thread_id"])
		}
	}
	if threadID == "" {
		t.Fatal("historical conversation was not saved")
	}
	if provider == "removed" {
		// 模拟历史对话创建后删除了配置中的提供商；只能依靠只读会话元数据找回旧身份。
		original = original[:bytes.Index([]byte(original), []byte("[model_providers.removed]"))]
		if err := os.WriteFile(filepath.Join(r.Codex.Home, "config.toml"), []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Enable("demo"); err != nil {
		t.Fatal(err)
	}
	alias := codexconfig.RelayModelAlias("demo", "mock-model")
	t.Run("resume-provider-identity", func(t *testing.T) {
		client := startCodexRPC(t, bin, r.Codex.Home, root)
		resumed := client.call(t, "thread/resume", shared.Object{"threadId": threadID, "modelProvider": provider, "model": alias})
		if resumed["modelProvider"] != provider {
			t.Fatal("historical provider identity was not retained", resumed["modelProvider"])
		}
	})
	cmd = codexTestCommand(ctx, bin, "-c", providerSetting, "exec", "resume", "--skip-git-repo-check", "--json", "-m", "unconfigured-legacy-model", threadID, "Reply OK without tools.")
	cmd.Env, cmd.Dir = codexTestEnv(r.Codex.Home), root
	if output, err := cmd.CombinedOutput(); err != nil || originalRequests.Load() != 1 || relayRequests.Load() != 1 {
		t.Fatalf("historical provider bypassed relay: %v, original=%d, relay=%d\n%s", err, originalRequests.Load(), relayRequests.Load(), output)
	}
	if err := r.Disable(); err != nil {
		t.Fatal(err)
	}
	if provider == "removed" {
		restored, err := os.ReadFile(filepath.Join(r.Codex.Home, "config.toml"))
		if err != nil || bytes.Contains(restored, []byte("model_providers.removed")) {
			t.Fatal("temporary historical provider was not removed", err)
		}
		return
	}
	cmd = codexTestCommand(ctx, bin, "-c", providerSetting, "exec", "resume", "--skip-git-repo-check", "--json", "-m", "mock-model", threadID, "Reply OK without tools.")
	cmd.Env, cmd.Dir = codexTestEnv(r.Codex.Home), root
	if output, err := cmd.CombinedOutput(); err != nil || originalRequests.Load() != 2 || relayRequests.Load() != 1 {
		t.Fatalf("historical provider was not restored: %v\n%s", err, output)
	}
}
