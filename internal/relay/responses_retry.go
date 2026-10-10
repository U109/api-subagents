package relay

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// doResponsesWithRecovery 在尚未交付内容时恢复 502/503，或由前导检查确认安全的流失败；未收到 HTTP 响应的连接错误仍不重放。
// 两类错误共用最多三次恢复；正文、模型和身份保持不变，最终响应原样返回，取消与原截止时间覆盖等待和全部尝试。
func (r *Relay) doResponsesWithRecovery(upstream *http.Request, allowRetry bool, inspectPrelude func(*http.Response) bool) (*http.Response, error) {
	wait := r.responsesRetryWait
	if wait == nil {
		wait = waitResponsesRetry
	}
	for attempt := 0; ; attempt++ {
		if err := upstream.Context().Err(); err != nil {
			return nil, err
		}
		request := upstream
		if attempt > 0 {
			body, err := upstream.GetBody()
			if err != nil {
				return nil, err
			}
			request = upstream.Clone(upstream.Context())
			request.Body = body
		}
		res, err := r.Client.Do(request)
		if err != nil || !allowRetry || upstream.GetBody == nil {
			return res, err
		}
		recoverable := res.StatusCode == http.StatusBadGateway || res.StatusCode == http.StatusServiceUnavailable
		if !recoverable && inspectPrelude != nil {
			recoverable = inspectPrelude(res)
		}
		if err := upstream.Context().Err(); err != nil {
			res.Body.Close()
			return nil, err
		}
		if !recoverable {
			return res, nil
		}
		delay, retry := responsesRetryDelay(res.Header.Get("Retry-After"), attempt, time.Now())
		if !retry {
			return res, nil
		}
		// 不提前于服务端冷却时间重试，也不在已知总截止时间内放不下退避时吞掉原始错误。
		if deadline, ok := upstream.Context().Deadline(); ok && time.Until(deadline) <= delay {
			return res, nil
		}
		// 原响应已不再交付，先关闭流再退避，避免失败流占用连接或继续读取上游内容。
		res.Body.Close()
		if err := wait(upstream.Context(), delay); err != nil {
			return nil, err
		}
	}
}

// responsesRetryDelay 为三次恢复提供 2/15/45 秒退避；有效 Retry-After 只能延长等待，超过一分钟则原样交回客户端。
// 秒数与 HTTP 日期均受支持；溢出的数字不能被误当作短等待，非法或过期值回退到默认退避。
func responsesRetryDelay(retryAfter string, attempt int, now time.Time) (time.Duration, bool) {
	delays := [...]time.Duration{2 * time.Second, 15 * time.Second, 45 * time.Second}
	if attempt < 0 || attempt >= len(delays) {
		return 0, false
	}
	delay := delays[attempt]
	value := strings.TrimSpace(retryAfter)
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		if seconds > 60 {
			return 0, false
		}
		if requested := time.Duration(seconds) * time.Second; requested > delay {
			delay = requested
		}
	} else if number, ok := err.(*strconv.NumError); ok && number.Err == strconv.ErrRange {
		return 0, false
	} else if when, err := http.ParseTime(value); err == nil {
		requested := when.Sub(now)
		if requested > time.Minute {
			return 0, false
		}
		if requested > delay {
			delay = requested
		}
	}
	return delay, true
}

// waitResponsesRetry 退避期间不发送伪心跳或响应头；客户端取消和原请求时限会立即终止计时等待。
func waitResponsesRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
