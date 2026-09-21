package providers

import (
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestResponsesDoneEvent 验证兼容网关使用 response.done 时仍能完成 Responses 响应。
func TestResponsesDoneEvent(t *testing.T) {
	var result shared.Object
	decoder := NewEventDecoder(func(event, data string) error {
		acc := newAccumulator("responses")
		if err := acc.accept(event, data); err != nil {
			return err
		}
		result = acc.result
		return nil
	})
	body := `event: response.done
data: {"type":"response.done","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}}

`
	if err := decoder.Feed([]byte(body), true); err != nil {
		t.Fatal(err)
	}
	if result == nil || shared.Str(result["status"]) != "completed" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

// TestResponsesFailedEventKeepsSafeReason 验证失败事件保留有限错误原因，便于区分模型、额度和服务故障。
func TestResponsesFailedEventKeepsSafeReason(t *testing.T) {
	var got error
	decoder := NewEventDecoder(func(event, data string) error {
		acc := newAccumulator("responses")
		got = acc.accept(event, data)
		return got
	})
	body := `event: response.failed
data: {"type":"response.failed","response":{"error":{"code":"model_not_found","message":"model unavailable"}}}

`
	if err := decoder.Feed([]byte(body), true); err == nil {
		t.Fatal("expected response failure")
	}
	if !strings.Contains(got.Error(), "model unavailable") {
		t.Fatalf("error lost upstream reason: %v", got)
	}
}
