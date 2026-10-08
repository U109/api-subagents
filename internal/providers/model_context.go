package providers

import (
	"github.com/U109/api-subagents/internal/shared"
	"math"
)

// declaredModelContext 只读取目录显式容量字段；不按模型 ID 推测，不把最大输出额度当作上下文。
func declaredModelContext(entry shared.Object) int {
	for _, key := range []string{"context_window", "context_length", "inputTokenLimit"} {
		value, ok := entry[key].(float64)
		if ok && value > 0 && value <= 100000000 && math.Trunc(value) == value {
			return int(value)
		}
	}
	return 0
}
