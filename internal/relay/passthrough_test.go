package relay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	configstore "github.com/U109/api-subagents/internal/config"
)

// TestResponsesPassthroughExactBytes 验证仅替换模型和凭据，未知字段、精确数值、原始正文、错误及 SSE 都逐字保留。
func TestResponsesPassthroughExactBytes(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, response string
		status                      int
	}{
		{"json", "application/json", `{ "id":"r1", "usage":{"input_tokens":9007199254740993}, "output":[{"text":"<thinking>literal</thinking>"}], "vendor":{"unknown":true} }`, 200},
		{"sse", "text/event-stream", ": heartbeat\r\nid: 17\r\nretry: 2000\r\nevent: vendor.custom\r\ndata: {\"text\":\"<thinking>literal</thinking>\"}\r\n\r\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"output\":[],\"usage\":{\"input_tokens\":1}}}\n\ndata: [DONE]\n\n", 200},
		{"upstreamError", "application/json", `{"error":{"code":"vendor_specific","message":"synthetic upstream explanation"}}`, 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := []byte(`{ "model":"api-subagents", "stream":true, "store":true, "reasoning":{"effort":"vendor-level"}, "input":[{"role":"user","content":"keep <thinking> code"}], "instructions":"EXACT INSTRUCTIONS", "tools":[{"type":"function","name":"very_long_` + strings.Repeat("tool_", 20) + `","parameters":{"id":"keep","type":"object"}}], "vendor":{"integer":9007199254740993,"decimal":1.234567890123456789}, "previous_response_id":"keep_previous", "include":["reasoning.encrypted_content"] }`)
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				count.Add(1)
				body, _ := io.ReadAll(req.Body)
				want := bytes.Replace(original, []byte(`"model":"api-subagents"`), []byte(`"model":"mock-model"`), 1)
				if !bytes.Equal(body, want) {
					t.Errorf("unexpected request rewrite: %s", body)
				}
				if req.Header.Get("Authorization") != "Bearer synthetic-relay-key" || req.Header.Get("X-Api-Subagents-Token") != "" || req.Header.Get("Cookie") != "" || req.Header.Get("X-Local-Hop") != "" {
					t.Error("credential or hop header forwarding")
				}
				if req.Header.Get("Session_id") != "synthetic-session" || req.Header.Get("X-Codex-Turn-State") != "opaque-state" {
					t.Error("protocol state lost")
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("X-Request-Id", "vendor-request")
				w.Header().Set("X-Codex-Turn-State", "next-state")
				w.Header().Set("Retry-After", "7")
				w.Header().Set("Set-Cookie", "not-for-local-client=1")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.response)
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, false)
			c, _ := r.Store.Read()
			p := c.Models["demo"]
			p.ReasoningEffort = "low"
			p.ModelCompatibility = map[string]string{p.Model: "minimax"}
			c.Models["demo"] = p
			if err := r.Store.Save(c); err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest("POST", r.Snapshot().Address+"/responses", bytes.NewReader(original))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Api-Subagents-Token", r.token)
			req.Header.Set("Authorization", "Bearer synthetic-local-auth")
			req.Header.Set("Cookie", "synthetic-local-cookie=1")
			req.Header.Set("Session_id", "synthetic-session")
			req.Header.Set("X-Codex-Turn-State", "opaque-state")
			req.Header.Set("Connection", "X-Local-Hop")
			req.Header.Set("X-Local-Hop", "private")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil || res.StatusCode != tc.status || string(body) != tc.response {
				t.Fatal("response changed", res.StatusCode, err, string(body))
			}
			if res.Header.Get("X-Request-Id") != "vendor-request" || res.Header.Get("X-Codex-Turn-State") != "next-state" || res.Header.Get("Retry-After") != "7" || res.Header.Get("Set-Cookie") != "" {
				t.Fatal("response protocol headers changed", res.Header)
			}
			if count.Load() != 1 || len(r.chatHistory.items) != 0 {
				t.Fatal("unexpected retry or reasoning cache")
			}
		})
	}
}

// TestResponsesPassthroughStreamingCancellation 验证首帧在上游结束前可见，客户端取消立即传给上游，不等待完整响应。
func TestResponsesPassthroughStreamingCancellation(t *testing.T) {
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ": first chunk\n\n")
		w.(http.Flusher).Flush()
		<-req.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", r.Snapshot().Address+"/responses", strings.NewReader(`{"model":"api-subagents","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Subagents-Token", r.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, len(": first chunk\n\n"))
	if _, err = io.ReadFull(res.Body, first); err != nil || string(first) != ": first chunk\n\n" {
		t.Fatal("stream buffered", err)
	}
	cancel()
	res.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not reach upstream")
	}
}

// TestResponsesPassthroughReadFailure 验证半途网络错误表现为 HTTP 断流，不插入 response.failed 或重试。
func TestResponsesPassthroughReadFailure(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		count.Add(1)
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprint(buffer, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 1000\r\n\r\n: partial\n\n")
		buffer.Flush()
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	req, _ := http.NewRequest("POST", r.Snapshot().Address+"/responses", strings.NewReader(`{"model":"api-subagents","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Subagents-Token", r.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err == nil || string(body) != ": partial\n\n" || count.Load() != 1 {
		t.Fatal("broken stream hidden or rewritten", err, string(body), count.Load())
	}
}

// TestPassthroughDeadline 验证透传的首包与空闲时限独立于内容解释，触发取消后不能再被触摸恢复。
func TestPassthroughDeadline(t *testing.T) {
	p := configstore.Profile{TaskTimeout: 1, FirstTimeout: 1, IdleTimeout: 1}
	ctx, touch, closeRequest := passthroughContext(context.Background(), p)
	defer closeRequest()
	touch()
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("idle timeout missing")
	}
	touch()
	if ctx.Err() == nil {
		t.Fatal("cancelled request restarted")
	}
}

// TestResponsesPassthroughModelSwitchAndCompact 验证一个连接切换模型仅改变 model，compact 的其他参数与响应不被剥离。
func TestResponsesPassthroughModelSwitchAndCompact(t *testing.T) {
	got := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		got <- req.URL.RequestURI() + " " + string(body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"vendor_compaction":"opaque"}`)
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL+"/v1/responses", true)
	c, _ := r.Store.Read()
	p := c.Models["demo"]
	p.RelayModels = []string{"gemini-alias", "glm-alias"}
	c.Models["demo"] = p
	if err := r.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	for _, model := range p.RelayModels {
		alias := codexconfig.RelayModelAlias("demo", model)
		req, _ := http.NewRequest("POST", r.Snapshot().Address+"/responses/compact?trace=1", strings.NewReader(fmt.Sprintf(`{"model":%q,"store":true,"stream":false,"vendor":123}`, alias)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Api-Subagents-Token", r.token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if string(body) != `{"vendor_compaction":"opaque"}` {
			t.Fatal("compact response rewritten")
		}
		want := fmt.Sprintf(`/v1/responses/compact?trace=1 {"model":%q,"store":true,"stream":false,"vendor":123}`, model)
		if actual := <-got; actual != want {
			t.Fatal("compact request changed", actual)
		}
	}
}
