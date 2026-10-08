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
)

// TestResponsesPassthroughCompactionHistoryPreserved 验证接近故障请求大小的合成压缩历史逐字透传，保留加密推理、压缩元数据与完整响应，不接触真实 Codex 配置或模型。
func TestResponsesPassthroughCompactionHistoryPreserved(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	for _, tc := range []struct {
		name, path, contentType, response string
		stream                            bool
	}{
		{
			name:        "localSummaryResponses",
			path:        "/responses",
			contentType: "text/event-stream",
			response:    "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-summary\",\"status\":\"completed\",\"output\":[]}}\n\n",
			stream:      true,
		},
		{
			name:        "remoteCompact",
			path:        "/responses/compact",
			contentType: "application/json",
			response:    `{"id":"synthetic-compact","output":[{"type":"compaction","encrypted_content":"synthetic-opaque-result"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := []byte(fmt.Sprintf(`{ "model":"api-subagents", "stream":%t, "store":false, "input":[{"type":"reasoning","id":"synthetic-reasoning","encrypted_content":"%s","summary":[]},{"role":"user","content":"Summarize only this synthetic history: %s"}], "tools":[], "include":["reasoning.encrypted_content"], "reasoning":{"effort":"xhigh"}, "vendor":{"integer":9007199254740993} }`, tc.stream, strings.Repeat("synthetic_opaque_", 32000), strings.Repeat("synthetic_history_", 21000)))
			metadata := `{"compaction":{"trigger":"auto","phase":"mid_turn"}}`
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				count.Add(1)
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Error(err)
					return
				}
				want := bytes.Replace(original, []byte(`"model":"api-subagents"`), []byte(`"model":"mock-model"`), 1)
				if !bytes.Equal(body, want) {
					t.Errorf("compaction history changed: got %d bytes, want %d", len(body), len(want))
				}
				if req.URL.Path != "/v1"+tc.path || req.Header.Get("X-Codex-Turn-Metadata") != metadata {
					t.Error("compaction route or metadata changed")
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("X-Request-Id", "synthetic-compaction-request")
				io.WriteString(w, tc.response)
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL+"/v1", true)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Snapshot().Address+tc.path, bytes.NewReader(original))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Api-Subagents-Token", r.token)
			req.Header.Set("X-Codex-Turn-Metadata", metadata)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil || res.StatusCode != http.StatusOK || string(body) != tc.response {
				t.Fatalf("compaction response changed: status=%d, body=%q, err=%v", res.StatusCode, body, err)
			}
			if res.Header.Get("X-Request-Id") != "synthetic-compaction-request" || count.Load() != 1 || len(r.chatHistory.items) != 0 {
				t.Fatal("compaction request ID lost, request replayed, or reasoning history cached")
			}
		})
	}
}

// TestResponsesPassthroughIncompletePreludePreservesFinalFailure 验证纯思考前导恢复耗尽后保留最后一次 EOF 或失败事件，不伪造完成、不缓存历史，分片边界不影响原始字节。
func TestResponsesPassthroughIncompletePreludePreservesFinalFailure(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	for _, tc := range []struct {
		name, tail string
	}{
		{"silentEOF", ""},
		{"failedEvent", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"upstream_error\",\"message\":\"stream closed before response.completed\"}}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"synthetic-partial\",\"status\":\"in_progress\"}}\n\nevent: response.reasoning_summary_text.delta\ndata: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"synthetic partial reasoning\"}\n\n" + tc.tail
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				count.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Request-Id", "synthetic-incomplete-request")
				// 刻意拆开 SSE 事件与字段，确保网关不会依赖读取边界改写正文。
				for offset := 0; offset < len(payload); offset += 17 {
					io.WriteString(w, payload[offset:min(offset+17, len(payload))])
					w.(http.Flusher).Flush()
				}
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, true)
			r.responsesRetryWait = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Snapshot().Address+"/responses", strings.NewReader(`{"model":"api-subagents","stream":true,"input":[]}`))
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
			if err != nil || res.StatusCode != http.StatusOK || string(body) != payload {
				t.Fatalf("incomplete response changed: status=%d, body=%q, err=%v", res.StatusCode, body, err)
			}
			if res.Header.Get("X-Request-Id") != "synthetic-incomplete-request" || count.Load() != 4 || len(r.chatHistory.items) != 0 {
				t.Fatal("final request ID lost, recovery budget exceeded, or reasoning history cached")
			}
		})
	}
}
