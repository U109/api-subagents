package relay

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/providers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// forwardResponses 替换模型及凭据，仅在普通生成请求未给出额度时补模型预算；正文和返回流保持原样。
// 桌面普通 Responses 请求仅在上游返回 502/503、尚未向客户端输出时有限重试；worker、compact、连接错误与已开始的流不重试。
// SSE 只旁观终止状态，不改写正文、不合成完成事件、不保存思考历史；各次尝试共享原首包及总时限，取消会终止请求和退避。
func (r *Relay) forwardResponses(w http.ResponseWriter, req *http.Request, original []byte, profile configstore.Profile, trace *requestTrace) {
	defer trace.finish()
	body, err := sjson.SetBytes(original, "model", profile.Model)
	if err != nil {
		trace.result.Outcome = "invalid_request"
		replyError(w, http.StatusBadRequest, "请求 JSON 无效")
		return
	}
	// null、零值或非标准输出字段也视为调用方显式参数，交给上游校验；压缩接口不接受生成额度。
	if req.URL.Path == "/v1/responses" && !gjson.GetBytes(original, "max_output_tokens").Exists() && !gjson.GetBytes(original, "max_tokens").Exists() && !gjson.GetBytes(original, "max_completion_tokens").Exists() {
		if limit := profile.OutputLimit(profile.Model); limit > 0 {
			body, err = sjson.SetBytes(body, "max_output_tokens", limit)
			if err != nil {
				trace.result.Outcome = "invalid_request"
				replyError(w, http.StatusBadRequest, "无法设置模型输出额度")
				return
			}
		}
	}
	endpoint, _, _ := providers.RequestSpec(profile, nil, "", nil)
	if req.URL.Path == "/v1/responses/compact" {
		endpoint = strings.TrimSuffix(endpoint, "/responses") + "/responses/compact"
	}
	if req.URL.RawQuery != "" {
		endpoint += "?" + req.URL.RawQuery
	}
	ctx, touch, closeRequest := passthroughContext(req.Context(), profile)
	defer closeRequest()
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		trace.result.Outcome = "invalid_request"
		replyError(w, http.StatusBadRequest, "API 地址无效")
		return
	}
	copyPassthroughHeaders(upstream.Header, req.Header)
	if profile.APIKey != "" {
		upstream.Header.Set("Authorization", "Bearer "+profile.APIKey)
	}
	res, err := r.doResponsesWithRecovery(upstream, r.workerConfig == nil && req.URL.Path == "/v1/responses")
	if err != nil {
		trace.result.Outcome = interruptionOutcome(req.Context(), context.Cause(ctx), "upstream_connect_error")
		if req.Context().Err() != nil {
			return
		}
		status := http.StatusBadGateway
		if ctx.Err() != nil {
			status = http.StatusGatewayTimeout
		}
		replyError(w, status, "上游连接失败或响应超时")
		return
	}
	defer res.Body.Close()
	trace.result.HTTPStatus = res.StatusCode
	trace.result.UpstreamRequestID = safeUpstreamRequestID(res.Header.Get("X-Request-Id"), profile.APIKey, req.Header.Get("X-Api-Subagents-Token"))
	trace.publish()
	mediaType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	observe := mediaType == "text/event-stream" && (res.Header.Get("Content-Encoding") == "" || res.Header.Get("Content-Encoding") == "identity")
	observer := streamObserver{}
	copyPassthroughHeaders(w.Header(), res.Header)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(res.StatusCode)
	if flush, ok := w.(http.Flusher); ok {
		flush.Flush()
	}
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := res.Body.Read(buffer)
		if n > 0 {
			touch()
			firstByte := trace.result.FirstByteMS < 0
			trace.received(n)
			if firstByte {
				trace.publish()
			}
			if observe {
				observer.Feed(buffer[:n])
			}
			if _, err := w.Write(buffer[:n]); err != nil {
				trace.result.Outcome = interruptionOutcome(req.Context(), context.Cause(ctx), "downstream_write_error")
				return
			}
			if flush, ok := w.(http.Flusher); ok {
				flush.Flush()
			}
		}
		if readErr == io.EOF {
			switch {
			case req.Context().Err() != nil || ctx.Err() != nil:
				trace.result.Outcome = interruptionOutcome(req.Context(), context.Cause(ctx), "upstream_read_error")
			case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
				trace.result.Outcome = "http_auth_error"
			case res.StatusCode < 200 || res.StatusCode >= 300:
				trace.result.Outcome = "http_error"
			case observe:
				trace.result.Outcome = observer.Result()
			case req.URL.Path == "/v1/responses" && gjson.GetBytes(original, "stream").Bool():
				trace.result.Outcome = "observation_unknown"
			default:
				trace.result.Outcome = "http_completed"
			}
			return
		}
		if readErr != nil {
			trace.result.Outcome = interruptionOutcome(req.Context(), context.Cause(ctx), "upstream_read_error")
			if req.Context().Err() != nil {
				return
			}
			// 已发送的字节不能撤回；中止 HTTP 传输，让客户端识别断流，不往上游正文中插入本地错误事件。
			panic(http.ErrAbortHandler)
		}
	}
}

// copyPassthroughHeaders 保留协议、会话与用量头，剔除逐跳字段及连接凭据；本地令牌和浏览器身份不会泄露到上游。
func copyPassthroughHeaders(dst, src http.Header) {
	blocked := map[string]bool{
		"connection": true, "proxy-connection": true, "keep-alive": true, "transfer-encoding": true,
		"te": true, "trailer": true, "upgrade": true, "content-length": true, "host": true,
		"authorization": true, "proxy-authorization": true, "proxy-authenticate": true,
		"cookie": true, "set-cookie": true, "origin": true, "referer": true,
		"x-api-key": true, "x-goog-api-key": true,
	}
	for _, value := range src.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			blocked[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	for name, values := range src {
		lower := strings.ToLower(name)
		if blocked[lower] || strings.HasPrefix(lower, "x-api-subagents-") {
			continue
		}
		dst[name] = append([]string(nil), values...)
	}
}

// passthroughContext 分别标记总时限、首字节和空闲超时；代数检查避免旧计时回调误取消新数据。
func passthroughContext(parent context.Context, p configstore.Profile) (context.Context, func(), func()) {
	task, cancelTask := context.WithTimeoutCause(parent, time.Duration(p.TaskTimeout)*time.Minute, errRequestTimeout)
	ctx, cancel := context.WithCancelCause(task)
	var mu sync.Mutex
	var timer *time.Timer
	generation := 0
	closed := false
	arm := func(wait time.Duration, cause error) {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		generation++
		current := generation
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(wait, func() {
			mu.Lock()
			defer mu.Unlock()
			if !closed && current == generation {
				cancel(cause)
			}
		})
	}
	arm(time.Duration(p.FirstTimeout)*time.Second, errFirstByteTimeout)
	touch := func() { arm(time.Duration(p.IdleTimeout)*time.Second, errStreamIdleTimeout) }
	closeRequest := func() {
		mu.Lock()
		closed = true
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
		cancel(nil)
		cancelTask()
	}
	return ctx, touch, closeRequest
}
