//go:build windows

package desktop

import (
	"context"
	"fmt"
	"github.com/U109/api-subagents/internal/shared"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestProbeCancellation 验证取消只影响对应测试，重复 ID 被拒绝，完成后清理注册项。
func TestProbeCancellation(t *testing.T) {
	a := &App{ctx: context.Background()}
	first, finish, err := a.startProbeContext("probe-first-123")
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	second, finishSecond, err := a.startProbeContext("probe-second-123")
	if err != nil {
		t.Fatal(err)
	}
	defer finishSecond()
	if _, _, err = a.startProbeContext("probe-first-123"); err == nil {
		t.Fatal("duplicate active request accepted")
	}
	if err = a.CancelProbe("probe-first-123"); err != nil {
		t.Fatal(err)
	}
	if first.Err() != context.Canceled || second.Err() != nil {
		t.Fatal("cancellation leaked across requests")
	}
	finish()
	if len(a.probes) != 1 {
		t.Fatal("completed request not removed")
	}
}

// TestAPIProbeCancellationStopsLocalRequest 用本地阻塞接口验证 Go 绑定的取消能到达 HTTP 请求，不接触真实服务。
func TestAPIProbeCancellationStopsLocalRequest(t *testing.T) {
	started := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		select {
		case <-r.Context().Done():
			stopped <- struct{}{}
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()
	a := closeTestApp(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.ctx = ctx
	body := shared.Marshal(shared.Object{"requestId": "probe-cancel-integration", "name": "demo", "config": shared.Object{"version": 1, "models": shared.Object{"demo": shared.Object{"protocol": "responses", "model": "mock-model", "baseUrl": server.URL, "apiKey": "synthetic-probe-key", "stream": true}}}})
	done := make(chan error, 1)
	go func() { _, err := a.API("/api/probe", string(body)); done <- err }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("probe finished too early: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not reach local server")
	}
	if err := a.CancelProbe("probe-cancel-integration"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled probe reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not cancel")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP request was not cancelled")
	}
	if a.busy.Load() != 0 {
		t.Fatal("request busy state leaked")
	}
}

// TestProbeCancellationBeforeStart 验证 IPC 取消先到时测试不会继续，以及无效或大量请求号不无限占内存。
func TestProbeCancellationBeforeStart(t *testing.T) {
	a := &App{ctx: context.Background()}
	if a.CancelProbe("bad") == nil {
		t.Fatal("invalid request ID accepted")
	}
	if _, _, err := a.startProbeContext("bad"); err == nil {
		t.Fatal("invalid start accepted")
	}
	if err := a.CancelProbe("probe-early-123"); err != nil {
		t.Fatal(err)
	}
	ctx, finish, err := a.startProbeContext("probe-early-123")
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if ctx.Err() != context.Canceled {
		t.Fatal("early cancel was lost")
	}
	for n := 0; n < 100; n++ {
		if err := a.CancelProbe(fmt.Sprintf("late-cancel-%03d", n)); err != nil {
			t.Fatal(err)
		}
	}
	if len(a.probeCancelled) > 32 {
		t.Fatal("unbounded cancellation history")
	}
}
