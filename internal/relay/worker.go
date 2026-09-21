package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"time"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	configstore "github.com/U109/api-subagents/internal/config"
)

// WorkerGateway 仅为一个执行任务提供临时入口；不会启用挟持模式或修改用户的 Codex 配置。
type WorkerGateway struct {
	Address string
	Token   string
	relay   *Relay
}

// requestConfig 让执行任务使用提交时的单连接快照，普通挟持模式仍读取最新连接配置。
func (r *Relay) requestConfig() (configstore.Config, error) {
	if r.workerConfig != nil {
		return *r.workerConfig, nil
	}
	return r.Store.Read()
}

// StartWorkerGateway 启动带随机令牌的回环网关，复用协议适配但不把真实供应商凭据写入文件。
// maxRequests 限制整个任务的模型请求数量；取消上下文会中止所有正在转发的请求。
func StartWorkerGateway(ctx context.Context, profile configstore.Profile, maxRequests int, client *http.Client) (*WorkerGateway, error) {
	if maxRequests < 1 {
		return nil, errors.New("执行型子代理需要正数模型请求上限。")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	// 去掉其他候选模型和环境变量引用，防止任务中途改配置或换模型扩大路由范围。
	profile.APIKeyEnv = ""
	profile.RelayModels = nil
	profile.ModelOrder = nil
	c := configstore.Config{Version: 1, MaxConcurrent: 1, Models: map[string]configstore.Profile{"worker": profile}}
	r := New(nil, codexconfig.CodexConfig{})
	r.workerConfig, r.requestLimit = &c, maxRequests
	if client != nil {
		r.Client = client
	}
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.token, r.host = hex.EncodeToString(key), listener.Addr().String()
	r.state = State{Enabled: true, Model: "worker", Address: "http://" + r.host + "/v1"}
	r.server = &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = r.server.Serve(listener) }()
	return &WorkerGateway{Address: r.state.Address, Token: r.token, relay: r}, nil
}

// Usage 返回已发送的模型请求数及是否拒绝过超额请求，不包含密钥或任务正文。
func (g *WorkerGateway) Usage() (int, bool) {
	g.relay.mu.RLock()
	defer g.relay.mu.RUnlock()
	return int(g.relay.state.Requests), g.relay.limitExceeded
}

// Close 撤销临时入口、取消上游请求并清除内存中的协议元数据；不调用配置恢复逻辑。
func (g *WorkerGateway) Close() {
	g.relay.cancel()
	_ = g.relay.server.Close()
	g.relay.chatHistory.clear()
}
