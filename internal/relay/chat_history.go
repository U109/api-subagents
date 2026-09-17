package relay

import (
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"
)

// chatHistory 保存 CPA 标准格式无法携带的工具回合元数据；只驻留内存，不写任务记录或磁盘。
type chatHistory struct {
	mu    sync.Mutex
	items map[[32]byte]chatHistoryEntry
	bytes int
}

type chatHistoryEntry struct {
	data    []byte
	expires time.Time
}

// chatScope 将凭据、线路、模型和会话标识纳入不可逆指纹，防止换模型或换 Key 后串用思考历史。
func chatScope(p configstore.Profile, session string) [32]byte {
	return sha256.Sum256(shared.Marshal([]string{p.BaseURL, p.APIKey, p.Model, p.Protocol, providers.Compatibility(p), session}))
}

// chatHistoryKey 按用户历史与完整工具身份匹配；正文或参数不一致就不回放，不能仅凭重复 call_id 命中。
func chatHistoryKey(scope [32]byte, users []any, calls []any) [32]byte {
	identities := []any{}
	for _, raw := range calls {
		call := shared.Obj(raw)
		identities = append(identities, shared.Object{"id": call["id"], "function": call["function"]})
	}
	return sha256.Sum256(shared.Marshal([]any{scope, users, identities}))
}

// restore 只补上经过完整终态校验的原始字段，CPA 的工具名称、参数、正文和普通历史保持原值。
func (h *chatHistory) restore(scope [32]byte, body shared.Object) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prune(time.Now())
	users := []any{}
	for _, raw := range shared.Arr(body["messages"]) {
		message := shared.Obj(raw)
		if message["role"] == "user" || message["role"] == "system" || message["role"] == "developer" {
			users = append(users, raw)
		}
		calls := shared.Arr(message["tool_calls"])
		if message["role"] != "assistant" || len(calls) == 0 {
			continue
		}
		entry, exists := h.items[chatHistoryKey(scope, users, calls)]
		if !exists {
			continue
		}
		var metadata shared.Object
		if json.Unmarshal(entry.data, &metadata) != nil {
			continue
		}
		for _, key := range []string{"reasoning_content", "reasoning", "reasoning_details"} {
			if value, exists := metadata[key]; exists {
				message[key] = value
			}
		}
		extras := shared.Arr(metadata["tool_extras"])
		for index, rawCall := range calls {
			if index < len(extras) && extras[index] != nil {
				shared.Obj(rawCall)["extra_content"] = extras[index]
			}
		}
	}
}

// remember 在完整工具回合结束后保存最小元数据，最多 256 轮／16 MB／30 分钟，超额淘汰最旧项。
func (h *chatHistory) remember(scope [32]byte, request, message shared.Object) {
	calls := shared.Arr(message["tool_calls"])
	if len(calls) == 0 {
		return
	}
	metadata := shared.Object{}
	for _, key := range []string{"reasoning_content", "reasoning", "reasoning_details"} {
		if value, exists := message[key]; exists {
			metadata[key] = value
		}
	}
	extras := []any{}
	hasExtras := false
	for _, raw := range calls {
		extra := shared.Obj(raw)["extra_content"]
		extras = append(extras, extra)
		hasExtras = hasExtras || extra != nil
	}
	if hasExtras {
		metadata["tool_extras"] = extras
	}
	if len(metadata) == 0 {
		return
	}
	data := shared.Marshal(metadata)
	if len(data) > 2*1024*1024 {
		return
	}
	users := []any{}
	for _, raw := range shared.Arr(request["messages"]) {
		message := shared.Obj(raw)
		if message["role"] == "user" || message["role"] == "system" || message["role"] == "developer" {
			users = append(users, raw)
		}
	}
	key := chatHistoryKey(scope, users, calls)
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	h.prune(now)
	if h.items == nil {
		h.items = map[[32]byte]chatHistoryEntry{}
	}
	if old, exists := h.items[key]; exists {
		h.bytes -= len(old.data)
		delete(h.items, key)
	}
	for len(h.items) >= 256 || h.bytes+len(data) > 16*1024*1024 {
		var oldest [32]byte
		var expiry time.Time
		for key, value := range h.items {
			if expiry.IsZero() || value.expires.Before(expiry) {
				oldest, expiry = key, value.expires
			}
		}
		h.bytes -= len(h.items[oldest].data)
		delete(h.items, oldest)
	}
	h.items[key] = chatHistoryEntry{data: data, expires: now.Add(30 * time.Minute)}
	h.bytes += len(data)
}

// prune 在持锁访问时清理过期数据，避免后台定时器及无界历史积累。
func (h *chatHistory) prune(now time.Time) {
	for key, entry := range h.items {
		if !entry.expires.After(now) {
			h.bytes -= len(entry.data)
			delete(h.items, key)
		}
	}
}

// clear 在关闭挟持模式时释放思考历史，不影响用户原有 Codex 对话。
func (h *chatHistory) clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.items = nil
	h.bytes = 0
}
