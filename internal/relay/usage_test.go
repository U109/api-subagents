package relay

import (
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestNormalizePartialUsage 部分兼容服务缺少统计字段时补零而不估算，也不覆盖上游已有用量。
func TestNormalizePartialUsage(t *testing.T) {
	value := shared.Object{"usage": shared.Object{"input_tokens": 9, "output_tokens": 4}}
	normalizeUsage(value)
	usage := shared.Obj(value["usage"])
	if usage["input_tokens"] != 9 || usage["output_tokens"] != 4 || usage["total_tokens"] != 0 || shared.Obj(usage["input_tokens_details"])["cached_tokens"] != 0 {
		t.Fatal(usage)
	}
	usage["total_tokens"] = 17
	normalizeUsage(value)
	if usage["total_tokens"] != 17 {
		t.Fatal("overwrote upstream usage")
	}
}
