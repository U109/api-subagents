package relay

import (
	"bufio"
	"bytes"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

const responsesPreludeLimit = 256 * 1024

type responsesPreludeDecision uint8

const (
	responsesPreludeHold responsesPreludeDecision = iota
	responsesPreludeCommit
	responsesPreludeRecover
)

type responsesPreludeBody struct {
	reader   io.Reader
	original io.ReadCloser
	readErr  error
}

// Read 先交回观察过的原始字节，再继续上游流；前导读取遇到的传输错误不会被包装成正常 EOF。
func (b *responsesPreludeBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if err == io.EOF && b.readErr != nil {
		err, b.readErr = b.readErr, nil
	}
	return n, err
}

// Close 关闭原始上游响应，确保取消、恢复和正常结束都释放同一条连接。
func (b *responsesPreludeBody) Close() error { return b.original.Close() }

type responsesPreludeProgress struct {
	reader io.Reader
	touch  func()
}

// Read 在观察长事件期间也按实际收到的字节刷新活跃时间，不能把活跃流误判为空闲。
func (p responsesPreludeProgress) Read(b []byte) (int, error) {
	n, err := p.reader.Read(b)
	if n > 0 && p.touch != nil {
		p.touch()
	}
	return n, err
}

// responsesPreludeEligible 只允许不引用持久会话、非后台的普通流式生成进入前导恢复；执行型服务端工具和未知工具类型一律不自动重放。
func responsesPreludeEligible(body []byte) bool {
	if !gjson.ValidBytes(body) || gjson.GetBytes(body, "stream").Type != gjson.True {
		return false
	}
	for _, field := range []string{"background", "conversation", "previous_response_id", "agent", "multi_agent"} {
		value := gjson.GetBytes(body, field)
		if value.Exists() && value.Type != gjson.Null && value.Type != gjson.False {
			return false
		}
	}
	return responsesPreludeSafeTools(gjson.GetBytes(body, "tools"), 0)
}

// responsesPreludeSafeTools 接受客户端函数、自定义工具、命名空间与内置只读网页搜索声明；实际工具事件仍立即交付，执行型服务端工具或过深嵌套禁止恢复。
func responsesPreludeSafeTools(tools gjson.Result, depth int) bool {
	if depth > 4 || (tools.Exists() && !tools.IsArray()) {
		return false
	}
	for _, tool := range tools.Array() {
		switch tool.Get("type").String() {
		case "function", "custom", "web_search":
		case "namespace":
			if !tool.Get("tools").IsArray() || !responsesPreludeSafeTools(tool.Get("tools"), depth+1) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// inspectResponsesPrelude 最多观察 256 KiB 元数据与思考前导；正文、工具、未知事件或达到上限后立即原样交付，只有尚未交付的明确瞬态失败可恢复。
func inspectResponsesPrelude(res *http.Response, touch func()) bool {
	mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	encoding := strings.TrimSpace(res.Header.Get("Content-Encoding"))
	if res.StatusCode != http.StatusOK || err != nil || mediaType != "text/event-stream" || (encoding != "" && !strings.EqualFold(encoding, "identity")) {
		return false
	}
	original := res.Body
	limited := &io.LimitedReader{R: responsesPreludeProgress{reader: original, touch: touch}, N: responsesPreludeLimit}
	reader := bufio.NewReaderSize(limited, 4096)
	var prefix bytes.Buffer
	var readErr error
	defer func() {
		var rest io.Reader = io.MultiReader(reader, original)
		if readErr != nil {
			rest = bytes.NewReader(nil)
		}
		res.Body = &responsesPreludeBody{reader: io.MultiReader(bytes.NewReader(prefix.Bytes()), rest), original: original, readErr: readErr}
	}()
	event := ""
	data := []string{}
	seenResponse := false
	for prefix.Len() < responsesPreludeLimit {
		line, err := reader.ReadString('\n')
		prefix.WriteString(line)
		// LimitReader 的 EOF 只是缓存到达上限，不能误判为上游断流；未解析字节仍交给调用方。
		if limited.N == 0 {
			return false
		}
		if err != nil {
			readErr = err
			// 未完整接收的事件不参与分类，且只恢复已识别的安全前导或完全空的 EOF。
			return seenResponse || (err == io.EOF && prefix.Len() == 0)
		}
		value := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if prefix.Len() == len(line) {
			value = strings.TrimPrefix(value, "\xef\xbb\xbf")
		}
		// 单独 CR、未知 SSE 字段和不可识别格式保守交回客户端，不借机吞掉或修复正文。
		if strings.ContainsRune(value, '\r') {
			return false
		}
		if value == "" {
			if len(data) == 0 {
				// 只有心跳且尚未识别 Responses 元数据时保持原来的立即透传，避免等待正文阻塞传统 SSE 客户端或取消流程。
				if event != "" || !seenResponse {
					return false
				}
				continue
			}
			decision := classifyResponsesPrelude(event, strings.Join(data, "\n"))
			if decision != responsesPreludeHold {
				return decision == responsesPreludeRecover
			}
			seenResponse = true
			event, data = "", nil
			continue
		}
		if strings.HasPrefix(value, ":") {
			continue
		}
		field, content, _ := strings.Cut(value, ":")
		content = strings.TrimPrefix(content, " ")
		switch field {
		case "event":
			event = content
		case "data":
			data = append(data, content)
		case "id", "retry":
		default:
			return false
		}
	}
	return false
}

// classifyResponsesPrelude 只认 Responses 的元数据与思考事件；所有可执行输出、正文、正常结束和未知结构都是不可再重放的交付边界。
func classifyResponsesPrelude(event, data string) responsesPreludeDecision {
	if !gjson.Valid(data) {
		return responsesPreludeCommit
	}
	payload := gjson.Parse(data)
	kind := payload.Get("type").String()
	if event != "" && kind != "" && event != kind {
		return responsesPreludeCommit
	}
	if kind == "" {
		kind = event
	}
	switch kind {
	case "response.created", "response.in_progress", "response.queued":
		response := payload.Get("response")
		if !response.IsObject() {
			return responsesPreludeCommit
		}
		if failure := response.Get("error"); failure.Exists() && failure.Type != gjson.Null {
			return responsesPreludeCommit
		}
		if status := response.Get("status"); status.Exists() && status.String() != "in_progress" && status.String() != "queued" {
			return responsesPreludeCommit
		}
		output := response.Get("output")
		if output.Exists() && !output.IsArray() {
			return responsesPreludeCommit
		}
		for _, item := range output.Array() {
			if item.Get("type").String() != "reasoning" {
				return responsesPreludeCommit
			}
		}
		return responsesPreludeHold
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
		"response.reasoning_text.delta", "response.reasoning_text.done":
		return responsesPreludeHold
	case "response.output_item.added", "response.output_item.done":
		if payload.Get("item.type").String() == "reasoning" {
			return responsesPreludeHold
		}
	case "response.failed", "error":
		if responsesPreludeTransientError(payload) {
			return responsesPreludeRecover
		}
	}
	return responsesPreludeCommit
}

// responsesPreludeTransientError 只恢复已知服务内部错误与明确的流中断；鉴权、限额、上下文与参数错误不会按消息模糊匹配重试。
func responsesPreludeTransientError(payload gjson.Result) bool {
	err := payload.Get("response.error")
	if !err.IsObject() {
		err = payload.Get("error")
	}
	if !err.IsObject() {
		err = payload
	}
	code := strings.ToLower(err.Get("code").String())
	kind := strings.ToLower(err.Get("type").String())
	for _, value := range []string{code, kind} {
		switch value {
		case "rate_limit_exceeded", "rate_limit_error", "insufficient_quota", "quota_exceeded", "billing_error",
			"invalid_api_key", "invalid_request_error", "invalid_request", "bad_request", "invalid_argument", "unsupported_parameter", "unsupported_model", "unsupported_value",
			"context_length_exceeded", "context_window_exceeded", "content_policy_violation", "authentication_error", "permission_denied", "permission_error", "unauthorized", "forbidden", "model_not_found":
			return false
		}
	}
	for _, value := range []string{code, kind} {
		switch value {
		case "server_error", "internal_error", "upstream_error", "service_unavailable", "bad_gateway", "temporarily_unavailable", "stream_error", "connection_error", "response_protection_unavailable":
			return true
		}
	}
	// 顶层 SSE 的 type=error 是事件信封，不是未知业务错误分类；仅此通用信封可继续检查明确的瞬态消息。
	if code != "" || (kind != "" && kind != "error") {
		return false
	}
	message := strings.ToLower(err.Get("message").String())
	return strings.Contains(message, "stream closed before response.completed") || strings.Contains(message, "response protection is unavailable")
}
