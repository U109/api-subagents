package relay

import (
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

	"github.com/U109/api-subagents/internal/shared"
)

// TestResponsesRecoveryRealBackoff 验证首次上游 503 后的普通生成请求可恢复，真实退避不改写响应且只保留一个完成诊断。
func TestResponsesRecoveryRealBackoff(t *testing.T) {
	var calls atomic.Int32
	want := "data: {\"type\":\"response.completed\"}\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("X-Request-Id", "req-rejected")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":{"message":"synthetic temporary unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "req-recovered")
		io.WriteString(w, want)
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	req, err := http.NewRequest(http.MethodPost, r.Snapshot().Address+"/responses", strings.NewReader(`{"model":"api-subagents","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Subagents-Token", r.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK || string(body) != want || calls.Load() != 2 {
		t.Fatalf("temporary rejection was not recovered: status=%d calls=%d body=%q err=%v", res.StatusCode, calls.Load(), body, err)
	}
	result := awaitRequestResult(t, r)
	if result.Outcome != "completed" || result.HTTPStatus != http.StatusOK || result.UpstreamRequestID != "req-recovered" || result.BytesReceived != int64(len(want)) || r.Snapshot().Requests != 1 || len(r.Snapshot().RecentRequests) != 1 {
		t.Fatalf("recovery changed request diagnostics: %+v", result)
	}
}

// TestResponsesRecoveryCancellationAtGateway 验证真实 HTTP 客户端取消或关闭网关会中止退避，不先发送伪心跳或发出隐藏重试。
func TestResponsesRecoveryCancellationAtGateway(t *testing.T) {
	for _, disable := range []bool{false, true} {
		t.Run(map[bool]string{false: "clientCancel", true: "gatewayClose"}[disable], func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
				io.WriteString(w, "synthetic rejection")
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, true)
			waiting, stopped := make(chan struct{}), make(chan struct{})
			r.responsesRetryWait = func(ctx context.Context, delay time.Duration) error {
				close(waiting)
				err := waitResponsesRetry(ctx, delay)
				close(stopped)
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Snapshot().Address+"/responses", strings.NewReader(`{"model":"api-subagents","stream":true}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Api-Subagents-Token", r.token)
			done := make(chan error, 1)
			go func() {
				res, err := http.DefaultClient.Do(req)
				if res != nil {
					res.Body.Close()
				}
				done <- err
			}()
			select {
			case <-waiting:
			case <-time.After(3 * time.Second):
				t.Fatal("recovery wait not entered")
			}
			select {
			case err := <-done:
				t.Fatalf("response headers sent during recovery: %v", err)
			default:
			}
			if disable {
				if err := r.Disable(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("cancel did not stop recovery")
			}
			select {
			case err := <-done:
				if err == nil || calls.Load() != 1 {
					t.Fatal("cancelled request returned success or replayed", err, calls.Load())
				}
			case <-time.After(time.Second):
				t.Fatal("HTTP request remained open after cancellation")
			}
			if disable {
				if state := r.Snapshot(); state.Enabled || len(state.RecentRequests) != 0 {
					t.Fatalf("closed gateway retained stale diagnostics: %+v", state)
				}
			} else if result := awaitRequestResult(t, r); result.Outcome != "client_cancelled" {
				t.Fatalf("client cancellation diagnosis lost during recovery: %+v", result)
			}
		})
	}
}

// TestResponsesGatewayCloseAbortsActiveStream 验证正文已交付后关闭网关会中止 HTTP 流，不将取消包装为正常 EOF，也不自动重放。
func TestResponsesGatewayCloseAbortsActiveStream(t *testing.T) {
	const payload = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic partial\"}\n\n"
	var calls atomic.Int32
	stopped := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, payload)
		w.(http.Flusher).Flush()
		<-req.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Snapshot().Address+"/responses", strings.NewReader(`{"model":"api-subagents","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Subagents-Token", r.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	first := make([]byte, len(payload))
	if _, err := io.ReadFull(res.Body, first); err != nil || string(first) != payload {
		t.Fatal("committed stream changed", string(first), err)
	}
	if err := r.Disable(); err != nil {
		t.Fatal(err)
	}
	if rest, err := io.ReadAll(res.Body); err == nil || len(rest) != 0 || calls.Load() != 1 {
		t.Fatal("gateway cancellation finished as success or replayed", string(rest), err, calls.Load())
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("gateway cancellation did not stop upstream")
	}
}

// TestCodexResponsesTransientRecovery 验证真实 Codex CLI 在隔离 CODEX_HOME 中经历 503/502 后获得完整回答，所有模型请求只访问本机模拟服务。
func TestCodexResponsesTransientRecovery(t *testing.T) {
	bin := codexBinary(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body shared.Object
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body["model"] != "mock-model" || req.Header.Get("Authorization") != "Bearer synthetic-relay-key" {
			t.Error("recovered Codex request changed model or credentials", err)
		}
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "synthetic unavailable")
		case 2:
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "synthetic bad gateway")
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, upstreamReply("responses", true))
		}
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	r.responsesRetryWait = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	answer := filepath.Join(root, "answer.txt")
	cmd := codexTestCommand(ctx, bin, "exec", "--ephemeral", "--skip-git-repo-check", "--json", "-s", "read-only", "-m", "api-subagents/demo", "-C", root, "-o", answer, "Reply OK without using tools.")
	cmd.Env = codexTestEnv(r.Codex.Home)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Codex recovery failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(answer)
	if err != nil || strings.TrimSpace(string(data)) != "OK" || calls.Load() != 3 || r.Snapshot().Requests != 1 {
		t.Fatalf("Codex did not complete one recovered turn: answer=%q calls=%d requests=%d err=%v\n%s", data, calls.Load(), r.Snapshot().Requests, err, output)
	}
	if result := awaitRequestResult(t, r); result.Outcome != "completed" {
		t.Fatalf("recovered Codex response not diagnosed as complete: %+v", result)
	}
}
