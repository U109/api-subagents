package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// awaitRequestResult 等待网关发送 EOF 后发布诊断，避免 HTTP 客户端读完与服务端收尾的调度竞争。
func awaitRequestResult(t *testing.T, r *Relay) RequestResult {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state := r.Snapshot()
		if len(state.RecentRequests) > 0 && state.RecentRequests[0].Outcome != "receiving" {
			return state.RecentRequests[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("request result not published")
	return RequestResult{}
}

// TestResponsesDiagnostics 验证诊断不改变状态码、响应字节和请求次数，也不记录原始正文或密钥。
func TestResponsesDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status           int
		kind, body, want string
		stream           bool
	}{
		{"complete", 200, "text/event-stream", "data: {\"type\":\"response.completed\"}\n\n", "completed", true},
		{"failed", 200, "text/event-stream", "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"synthetic-relay-key private-prompt private-answer\"}}}\n\n", "upstream_failed", true},
		{"incomplete", 200, "text/event-stream", "data: {\"type\":\"response.incomplete\"}\n\n", "incomplete", true},
		{"truncated", 200, "text/event-stream", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"private-answer\"}\n\n", "unexpected_eof", true},
		{"doneOnly", 200, "text/event-stream", "data: [DONE]\n\n", "unexpected_eof", true},
		{"auth", 401, "application/json", "{\"error\":\"synthetic-relay-key\"}", "http_auth_error", true},
		{"badGateway", 502, "application/json", "{\"error\":\"private-answer\"}", "http_error", true},
		{"normalJSON", 200, "application/json", "{\"output\":\"private-answer\"}", "http_completed", false},
		{"unknownStream", 200, "text/plain", "private-answer", "observation_unknown", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				count.Add(1)
				w.Header().Set("Content-Type", tc.kind)
				w.Header().Set("X-Request-Id", "req-safe-123")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, true)
			status, body := requestRelay(t, r, shared.Object{"model": "api-subagents", "stream": tc.stream, "input": "private-prompt"})
			got := awaitRequestResult(t, r)
			if status != tc.status || body != tc.body || count.Load() != 1 {
				t.Fatal("forwarding contract changed", status, body, count.Load())
			}
			if got.Outcome != tc.want || got.HTTPStatus != tc.status || got.UpstreamRequestID != "req-safe-123" || got.FirstByteMS < 0 || got.BytesReceived != int64(len(tc.body)) {
				t.Fatalf("unexpected result: %+v", got)
			}
			encoded, _ := json.Marshal(got)
			for _, secret := range []string{"synthetic-relay-key", "private-prompt", "private-answer"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatal("diagnostic leaked content")
				}
			}
		})
	}
}

// TestRequestHistoryIsolation 验证并发完成不覆盖其他请求、快照独立、有界历史及关闭重启后的代数隔离。
func TestRequestHistoryIsolation(t *testing.T) {
	r := &Relay{state: State{Enabled: true}, epoch: 1}
	r.OnChange = func() { _ = r.Snapshot() }
	profile := configstore.Profile{Model: "mock-model", APIKey: "secret"}
	r.mu.Lock()
	one := r.startTraceLocked("one", profile)
	two := r.startTraceLocked("two", profile)
	r.mu.Unlock()
	two.result.Outcome = "completed"
	two.finish()
	one.result.Outcome = "upstream_failed"
	one.finish()
	state := r.Snapshot()
	if state.RecentRequests[0].ID != two.result.ID || state.RecentRequests[0].Outcome != "completed" || state.RecentRequests[1].Outcome != "upstream_failed" {
		t.Fatal("out-of-order finish overwrote another request")
	}
	state.RecentRequests[0].Outcome = "changed"
	if r.Snapshot().RecentRequests[0].Outcome != "completed" {
		t.Fatal("snapshot aliases mutable history")
	}
	r.mu.Lock()
	r.epoch++
	r.state.RecentRequests = nil
	next := r.startTraceLocked("new", profile)
	r.mu.Unlock()
	one.finish()
	if r.Snapshot().RecentRequests[0].ID != next.result.ID {
		t.Fatal("old request crossed restart boundary")
	}
	r.mu.Lock()
	for range recentRequestLimit + 5 {
		r.startTraceLocked("demo", profile)
	}
	r.mu.Unlock()
	if len(r.Snapshot().RecentRequests) != recentRequestLimit {
		t.Fatal("history not bounded")
	}
	r.mu.Lock()
	r.state.Enabled = false
	r.state.RecentRequests = nil
	r.mu.Unlock()
	next.finish()
	if len(r.Snapshot().RecentRequests) != 0 {
		t.Fatal("disabled state repopulated")
	}
}

// TestSafeRequestIDs 验证请求标识不能携带密钥、换行、HTML 或任意长正文。
func TestSafeRequestIDs(t *testing.T) {
	for _, value := range []string{"synthetic-relay-key", "prefix-local-token", "sk-private", "<script>", "header\r\nvalue", strings.Repeat("a", 129)} {
		if safeUpstreamRequestID(value, "synthetic-relay-key", "local-token") != "" {
			t.Fatal("unsafe request ID accepted")
		}
	}
	if safeUpstreamRequestID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee") == "" {
		t.Fatal("normal request ID rejected")
	}
}

// TestPassthroughTimeoutCauses 验证首字节、空闲和总时限分别携带可识别原因，客户端取消优先。
func TestPassthroughTimeoutCauses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile configstore.Profile
		touch   bool
		cause   error
		want    string
	}{
		{"first", configstore.Profile{TaskTimeout: 1, FirstTimeout: 1, IdleTimeout: 10}, false, errFirstByteTimeout, "first_byte_timeout"},
		{"idle", configstore.Profile{TaskTimeout: 1, FirstTimeout: 10, IdleTimeout: 1}, true, errStreamIdleTimeout, "stream_idle_timeout"},
		{"total", configstore.Profile{TaskTimeout: 0, FirstTimeout: 10, IdleTimeout: 10}, false, errRequestTimeout, "request_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, touch, closeRequest := passthroughContext(context.Background(), tc.profile)
			defer closeRequest()
			if tc.touch {
				touch()
			}
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("timeout did not fire")
			}
			if !errors.Is(context.Cause(ctx), tc.cause) || interruptionOutcome(context.Background(), context.Cause(ctx), "error") != tc.want {
				t.Fatal("wrong timeout classification", context.Cause(ctx))
			}
			touch()
			if !errors.Is(context.Cause(ctx), tc.cause) {
				t.Fatal("touch changed cancelled cause")
			}
		})
	}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	if interruptionOutcome(parent, errStreamIdleTimeout, "error") != "client_cancelled" {
		t.Fatal("user cancellation became an upstream failure")
	}
}
