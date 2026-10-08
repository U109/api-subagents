package relay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const syntheticResponsesPrelude = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"failed-prelude\",\"status\":\"in_progress\",\"output\":[]}}\n\nevent: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"synthetic reasoning not yet committed\"}\n\n"

const syntheticResponsesPreludeFailure = "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"upstream_error\",\"message\":\"stream closed before response.completed\"}}}\n\n"

// requestPreludeRelay 通过真实回环 HTTP 读取网关结果，限时并关闭响应；合成请求不会访问真实模型或用户配置。
func requestPreludeRelay(t *testing.T, r *Relay, body []byte) (*http.Response, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Snapshot().Address+"/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Subagents-Token", r.token)
	req.Header.Set("Session_id", "synthetic-prelude-session")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, data
}

// TestResponsesPreludeEligible 验证普通流式生成、客户端工具和内置只读搜索声明可恢复；后台任务、已有会话、执行型服务端工具及未知结构保守拒绝。
func TestResponsesPreludeEligible(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		eligible   bool
	}{
		{"plain", `{"stream":true}`, true},
		{"clientFunctions", `{"stream":true,"store":false,"background":false,"tools":[{"type":"function","name":"exec_command"}]}`, true},
		{"clientCustomTool", `{"stream":true,"tools":[{"type":"custom","name":"apply_patch"}]}`, true},
		{"clientNamespace", `{"stream":true,"tools":[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"exec_command"},{"type":"custom","name":"apply_patch"}]}]}`, true},
		{"unsafeNamespace", `{"stream":true,"tools":[{"type":"namespace","name":"functions","tools":[{"type":"mcp"}]}]}`, false},
		{"malformedNamespace", `{"stream":true,"tools":[{"type":"namespace","name":"functions"}]}`, false},
		{"summary", `{"stream":true,"tools":[],"input":[{"role":"user","content":"synthetic summary"}]}`, true},
		{"notStreamed", `{"stream":false}`, false},
		{"stringStream", `{"stream":"true"}`, false},
		{"invalidJSON", `{"stream":true`, false},
		{"background", `{"stream":true,"background":true}`, false},
		{"previousResponse", `{"stream":true,"previous_response_id":"opaque"}`, false},
		{"conversation", `{"stream":true,"conversation":{"id":"opaque"}}`, false},
		{"agent", `{"stream":true,"agent":{}}`, false},
		{"multiAgent", `{"stream":true,"multi_agent":true}`, false},
		{"hostedMCP", `{"stream":true,"tools":[{"type":"mcp"}]}`, false},
		{"readOnlySearch", `{"stream":true,"tools":[{"type":"web_search"}]}`, true},
		{"codeInterpreter", `{"stream":true,"tools":[{"type":"code_interpreter"}]}`, false},
		{"unknownTool", `{"stream":true,"tools":[{"type":"future_tool"}]}`, false},
		{"malformedTools", `{"stream":true,"tools":{}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := responsesPreludeEligible([]byte(tc.body)); got != tc.eligible {
				t.Fatalf("eligible=%v, want %v", got, tc.eligible)
			}
		})
	}
}

// TestResponsesPreludeRecovery 验证纯思考断流、明确瞬态错误和空流可在同一预算内恢复，失败前导与身份头不混入成功响应，请求正文不被重新编码。
func TestResponsesPreludeRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, tail string
	}{
		{"silentEOF", syntheticResponsesPrelude, ""},
		{"heartbeatAfterMetadata", syntheticResponsesPrelude + ": alive\n\n", ""},
		{"failedEvent", syntheticResponsesPrelude, syntheticResponsesPreludeFailure},
		{"emptyStream", "", ""},
		{"protectionError", syntheticResponsesPrelude, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"internal_error\",\"message\":\"response protection is unavailable\"}}\n\n"},
		{"bareStreamError", syntheticResponsesPrelude, "event: error\ndata: {\"type\":\"error\",\"message\":\"stream closed before response.completed\"}\n\n"},
		{"multilineCRLF", ": heartbeat\r\nid: opaque\r\nretry: 3000\r\nevent: response.created\r\ndata: {\"type\":\"response.created\",\r\ndata: \"response\":{\"id\":\"failed-prelude\",\"output\":[]}}\r\n\r\n", ""},
		{"truncatedTransfer", syntheticResponsesPrelude, "broken HTTP body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := []byte(`{ "model":"api-subagents", "stream":true, "store":false, "tools":[{"type":"function","name":"exec_command"}], "input":[], "include":["reasoning.encrypted_content"], "vendor":9007199254740993 }`)
			wantRequest := bytes.Replace(original, []byte(`"model":"api-subagents"`), []byte(`"model":"mock-model"`), 1)
			wantResponse := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"recovered-prelude\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":9007199254740993}}}\n\n"
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				attempt := count.Add(1)
				body, err := io.ReadAll(req.Body)
				if err != nil || !bytes.Equal(body, wantRequest) || req.Header.Get("Session_id") != "synthetic-prelude-session" || req.Header.Get("Authorization") != "Bearer synthetic-relay-key" {
					t.Error("recovery changed request bytes or identity", err)
				}
				if attempt == 1 {
					if tc.name == "truncatedTransfer" {
						conn, buffered, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.Close()
						fmt.Fprintf(buffered, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n%s", len(tc.prefix)+100, tc.prefix)
						buffered.Flush()
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("X-Request-Id", "failed-prelude-request")
					w.Header().Set("X-Failed-Only", "must-not-leak")
					io.WriteString(w, tc.prefix+tc.tail)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				w.Header().Set("X-Request-Id", "recovered-prelude-request")
				io.WriteString(w, wantResponse)
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, true)
			waits := make(chan time.Duration, 3)
			r.responsesRetryWait = func(ctx context.Context, delay time.Duration) error { waits <- delay; return ctx.Err() }
			res, body := requestPreludeRelay(t, r, original)
			if res.StatusCode != http.StatusOK || string(body) != wantResponse || count.Load() != 2 || res.Header.Get("X-Request-Id") != "recovered-prelude-request" || res.Header.Get("X-Failed-Only") != "" {
				t.Fatal("safe prelude not recovered cleanly", res.StatusCode, count.Load(), string(body), res.Header)
			}
			if len(waits) != 1 || <-waits != 2*time.Second || len(r.chatHistory.items) != 0 {
				t.Fatal("retry budget changed or prelude persisted")
			}
		})
	}
}

// TestResponsesPreludeCommitBoundaries 验证正文、工具、未知格式、非瞬态错误与缓存上限都是交付边界，不能恢复或伪造完成，原始字节逐一保留。
func TestResponsesPreludeCommitBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, barrier string
	}{
		{"text", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"visible output\"}\n\n"},
		{"messageItem", "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"content\":[]}}\n\n"},
		{"functionCall", "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":\"exec_command\"}}\n\n"},
		{"functionDone", "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\"}}\n\n"},
		{"functionArguments", "data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{}\"}\n\n"},
		{"customTool", "data: {\"type\":\"response.custom_tool_call_input.delta\",\"delta\":\"synthetic\"}\n\n"},
		{"hostedTool", "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"mcp_call\"}}\n\n"},
		{"searchCall", "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"web_search_call\"}}\n\n"},
		{"searchStarted", "data: {\"type\":\"response.web_search_call.in_progress\",\"item_id\":\"search-call\"}\n\n"},
		{"unknownEvent", "event: vendor.custom\ndata: {\"vendor\":true}\n\n"},
		{"unknownField", "future-field: retain this\n\n"},
		{"mismatchedEvent", "event: response.created\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"output\"}\n\n"},
		{"metadataError", "data: {\"type\":\"response.created\",\"response\":{\"error\":{\"code\":\"invalid_request\"},\"status\":\"failed\"}}\n\n"},
		{"metadataCompleted", "data: {\"type\":\"response.in_progress\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"},
		{"malformedJSON", "data: {not JSON}\n\n"},
		{"normalCompletion", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"},
		{"normalIncomplete", "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\"}}\n\n"},
		{"contextError", "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"stream closed before response.completed\"}}}\n\n"},
		{"quotaError", "data: {\"type\":\"error\",\"code\":\"insufficient_quota\",\"message\":\"upstream unavailable\"}\n\n"},
		{"unknownError", "data: {\"type\":\"error\",\"code\":\"future_error\",\"message\":\"stream closed before response.completed\"}\n\n"},
		{"invalidWrappedAsInternal", "data: {\"type\":\"error\",\"error\":{\"code\":\"invalid_request\",\"type\":\"internal_error\",\"message\":\"stream closed before response.completed\"}}\n\n"},
		{"bufferLimit", ":" + strings.Repeat("x", responsesPreludeLimit+17) + "\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := syntheticResponsesPrelude + tc.barrier + syntheticResponsesPreludeFailure
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				count.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, payload)
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, true)
			r.responsesRetryWait = func(context.Context, time.Duration) error {
				t.Error("committed stream retried")
				return context.Canceled
			}
			res, body := requestPreludeRelay(t, r, []byte(`{"model":"api-subagents","stream":true}`))
			if res.StatusCode != http.StatusOK || string(body) != payload || count.Load() != 1 {
				t.Fatal("commit boundary altered or replayed", tc.name, res.StatusCode, count.Load(), len(body))
			}
		})
	}
}

// TestResponsesPreludeUnsafeRequestsRemainSingleAttempt 验证执行型服务端工具、后台任务、持久会话与 worker 即使只有思考前导也不自动重放。
func TestResponsesPreludeUnsafeRequestsRemainSingleAttempt(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		worker     bool
	}{
		{"hostedMCP", `{"model":"api-subagents","stream":true,"tools":[{"type":"mcp"}]}`, false},
		{"codeInterpreter", `{"model":"api-subagents","stream":true,"tools":[{"type":"code_interpreter"}]}`, false},
		{"background", `{"model":"api-subagents","stream":true,"background":true}`, false},
		{"conversation", `{"model":"api-subagents","stream":true,"conversation":"opaque"}`, false},
		{"worker", `{"model":"api-subagents","stream":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := syntheticResponsesPrelude + syntheticResponsesPreludeFailure
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				count.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, payload)
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, true)
			if tc.worker {
				config, err := r.Store.Read()
				if err != nil {
					t.Fatal(err)
				}
				gateway, err := StartWorkerGateway(context.Background(), config.Models["demo"], 1, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer gateway.Close()
				r = gateway.relay
			}
			r.responsesRetryWait = func(context.Context, time.Duration) error { t.Error("unsafe request retried"); return context.Canceled }
			res, body := requestPreludeRelay(t, r, []byte(tc.body))
			if res.StatusCode != http.StatusOK || string(body) != payload || count.Load() != 1 {
				t.Fatal("unsafe request not preserved", res.StatusCode, count.Load(), string(body))
			}
		})
	}
}

// TestResponsesPreludeSharedRecoveryBudget 验证 HTTP 拒绝与纯思考断流共享三个恢复机会，Retry-After 不被缩短，耗尽后保留最后一次真实错误与请求 ID。
func TestResponsesPreludeSharedRecoveryBudget(t *testing.T) {
	var count atomic.Int32
	final := "final actual upstream rejection"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch count.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, "first rejection")
		case 2:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, syntheticResponsesPrelude)
		case 3:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, syntheticResponsesPrelude+syntheticResponsesPreludeFailure)
		default:
			w.Header().Set("X-Request-Id", "last-prelude-rejection")
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, final)
		}
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	waits := make(chan time.Duration, 3)
	r.responsesRetryWait = func(ctx context.Context, delay time.Duration) error { waits <- delay; return ctx.Err() }
	res, body := requestPreludeRelay(t, r, []byte(`{"model":"api-subagents","stream":true}`))
	if count.Load() != 4 || res.StatusCode != http.StatusServiceUnavailable || string(body) != final || res.Header.Get("X-Request-Id") != "last-prelude-rejection" || len(waits) != 3 {
		t.Fatal("recovery budgets stacked or last rejection lost", count.Load(), res.StatusCode, string(body), len(waits))
	}
	if got := []time.Duration{<-waits, <-waits, <-waits}; !reflect.DeepEqual(got, []time.Duration{30 * time.Second, 15 * time.Second, 45 * time.Second}) {
		t.Fatal("server cooldown shortened or default backoff changed", got)
	}
}

// TestResponsesPreludeCancellation 验证暂存纯思考时客户端取消立即传到上游，不等待完整事件或下一次恢复，也不遗留请求。
func TestResponsesPreludeCancellation(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		count.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, syntheticResponsesPrelude)
		w.(http.Flusher).Flush()
		close(started)
		<-req.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, r.Snapshot().Address+"/responses", strings.NewReader(`{"model":"api-subagents","stream":true}`))
	req.Header.Set("X-Api-Subagents-Token", r.token)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan error, 1)
	go func() {
		res, err := http.DefaultClient.Do(req)
		if res != nil {
			res.Body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("prelude not started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled prelude returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel waited for prelude")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive cancellation")
	}
	if count.Load() != 1 {
		t.Fatal("cancelled prelude replayed")
	}
}

// TestResponsesPreludeCommitDoesNotWaitForEOF 验证正文或工具出现后立即交付已有字节，不等上游结束；之后的失败不会触发重放或阻塞客户端执行工具。
func TestResponsesPreludeCommitDoesNotWaitForEOF(t *testing.T) {
	for _, barrier := range []string{
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"visible answer\"}\n\n",
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":\"exec_command\"}}\n\n",
	} {
		prefix := syntheticResponsesPrelude + barrier
		release := make(chan struct{})
		var count atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			count.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, prefix)
			w.(http.Flusher).Flush()
			select {
			case <-release:
				io.WriteString(w, syntheticResponsesPreludeFailure)
			case <-req.Context().Done():
			}
		}))
		r := testRelay(t, "responses", server.URL, true)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, r.Snapshot().Address+"/responses", strings.NewReader(`{"model":"api-subagents","stream":true}`))
		req.Header.Set("X-Api-Subagents-Token", r.token)
		req.Header.Set("Content-Type", "application/json")
		ready := make(chan error, 1)
		go func() {
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				ready <- err
				return
			}
			defer res.Body.Close()
			body := make([]byte, len(prefix))
			_, err = io.ReadFull(res.Body, body)
			if err == nil && string(body) != prefix {
				err = fmt.Errorf("commit changed prefix")
			}
			ready <- err
			io.Copy(io.Discard, res.Body)
		}()
		var result error
		select {
		case result = <-ready:
		case <-time.After(2 * time.Second):
			result = fmt.Errorf("committed stream waited for EOF")
		}
		close(release)
		cancel()
		server.Close()
		if result != nil || count.Load() != 1 {
			t.Fatal("commit blocked or replayed", result, count.Load())
		}
	}
}

// TestResponsesPreludeDeadlineStopsRecovery 验证前导暂存不刷新总截止时间；空闲超时取消上游后返回真实超时，不再开启新的尝试。
func TestResponsesPreludeDeadlineStopsRecovery(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		count.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, syntheticResponsesPrelude)
		w.(http.Flusher).Flush()
		<-req.Context().Done()
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	r.responsesRetryWait = func(context.Context, time.Duration) error {
		t.Error("expired prelude retried")
		return context.Canceled
	}
	config, err := r.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	profile := config.Models["demo"]
	profile.FirstTimeout, profile.IdleTimeout = 1, 1
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	r.mu.Lock()
	trace := r.startTraceLocked("demo", profile)
	r.mu.Unlock()
	r.forwardResponses(w, req, []byte(`{"model":"api-subagents","stream":true}`), profile, trace)
	if w.Code != http.StatusGatewayTimeout || count.Load() != 1 {
		t.Fatal("prelude timeout hidden or request replayed", w.Code, count.Load())
	}
}

// TestResponsesPreludeCooldownPreservesFailure 验证冷却超过一分钟或放不进剩余截止时间时保留原始 HTTP 200 失败事件，不吞掉请求 ID，也不提前尝试。
func TestResponsesPreludeCooldownPreservesFailure(t *testing.T) {
	for _, cooldown := range []string{"61", "6"} {
		t.Run(cooldown, func(t *testing.T) {
			payload := syntheticResponsesPrelude + syntheticResponsesPreludeFailure
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				count.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Retry-After", cooldown)
				w.Header().Set("X-Request-Id", "original-prelude-error")
				io.WriteString(w, payload)
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, true)
			r.responsesRetryWait = func(context.Context, time.Duration) error { t.Error("cooldown bypassed"); return context.Canceled }
			config, err := r.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			w := httptest.NewRecorder()
			r.mu.Lock()
			trace := r.startTraceLocked("demo", config.Models["demo"])
			r.mu.Unlock()
			r.forwardResponses(w, req, []byte(`{"model":"api-subagents","stream":true}`), config.Models["demo"], trace)
			if w.Code != http.StatusOK || w.Body.String() != payload || count.Load() != 1 || w.Header().Get("X-Request-Id") != "original-prelude-error" || w.Header().Get("Retry-After") != cooldown {
				t.Fatal("original cooldown or failure lost", w.Code, count.Load(), w.Body.String(), w.Header())
			}
		})
	}
}

type responsesTrackedPreludeBody struct {
	reader    io.Reader
	chunkSize int
	closed    *atomic.Int32
}

// Read 以合成分片读取原始响应，用于验证长事件在完成整行之前也刷新活跃计时。
func (b *responsesTrackedPreludeBody) Read(p []byte) (int, error) {
	if b.chunkSize > 0 {
		p = p[:min(len(p), b.chunkSize)]
	}
	return b.reader.Read(p)
}

// Close 记录原始响应的释放次数，不关闭任何真实网络服务或用户资源。
func (b *responsesTrackedPreludeBody) Close() error {
	b.closed.Add(1)
	return nil
}

// TestResponsesPreludeProgressAndClose 验证按收到的分片刷新活跃时间，交回原始字节并且关闭包装响应会释放原始响应。
func TestResponsesPreludeProgressAndClose(t *testing.T) {
	var closed, touched atomic.Int32
	res := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &responsesTrackedPreludeBody{reader: strings.NewReader(syntheticResponsesPrelude), chunkSize: 8, closed: &closed}}
	if !inspectResponsesPrelude(res, func() { touched.Add(1) }) {
		t.Fatal("known prelude EOF not recognized")
	}
	body, err := io.ReadAll(res.Body)
	if err != nil || string(body) != syntheticResponsesPrelude || touched.Load() < 2 {
		t.Fatal("prelude changed or progress not refreshed per chunk", err, touched.Load())
	}
	res.Body.Close()
	if closed.Load() != 1 {
		t.Fatal("original response not closed")
	}
}

// TestResponsesPreludeReleasesBeforeRetry 验证恢复退避开始前已关闭失败流，全部尝试结束后每个原始响应都释放一次，不遗留思考流连接。
func TestResponsesPreludeReleasesBeforeRetry(t *testing.T) {
	var count, closed atomic.Int32
	r := testRelay(t, "responses", "http://synthetic.invalid", true)
	r.Client = &http.Client{Transport: responsesRoundTripFunc(func(*http.Request) (*http.Response, error) {
		payload := syntheticResponsesPrelude
		if count.Add(1) == 3 {
			payload = "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &responsesTrackedPreludeBody{reader: strings.NewReader(payload), closed: &closed}}, nil
	})}
	r.responsesRetryWait = func(ctx context.Context, _ time.Duration) error {
		if closed.Load() != count.Load() {
			t.Error("failed stream still open during backoff")
		}
		return ctx.Err()
	}
	res, body := requestPreludeRelay(t, r, []byte(`{"model":"api-subagents","stream":true}`))
	if res.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"type":"response.completed"`)) || count.Load() != 3 || closed.Load() != 3 {
		t.Fatal("original responses not released exactly once", res.StatusCode, count.Load(), closed.Load(), string(body))
	}
}

// FuzzResponsesPreludePreservesBytes 验证任意格式、分隔符、二进制内容和缓存边界都不能改变交回的流字节；只观察内存中的合成响应，不启动网络或模型。
func FuzzResponsesPreludePreservesBytes(f *testing.F) {
	for _, seed := range []string{
		"", syntheticResponsesPrelude, syntheticResponsesPrelude + syntheticResponsesPreludeFailure,
		": heartbeat\r\nid: opaque\r\ndata: {\"type\":\"vendor\",\"number\":9007199254740993}\r\n\r\n",
		"event: response.created\rdata: {}\r\r", "\x00\x01\xff\n\r\n",
		strings.Repeat("x", responsesPreludeLimit-1), strings.Repeat("x", responsesPreludeLimit+1),
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 1024*1024 {
			t.Skip("bound synthetic fuzz payload")
		}
		res := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(bytes.NewReader(payload))}
		inspectResponsesPrelude(res, nil)
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil || !bytes.Equal(body, payload) {
			t.Fatalf("prelude altered bytes: got=%d want=%d err=%v", len(body), len(payload), err)
		}
	})
}
