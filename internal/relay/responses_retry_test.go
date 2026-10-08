package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type responsesRoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip 将请求交给本地合成传输层，不访问真实服务，用于准确计数错误与取消边界。
func (f responsesRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// responsesTestRequest 创建可重放的合成 JSON 请求；测试上下文不使用用户的 Codex 配置。
func responsesTestRequest(t *testing.T, ctx context.Context) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://synthetic.invalid/v1/responses", strings.NewReader(`{"model":"mock-model","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// TestResponsesRetryDelay 验证冷却秒数和 HTTP 日期不能缩短默认退避，超长或溢出的冷却不被擅自截短。
func TestResponsesRetryDelay(t *testing.T) {
	now := time.Date(2026, 10, 8, 6, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header string
		attempt      int
		want         time.Duration
		retry        bool
	}{
		{"first", "", 0, 2 * time.Second, true},
		{"second", "", 1, 15 * time.Second, true},
		{"third", "", 2, 45 * time.Second, true},
		{"exhausted", "", 3, 0, false},
		{"negativeAttempt", "", -1, 0, false},
		{"seconds", " 30 ", 0, 30 * time.Second, true},
		{"zeroDoesNotSpin", "0", 0, 2 * time.Second, true},
		{"shortCooldown", "7", 1, 15 * time.Second, true},
		{"maximumCooldown", "60", 0, time.Minute, true},
		{"longCooldown", "61", 0, 0, false},
		{"overflow", "18446744073709551616", 0, 0, false},
		{"invalid", "later", 0, 2 * time.Second, true},
		{"negative", "-1", 0, 2 * time.Second, true},
		{"date", now.Add(30 * time.Second).Format(http.TimeFormat), 0, 30 * time.Second, true},
		{"pastDate", now.Add(-time.Minute).Format(http.TimeFormat), 0, 2 * time.Second, true},
		{"longDate", now.Add(61 * time.Second).Format(http.TimeFormat), 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, retry := responsesRetryDelay(tc.header, tc.attempt, now)
			if got != tc.want || retry != tc.retry {
				t.Fatalf("delay=%v retry=%v, want %v/%v", got, retry, tc.want, tc.retry)
			}
		})
	}
}

// TestResponsesTransientRecovery 验证自动压缩使用的普通 Responses 请求在 502/503 后恢复，正文与身份不变且失败响应头不泄漏到成功流。
func TestResponsesTransientRecovery(t *testing.T) {
	original := []byte(`{ "model":"api-subagents", "stream":true, "max_output_tokens":128, "store":true, "instructions":"Summarize the conversation", "vendor":9007199254740993, "previous_response_id":"opaque", "input":[] }`)
	wantBody := bytes.Replace(original, []byte(`"model":"api-subagents"`), []byte(`"model":"mock-model"`), 1)
	wantStream := ": first chunk\n\nevent: response.completed\ndata: {\"opaque\":9007199254740993}\n\n"
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		attempt := count.Add(1)
		body, err := io.ReadAll(req.Body)
		if err != nil || !bytes.Equal(body, wantBody) || req.URL.RawQuery != "trace=1" {
			t.Error("recovery changed request", err, string(body), req.URL.RawQuery)
		}
		if req.Header.Get("Authorization") != "Bearer synthetic-relay-key" || req.Header.Get("X-Api-Subagents-Token") != "" || req.Header.Get("Session_id") != "opaque-session" || req.Header.Get("X-Codex-Turn-State") != "opaque-state" {
			t.Error("recovery changed credentials or protocol state")
		}
		if attempt <= 2 {
			w.Header().Set("X-Request-Id", "rejected-request")
			w.Header().Set("X-Rejected-Only", "must-not-leak")
			w.Header().Set("Retry-After", "7")
			w.WriteHeader([]int{http.StatusBadGateway, http.StatusServiceUnavailable}[attempt-1])
			io.WriteString(w, `{"error":{"message":"synthetic unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "recovered-request")
		io.WriteString(w, wantStream)
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	waits := make(chan time.Duration, 3)
	r.responsesRetryWait = func(ctx context.Context, delay time.Duration) error { waits <- delay; return ctx.Err() }
	req, _ := http.NewRequest(http.MethodPost, r.Snapshot().Address+"/responses?trace=1", bytes.NewReader(original))
	req.Header.Set("X-Api-Subagents-Token", r.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Session_id", "opaque-session")
	req.Header.Set("X-Codex-Turn-State", "opaque-state")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK || string(body) != wantStream || count.Load() != 3 {
		t.Fatal("recovery failed or changed stream", res.StatusCode, err, string(body), count.Load())
	}
	if res.Header.Get("X-Request-Id") != "recovered-request" || res.Header.Get("X-Rejected-Only") != "" || res.Header.Get("Retry-After") != "" {
		t.Fatal("intermediate headers leaked", res.Header)
	}
	if len(waits) != 2 || !reflect.DeepEqual([]time.Duration{<-waits, <-waits}, []time.Duration{7 * time.Second, 15 * time.Second}) {
		t.Fatal("cooldown not respected")
	}
}

// TestResponsesRecoveryExhaustedPreservesFinalRejection 验证持续 502/503 最多追加三次请求，最终错误正文、状态与请求 ID 不被较早的错误覆盖。
func TestResponsesRecoveryExhaustedPreservesFinalRejection(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempt := count.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if attempt < 4 {
					w.Header().Set("X-Request-Id", "synthetic-earlier-rejection")
					w.WriteHeader(http.StatusBadGateway)
					io.WriteString(w, `{"error":{"message":"earlier rejection"}}`)
					return
				}
				w.Header().Set("X-Request-Id", "synthetic-final-rejection")
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"message":"final rejection"}}`)
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, true)
			waits := make(chan time.Duration, 4)
			r.responsesRetryWait = func(ctx context.Context, delay time.Duration) error {
				waits <- delay
				return ctx.Err()
			}
			req, err := http.NewRequest(http.MethodPost, r.Snapshot().Address+"/responses", strings.NewReader(`{"model":"api-subagents","stream":true}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Api-Subagents-Token", r.token)
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil || count.Load() != 4 || res.StatusCode != status || string(body) != `{"error":{"message":"final rejection"}}` || res.Header.Get("X-Request-Id") != "synthetic-final-rejection" || res.Header.Get("Retry-After") != "0" {
				t.Fatal("final rejection changed or recovery limit exceeded", err, count.Load(), res.StatusCode, string(body), res.Header)
			}
			gotWaits := make([]time.Duration, 0, len(waits))
			for len(waits) > 0 {
				gotWaits = append(gotWaits, <-waits)
			}
			if !reflect.DeepEqual(gotWaits, []time.Duration{2 * time.Second, 15 * time.Second, 45 * time.Second}) {
				t.Fatal("recovery backoff changed", gotWaits)
			}
		})
	}
}

// TestResponsesRecoveryStopsBeforeReplay 验证鉴权/额度错误、500/504、不可重放正文、长冷却和已知短截止时间均保留第一次响应。
func TestResponsesRecoveryStopsBeforeReplay(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		header     string
		noBodyCopy bool
		deadline   bool
	}{
		{"unauthorized", 401, "", false, false},
		{"forbidden", 403, "", false, false},
		{"quotaOrRateLimit", 429, "7", false, false},
		{"internalError", 500, "", false, false},
		{"gatewayTimeout", 504, "", false, false},
		{"longCooldown", 503, "61", false, false},
		{"unrepeatableBody", 502, "", true, false},
		{"insufficientDeadline", 503, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.deadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			count := 0
			r := &Relay{Client: &http.Client{Transport: responsesRoundTripFunc(func(*http.Request) (*http.Response, error) {
				count++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Retry-After": {tc.header}, "X-Request-Id": {"original"}}, Body: io.NopCloser(strings.NewReader("original rejection"))}, nil
			})}, responsesRetryWait: func(context.Context, time.Duration) error { t.Error("unexpected wait"); return nil }}
			req := responsesTestRequest(t, ctx)
			if tc.noBodyCopy {
				req.GetBody = nil
			}
			res, err := r.doResponsesWithRecovery(req, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if count != 1 || res.StatusCode != tc.status || res.Header.Get("X-Request-Id") != "original" || string(body) != "original rejection" {
				t.Fatal("first rejection not preserved", count, res.StatusCode, string(body))
			}
		})
	}
}

// TestResponsesTransportErrorNeverReplays 验证未获得 HTTP 响应时不能判断上游是否已生成，因此网络错误不自动重放。
func TestResponsesTransportErrorNeverReplays(t *testing.T) {
	want := errors.New("synthetic connection reset")
	count := 0
	r := &Relay{Client: &http.Client{Transport: responsesRoundTripFunc(func(*http.Request) (*http.Response, error) { count++; return nil, want })}}
	res, err := r.doResponsesWithRecovery(responsesTestRequest(t, context.Background()), true, nil)
	if res != nil || !errors.Is(err, want) || count != 1 {
		t.Fatal("ambiguous transport error replayed", count, err)
	}
}

// TestResponsesCancellationDuringRecovery 验证客户端在退避中取消可立即退出，取消后即使等待函数错误返回成功也不能发出下一次请求。
func TestResponsesCancellationDuringRecovery(t *testing.T) {
	for _, realWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelBeforeReplay", true: "cancelDuringTimer"}[realWait], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var count atomic.Int32
			waiting := make(chan struct{})
			r := &Relay{Client: &http.Client{Transport: responsesRoundTripFunc(func(*http.Request) (*http.Response, error) {
				count.Add(1)
				return &http.Response{StatusCode: 502, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("rejection"))}, nil
			})}}
			r.responsesRetryWait = func(ctx context.Context, delay time.Duration) error {
				close(waiting)
				if realWait {
					return waitResponsesRetry(ctx, delay)
				}
				cancel()
				return nil
			}
			request := responsesTestRequest(t, ctx)
			done := make(chan error, 1)
			go func() { _, err := r.doResponsesWithRecovery(request, true, nil); done <- err }()
			select {
			case <-waiting:
			case <-time.After(3 * time.Second):
				t.Fatal("retry wait not entered")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || count.Load() != 1 {
					t.Fatal("cancelled request replayed", count.Load(), err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancel waited for backoff")
			}
		})
	}
}

// TestResponsesRecoveryHonorsFirstDeadline 验证退避不刷新首包计时，超时返回本地 504 且只发送一次上游请求。
func TestResponsesRecoveryHonorsFirstDeadline(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		w.WriteHeader(502)
		io.WriteString(w, "rejection")
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	c, err := r.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	p := c.Models["demo"]
	p.FirstTimeout = 1
	// 直接传入短计时参数验证底层边界，避免绕过配置层对正式等待时间的最小值校验。
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	body := []byte(`{"model":"api-subagents","stream":true}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://local.invalid/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.mu.Lock()
	trace := r.startTraceLocked("demo", p)
	r.mu.Unlock()
	r.forwardResponses(response, req, body, p, trace)
	if response.Code != http.StatusGatewayTimeout || count.Load() != 1 {
		t.Fatal("first deadline reset by recovery", response.Code, count.Load())
	}
	if result := awaitRequestResult(t, r); result.Outcome != "first_byte_timeout" {
		t.Fatalf("first-byte timeout diagnosis lost during recovery: %+v", result)
	}
}

// TestResponsesWorkerAndCompactRemainSingleAttempt 验证 worker 模型调用上限及 compact 压缩路径保持单次调用，不因恢复策略产生隐藏请求。
func TestResponsesWorkerAndCompactRemainSingleAttempt(t *testing.T) {
	for _, worker := range []bool{false, true} {
		t.Run(map[bool]string{false: "compact", true: "worker"}[worker], func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				count.Add(1)
				w.WriteHeader(503)
				io.WriteString(w, "original rejection")
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, true)
			address, token := r.Snapshot().Address+"/responses/compact", r.token
			var gateway *WorkerGateway
			if worker {
				c, err := r.Store.Read()
				if err != nil {
					t.Fatal(err)
				}
				gateway, err = StartWorkerGateway(context.Background(), c.Models["demo"], 1, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer gateway.Close()
				address, token = gateway.Address+"/responses", gateway.Token
			}
			req, _ := http.NewRequest(http.MethodPost, address, strings.NewReader(`{"model":"api-subagents","stream":true}`))
			req.Header.Set("X-Api-Subagents-Token", token)
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != 503 || string(body) != "original rejection" || count.Load() != 1 {
				t.Fatal("restricted request retried", res.StatusCode, count.Load(), string(body))
			}
			if worker {
				if used, exceeded := gateway.Usage(); used != 1 || exceeded {
					t.Fatal("worker request budget changed", used, exceeded)
				}
			}
		})
	}
}
