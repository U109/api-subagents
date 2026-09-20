package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/providers"
	"github.com/tidwall/sjson"
)

// forwardResponses 只替换已选模型及上游凭据，逐块转发 Responses 正文；参数、事件和错误由上游与 Codex 解释。
// 不解析或改写 SSE，不合成完成事件，不缓存思考历史；客户端取消和超时会终止上游请求，失败不重试。
func (r *Relay) forwardResponses(w http.ResponseWriter, req *http.Request, original []byte, profile configstore.Profile) {
	body, err := sjson.SetBytes(original, "model", profile.Model)
	if err != nil {
		replyError(w, http.StatusBadRequest, "请求 JSON 无效")
		return
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
		replyError(w, http.StatusBadRequest, "API 地址无效")
		return
	}
	copyPassthroughHeaders(upstream.Header, req.Header)
	if profile.APIKey != "" {
		upstream.Header.Set("Authorization", "Bearer "+profile.APIKey)
	}
	res, err := r.Client.Do(upstream)
	if err != nil {
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
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			if flush, ok := w.(http.Flusher); ok {
				flush.Flush()
			}
		}
		if readErr == io.EOF {
			return
		}
		if readErr != nil {
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

// passthroughContext 只管理总时限、首包和空闲超时；代数检查避免旧计时回调取消刚收到数据的请求。
func passthroughContext(parent context.Context, p configstore.Profile) (context.Context, func(), func()) {
	task, cancelTask := context.WithTimeout(parent, time.Duration(p.TaskTimeout)*time.Minute)
	ctx, cancel := context.WithCancelCause(task)
	var mu sync.Mutex
	var timer *time.Timer
	generation := 0
	closed := false
	arm := func(wait time.Duration) {
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
				cancel(errors.New("上游响应超时"))
			}
		})
	}
	arm(time.Duration(p.FirstTimeout) * time.Second)
	touch := func() { arm(time.Duration(p.IdleTimeout) * time.Second) }
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
