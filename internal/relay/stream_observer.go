package relay

import (
	"bytes"
	"encoding/json"
	"strings"
)

const observationLimit = 64 * 1024

// streamObserver 只旁观 SSE 的类型字段；缓冲有界，绝不修改、延迟或保存完整响应。
type streamObserver struct {
	line, data                  []byte
	event, terminal             string
	lineSkipped, dataSkipped    bool
	hasData, afterCR, uncertain bool
}

// Feed 接受任意分块的响应字节，兼容 LF、CRLF 和 CR；超长行只影响诊断，不影响透传。
func (s *streamObserver) Feed(chunk []byte) {
	for _, b := range chunk {
		if s.afterCR {
			s.afterCR = false
			if b == '\n' {
				continue
			}
		}
		if b == '\r' || b == '\n' {
			s.consumeLine()
			s.afterCR = b == '\r'
			continue
		}
		if len(s.line) < observationLimit {
			s.line = append(s.line, b)
		} else {
			s.lineSkipped = true
		}
	}
}

// consumeLine 解析 SSE 字段，多行 data 按协议合并；注释和未知字段不进入诊断内容。
func (s *streamObserver) consumeLine() {
	defer func() { s.line = s.line[:0]; s.lineSkipped = false }()
	if s.lineSkipped {
		if bytes.HasPrefix(s.line, []byte("data:")) {
			s.hasData, s.dataSkipped = true, true
		} else if len(s.line) == 0 || s.line[0] != ':' {
			s.uncertain = true
		}
		return
	}
	if len(s.line) == 0 {
		s.consumeEvent()
		return
	}
	field, value, _ := bytes.Cut(s.line, []byte(":"))
	value = bytes.TrimPrefix(value, []byte(" "))
	switch string(field) {
	case "event":
		// 只保留已知类型；任意上游字符串不会进入状态或日志。
		s.event = observedTerminal(string(value))
	case "data":
		s.hasData = true
		if len(s.data)+len(value)+1 > observationLimit {
			s.dataSkipped = true
		} else if !s.dataSkipped {
			s.data = append(s.data, value...)
			s.data = append(s.data, '\n')
		}
	}
}

// consumeEvent 仅在空行结束事件时确认终态；失败优先于完成，超长事件可由显式事件名识别。
func (s *streamObserver) consumeEvent() {
	defer func() {
		s.data = s.data[:0]
		s.event, s.hasData, s.dataSkipped = "", false, false
	}()
	if !s.hasData {
		return
	}
	kind := s.event
	if s.dataSkipped {
		if kind == "" {
			s.uncertain = true
		}
	} else if strings.TrimSpace(string(s.data)) != "[DONE]" {
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(s.data, &event) != nil {
			s.uncertain = true
			return
		}
		fromData := observedTerminal(event.Type)
		if kind != "" && event.Type != "" && kind != fromData {
			s.uncertain = true
			return
		}
		if fromData != "" {
			kind = fromData
		}
	} else {
		// [DONE] 是传输结束提示，不是 Responses 成功完成的证明。
		return
	}
	if kind != "" && (s.terminal == "" || s.terminal == "completed") {
		s.terminal = kind
	}
}

// observedTerminal 将协议事件映射到固定诊断枚举，不保留上游正文、错误消息或未知类型。
func observedTerminal(kind string) string {
	switch kind {
	case "response.completed":
		return "completed"
	case "response.failed", "error":
		return "upstream_failed"
	case "response.incomplete":
		return "incomplete"
	default:
		return ""
	}
}

// Result 在 EOF 时返回诊断；未以空行闭合的尾帧不当作完成，超过观察上限时不武断报缺帧。
func (s *streamObserver) Result() string {
	if s.terminal != "" {
		return s.terminal
	}
	if s.uncertain || s.lineSkipped || s.dataSkipped {
		return "observation_unknown"
	}
	return "unexpected_eof"
}
