package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"
)

// TestReasoningStaysSeparate 使用合成思考字段验证两种 Gemini 接入路径，不应把标准思考内容改成普通正文。
func TestReasoningStaysSeparate(t *testing.T) {
	for _, protocol := range []string{"compatible", "gemini"} {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%v", protocol, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					if protocol == "compatible" {
						if stream {
							io.WriteString(w, "data: {\"id\":\"chat1\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"synthetic-reasoning\"}}]}\n\n")
							io.WriteString(w, upstreamReply(protocol, true))
						} else {
							io.WriteString(w, `{"id":"chat1","choices":[{"message":{"role":"assistant","reasoning_content":"synthetic-reasoning","content":"OK"},"finish_reason":"stop"}]}`)
						}
					} else {
						body := `{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"synthetic-reasoning","thought":true},{"text":"OK"}]},"finishReason":"STOP"}]}`
						if stream {
							body = "data: " + body + "\n\n"
						}
						io.WriteString(w, body)
					}
				}))
				defer server.Close()
				r := testRelay(t, protocol, server.URL, stream)
				status, body := requestRelay(t, r, shared.Object{"model": "api-subagents", "stream": true, "input": "synthetic request"})
				if status != 200 || strings.Contains(body, "response.failed") {
					t.Fatal(status, body)
				}
				var final shared.Object
				decoder := providers.NewEventDecoder(func(_ string, data string) error {
					var event shared.Object
					if err := json.Unmarshal([]byte(data), &event); err != nil {
						return err
					}
					if event["type"] == "response.output_text.delta" && strings.Contains(shared.Str(event["delta"]), "synthetic-reasoning") {
						t.Error("reasoning emitted as text delta")
					}
					if event["type"] == "response.completed" {
						final = shared.Obj(event["response"])
					}
					return nil
				})
				if err := decoder.Feed([]byte(body), true); err != nil {
					t.Fatal(err)
				}
				thought, answer := false, false
				for _, value := range shared.Arr(final["output"]) {
					item := shared.Obj(value)
					if item["type"] == "reasoning" && strings.Contains(string(shared.Marshal(item)), "synthetic-reasoning") {
						thought = true
					}
					if item["type"] == "message" {
						if strings.Contains(string(shared.Marshal(item)), "synthetic-reasoning") {
							t.Error("reasoning in final message")
						}
						answer = strings.Contains(string(shared.Marshal(item)), "OK")
					}
				}
				if !thought || !answer {
					t.Fatal("reasoning or answer lost", body)
				}
			})
		}
	}
}

// TestResponsesPreservesCompletionContract 透传不伪造完成或失败事件，缺少完成事件由 Codex 判断，尾帧保持原样。
func TestResponsesPreservesCompletionContract(t *testing.T) {
	partial := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"
	for _, tc := range []struct {
		name, data string
		complete   bool
	}{
		{"truncated", partial, false},
		{"doneWithoutCompletion", partial + "data: [DONE]\n\n", false},
		{"onlyDone", "data: [DONE]\n\n", false},
		{"complete", upstreamReply("responses", true), true},
		{"trailingFrame", upstreamReply("responses", true) + "data: {\"type\":\"keepalive\"}\n\ndata: [DONE]\n\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests++
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, tc.data)
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, true)
			status, body := requestRelay(t, r, shared.Object{"model": "api-subagents", "stream": true, "input": "synthetic request"})
			if requests != 1 {
				t.Fatal("unexpected retry")
			}
			if status != 200 || body != tc.data {
				t.Fatal("Responses stream was rewritten", status, body)
			}
			if tc.complete {
				if status != 200 || !strings.Contains(body, "response.completed") || strings.Contains(body, "response.failed") {
					t.Fatal(status, body)
				}
			} else if strings.Contains(body, "response.completed") {
				t.Fatal("incomplete stream treated as success", status, body)
			}
		})
	}
}
