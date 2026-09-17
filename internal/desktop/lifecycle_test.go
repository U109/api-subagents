//go:build windows

package desktop

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
	toml "github.com/pelletier/go-toml/v2"
)

// closeTestApp 使用独立 CODEX_HOME 和合成连接，验证真实配置恢复而不启动桌面或付费请求。
func closeTestApp(t *testing.T, endpoint string) *App {
	t.Helper()
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("API_SUBAGENTS_HOME", t.TempDir())
	a := newApp(false)
	c, err := configstore.ValidateConfig(shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": shared.Object{"protocol": "responses", "model": "mock-model", "baseUrl": endpoint, "apiKey": "synthetic-close-key", "stream": true}}}), true)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.service.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(a.relay.Codex.Home, "config.toml"), []byte("# original\nmodel='original-model'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// 冲突测试也应清理自己的网关；测试正文负责将模拟冲突修复后再结束。
		if a.relay.Snapshot().Enabled {
			if err := a.relay.Disable(); err != nil {
				t.Error(err)
			}
		}
	})
	return a
}

// assertCloseRestored 确认原默认模型恢复、令牌与备份清除，并保留旧对话所需的停用提供商。
func assertCloseRestored(t *testing.T, a *App) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(a.relay.Codex.Home, "config.toml"))
	if err != nil || !bytes.Contains(data, []byte("model='original-model'")) || bytes.Contains(data, []byte("X-Api-Subagents-Token")) || !bytes.Contains(data, []byte("127.0.0.1:0/v1")) {
		t.Fatal("Codex defaults or inactive conversation provider not restored")
	}
	if _, err := os.Stat(filepath.Join(a.relay.Codex.DataRoot, "codex-relay-backup.json")); !os.IsNotExist(err) {
		t.Fatal("relay backup still present after successful close")
	}
	if a.relay.Snapshot().Enabled || a.closeSnapshot().Phase != "ready" || a.beforeClose(context.Background()) {
		t.Fatal("restored window was not allowed to close")
	}
}

// TestCloseRelayAutomatically 覆盖正常退出和连续点击，只有一个请求执行恢复，已释放的端口不再监听。
func TestCloseRelayAutomatically(t *testing.T) {
	a := closeTestApp(t, "http://127.0.0.1:9")
	if err := a.relay.Enable("demo"); err != nil {
		t.Fatal(err)
	}
	address, _ := url.Parse(a.relay.Snapshot().Address)
	path := filepath.Join(a.relay.Codex.Home, "config.toml")
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(data, []byte("\n[features]\nmanual_change=true\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	var ready atomic.Int32
	var calls sync.WaitGroup
	for range 8 {
		calls.Go(func() {
			if a.prepareClose(false) {
				ready.Add(1)
			}
		})
	}
	calls.Wait()
	if ready.Load() != 1 {
		t.Fatal("close preparation ran more than once")
	}
	assertCloseRestored(t, a)
	data, _ = os.ReadFile(path)
	if !bytes.Contains(data, []byte("manual_change=true")) {
		t.Fatal("unrelated Codex edits were lost")
	}
	if connection, err := net.DialTimeout("tcp", address.Host, time.Second); err == nil {
		connection.Close()
		t.Fatal("relay listener still open after close")
	}
	if _, err := a.API("/api/config", ""); err == nil {
		t.Fatal("configuration operation accepted while exiting")
	}
	if _, err := a.EnableRelay("demo"); err == nil {
		t.Fatal("relay restarted while exiting")
	}
}

// TestCloseDraftDecision 继续编辑保留草稿与网关，明确放弃后自动恢复，不把草稿保存到磁盘。
func TestCloseDraftDecision(t *testing.T) {
	a := closeTestApp(t, "http://127.0.0.1:9")
	if err := a.relay.Enable("demo"); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(a.service.Store.Path)
	a.SetDirty(true)
	if a.prepareClose(false) || a.closeSnapshot().Phase != "confirm" || !a.relay.Snapshot().Enabled {
		t.Fatal("dirty window closed without a decision")
	}
	a.CancelClose()
	if a.closeSnapshot().Phase != "" || !a.dirty || !a.relay.Snapshot().Enabled {
		t.Fatal("continuing to edit changed draft or relay")
	}
	if a.prepareClose(true) {
		t.Fatal("canceled confirmation was still accepted")
	}
	a.prepareClose(false)
	if !a.prepareClose(true) {
		t.Fatal("discarding draft did not allow restoration")
	}
	assertCloseRestored(t, a)
	after, _ := os.ReadFile(a.service.Store.Path)
	if !bytes.Equal(saved, after) {
		t.Fatal("closing saved or changed model configuration")
	}
}

// TestCloseDuringOperation 安装与配置操作完成前不销毁窗口，完成后再次关闭能够退出。
func TestCloseDuringOperation(t *testing.T) {
	a := closeTestApp(t, "http://127.0.0.1:9")
	a.plugin["phase"] = "installing"
	if a.prepareClose(false) || a.closeSnapshot().Phase != "blocked" {
		t.Fatal("plugin installation was interrupted")
	}
	a.plugin["phase"] = "installed"
	if err := a.beginRequest(); err != nil {
		t.Fatal(err)
	}
	if a.prepareClose(false) || a.closeSnapshot().Phase != "blocked" {
		t.Fatal("configuration request was interrupted")
	}
	a.busy.Add(-1)
	if !a.prepareClose(false) {
		t.Fatal("completed operation kept blocking close")
	}
}

// TestCloseRestoreConflict 配置冲突时保留窗口、网关和备份，修复冲突后可再次关闭。
func TestCloseRestoreConflict(t *testing.T) {
	a := closeTestApp(t, "http://127.0.0.1:9")
	if err := a.relay.Enable("demo"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.relay.Codex.Home, "config.toml")
	written, _ := os.ReadFile(path)
	changed := bytes.Replace(written, []byte(`wire_api = "responses"`), []byte(`wire_api = "changed"`), 1)
	if bytes.Equal(written, changed) {
		t.Fatal("test did not alter the managed provider")
	}
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if a.relay.Snapshot().Enabled {
			_ = os.WriteFile(path, written, 0600)
		}
	})
	if a.prepareClose(false) || a.closeSnapshot().Phase != "error" || !a.relay.Snapshot().Enabled {
		t.Fatal("conflict did not preserve running relay")
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(current, changed) {
		t.Fatal("conflicting user edit was overwritten")
	}
	if _, err := os.Stat(filepath.Join(a.relay.Codex.DataRoot, "codex-relay-backup.json")); err != nil {
		t.Fatal("backup was lost on conflict")
	}
	if err := os.WriteFile(path, written, 0600); err != nil || !a.prepareClose(false) {
		t.Fatal("close could not retry after conflict resolved", err)
	}
	assertCloseRestored(t, a)
}

// TestCloseCancelsActiveStream 使用本地长连接确认退出取消上游请求，无需等待模型生成结束。
func TestCloseCancelsActiveStream(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ": waiting\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	t.Cleanup(upstream.Close)
	a := closeTestApp(t, upstream.URL)
	if err := a.relay.Enable("demo"); err != nil {
		t.Fatal(err)
	}
	written, _ := os.ReadFile(filepath.Join(a.relay.Codex.Home, "config.toml"))
	var parsed shared.Object
	if err := toml.Unmarshal(written, &parsed); err != nil {
		t.Fatal(err)
	}
	provider := shared.Obj(shared.Obj(parsed["model_providers"])["api_subagents"])
	token := shared.Str(shared.Obj(provider["http_headers"])["X-Api-Subagents-Token"])
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", a.relay.Snapshot().Address+"/responses", strings.NewReader(`{"model":"api-subagents","input":"local test","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Subagents-Token", token)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if response, err := http.DefaultClient.Do(req); err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("request did not reach local upstream")
	}
	if !a.prepareClose(false) {
		t.Fatal("active stream blocked close")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream was not canceled on close")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("gateway request remained open")
	}
	assertCloseRestored(t, a)
}
