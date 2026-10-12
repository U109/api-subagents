package relay

import (
	"strings"
	"testing"
)

// TestStreamObserverChunks 验证跨字节分块、各类换行、多行数据和终止状态，不把 DONE 或未闭合帧当成功。
func TestStreamObserverChunks(t *testing.T) {
	for _, tc := range []struct{ name, data, want string }{
		{"complete", "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n", "completed"},
		{"dataOnly", "data: {\"type\":\"response.completed\"}\n\n", "completed"},
		{"crlf", ": keepalive\r\nevent: response.failed\r\ndata: {\"type\":\"response.failed\",\"error\":\"private\"}\r\n\r\n", "upstream_failed"},
		{"cr", "data: {\"type\":\"response.incomplete\"}\r\r", "incomplete"},
		{"multiline", "data: {\n" + "data: \"type\":\"response.completed\"}\n\n", "completed"},
		{"error", "data: {\"type\":\"error\"}\n\n", "upstream_failed"},
		{"done", "data: [DONE]\n\n", "unexpected_eof"},
		{"unclosed", "data: {\"type\":\"response.completed\"}\n", "unexpected_eof"},
		{"noData", "event: response.completed\n\n", "unexpected_eof"},
		{"badJSON", "data: {invalid}\n\n", "observation_unknown"},
		{"conflict", "event: response.completed\ndata: {\"type\":\"response.failed\"}\n\n", "observation_unknown"},
		{"failureSticky", "data: {\"type\":\"response.failed\"}\n\ndata: {\"type\":\"response.completed\"}\n\n", "upstream_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, size := range []int{1, 2, 7, len(tc.data)} {
				var s streamObserver
				for i := 0; i < len(tc.data); i += size {
					s.Feed([]byte(tc.data[i:min(i+size, len(tc.data))]))
				}
				if got := s.Result(); got != tc.want {
					t.Fatalf("chunk=%d: %s, want %s", size, got, tc.want)
				}
			}
		})
	}
}

// TestStreamObserverBounded 验证巨大工具事件不会无限缓冲或阻止后续终止事件识别。
func TestStreamObserverBounded(t *testing.T) {
	var s streamObserver
	s.Feed([]byte("data: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\""))
	chunk := []byte(strings.Repeat("x", 8192))
	for range 512 {
		s.Feed(chunk)
	}
	s.Feed([]byte("\"}\n\n"))
	if len(s.line) > observationLimit || len(s.data) > observationLimit || s.Result() != "observation_unknown" {
		t.Fatal("observer exceeded bound or guessed result")
	}
	s.Feed([]byte("data: {\"type\":\"response.completed\"}\n\n"))
	if s.Result() != "completed" {
		t.Fatal("terminal event was lost")
	}
	var named streamObserver
	named.Feed([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"output\":\""))
	for range 20 {
		named.Feed(chunk)
	}
	named.Feed([]byte("\"}\n\n"))
	if named.Result() != "completed" {
		t.Fatal("named large terminal event was lost")
	}
}
