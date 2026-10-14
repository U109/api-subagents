package providers

import (
	"net/http"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// ApplyFast 将模型级开关翻译为厂商协议；关闭不附加参数，网关是否支持需由上游确认。
func ApplyFast(p configstore.Profile, headers http.Header, body shared.Object) {
	if !p.FastMode() {
		return
	}
	switch p.Protocol {
	case "responses", "compatible":
		// priority 与 fast 在官网协议中等价，保留 priority 以兼容已有中转服务。
		body["service_tier"] = "priority"
	case "anthropic":
		body["speed"] = "fast"
		headers.Set("anthropic-beta", "fast-mode-2026-02-01")
	}
}
