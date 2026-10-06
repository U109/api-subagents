package relay

import (
	"context"
	"errors"
	"strings"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

const recentRequestLimit = 20

var (
	errFirstByteTimeout  = errors.New("等待上游首个响应字节超时")
	errStreamIdleTimeout = errors.New("上游响应流空闲超时")
	errRequestTimeout    = errors.New("上游请求超过总时限")
)

// RequestResult 仅含固定状态及脱敏元数据；不存储提示词、回答、密钥或任意上游错误正文。
type RequestResult struct {
	ID                string `json:"id"`
	Connection        string `json:"connection"`
	Model             string `json:"model"`
	StartedAt         string `json:"startedAt"`
	Outcome           string `json:"outcome"`
	HTTPStatus        int    `json:"httpStatus"`
	UpstreamRequestID string `json:"upstreamRequestId,omitempty"`
	FirstByteMS       int64  `json:"firstByteMs"`
	DurationMS        int64  `json:"durationMs"`
	BytesReceived     int64  `json:"bytesReceived"`
}

type requestTrace struct {
	relay  *Relay
	epoch  uint64
	start  time.Time
	result RequestResult
}

// startTraceLocked 在持有网关锁时创建最近请求条目，按开始顺序保留有界历史，互不覆盖并发请求。
func (r *Relay) startTraceLocked(connection string, profile configstore.Profile) *requestTrace {
	t := &requestTrace{relay: r, epoch: r.epoch, start: time.Now()}
	t.result = RequestResult{ID: shared.UUID(), Connection: configstore.Redact(connection, profile), Model: configstore.Redact(profile.Model, profile), StartedAt: t.start.UTC().Format(time.RFC3339Nano), Outcome: "receiving", FirstByteMS: -1}
	r.state.RecentRequests = append([]RequestResult{t.result}, r.state.RecentRequests...)
	if len(r.state.RecentRequests) > recentRequestLimit {
		r.state.RecentRequests = r.state.RecentRequests[:recentRequestLimit]
	}
	return t
}

// received 记录首个正文字节耗时和字节数，不拷贝或保留实际响应内容。
func (t *requestTrace) received(n int) {
	if t.result.FirstByteMS < 0 {
		t.result.FirstByteMS = time.Since(t.start).Milliseconds()
	}
	t.result.BytesReceived += int64(n)
}

// finish 只更新同一次启用周期中的匹配条目；关闭或重启后旧请求不能污染新状态。
func (t *requestTrace) finish() {
	t.result.DurationMS = time.Since(t.start).Milliseconds()
	if t.result.Outcome == "receiving" {
		t.result.Outcome = "observation_unknown"
	}
	t.publish()
}

// publish 在响应头、首字节和结束时发布快照，不逐 token 通知；锁外回调允许 UI 安全读取状态。
func (t *requestTrace) publish() {
	r := t.relay
	r.mu.Lock()
	updated := false
	if r.state.Enabled && r.epoch == t.epoch {
		for i := range r.state.RecentRequests {
			if r.state.RecentRequests[i].ID == t.result.ID {
				r.state.RecentRequests[i] = t.result
				updated = true
				break
			}
		}
	}
	r.mu.Unlock()
	if updated {
		r.notify()
	}
}

// interruptionOutcome 优先识别客户端取消，再按明确的本地取消原因分类；不泄露底层错误正文。
func interruptionOutcome(parent context.Context, cause error, fallback string) string {
	if parent.Err() != nil {
		return "client_cancelled"
	}
	switch {
	case errors.Is(cause, errFirstByteTimeout):
		return "first_byte_timeout"
	case errors.Is(cause, errStreamIdleTimeout):
		return "stream_idle_timeout"
	case errors.Is(cause, errRequestTimeout):
		return "request_timeout"
	default:
		return fallback
	}
}

// safeUpstreamRequestID 限制请求标识长度和字符，并排除已知凭据及常见 Key 前缀，避免诊断回显密钥。
func safeUpstreamRequestID(value string, secrets ...string) string {
	if len(value) == 0 || len(value) > 128 || strings.HasPrefix(strings.ToLower(value), "sk-") {
		return ""
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(value, secret) {
			return ""
		}
	}
	for _, ch := range value {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
			return ""
		}
	}
	return value
}
