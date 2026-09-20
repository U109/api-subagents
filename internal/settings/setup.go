package settings

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"
)

type ConfigService struct {
	Store    *configstore.ConfigStore
	Provider *providers.Provider
	mu       sync.Mutex
}

// NewConfigService 让桌面绑定和浏览器入口共享同一套配置合并与校验逻辑。
func NewConfigService(store *configstore.ConfigStore) *ConfigService {
	return &ConfigService{Store: store, Provider: providers.NewProvider()}
}

// Handle 处理固定路由，网络测试不占写锁；复制和删除只修改选中连接。
func (s *ConfigService) Handle(ctx context.Context, route string, body []byte) (shared.Object, error) {
	if len(body) > 300000 {
		return nil, errors.New("配置过大。")
	}
	if route == "/api/config" && len(body) == 0 {
		c, err := s.Store.Read()
		if err != nil {
			return nil, err
		}
		return shared.Object{"config": configstore.Editable(c), "path": s.Store.Path, "modelContextDefaults": configstore.OfficialModelContexts(), "modelContextFallback": configstore.DefaultContextWindow}, nil
	}
	var input struct {
		Config json.RawMessage `json:"config"`
		Name   string          `json:"name"`
	}
	if json.Unmarshal(body, &input) != nil || strings.TrimSpace(string(body)) == "null" {
		return nil, errors.New("请求 JSON 无效，请检查配置格式。")
	}
	if route == "/api/probe" || route == "/api/models" {
		previous, err := s.Store.Read()
		if err != nil {
			return nil, err
		}
		single, err := selectedConfig(input.Config, input.Name, previous.MaxConcurrent)
		if err != nil {
			return nil, err
		}
		c, err := configstore.MergeKeys(single, previous, route != "/api/models")
		if err != nil {
			return nil, err
		}
		p, err := configstore.ResolveProfile(c, input.Name)
		if err != nil {
			return nil, err
		}
		if route == "/api/models" {
			return s.Provider.ListModels(ctx, p)
		}
		return s.Provider.Probe(ctx, p)
	}
	if route != "/api/config" && route != "/api/config/copy" && route != "/api/config/remove" {
		return nil, errors.New("未找到操作。")
	}
	if !s.mu.TryLock() {
		return nil, errors.New("正在保存，请稍后重试。")
	}
	defer s.mu.Unlock()
	c, err := s.Store.Read()
	if err != nil {
		return nil, err
	}
	copiedName := ""
	switch route {
	case "/api/config/remove":
		if _, ok := c.Models[input.Name]; !ok {
			return nil, errors.New("该模型配置不存在，请刷新后重试。")
		}
		delete(c.Models, input.Name)
	case "/api/config/copy":
		single, err := selectedConfig(input.Config, input.Name, c.MaxConcurrent)
		if err != nil {
			return nil, err
		}
		one, err := configstore.MergeKeys(single, c, true)
		if err != nil {
			return nil, err
		}
		var draft struct {
			Models map[string]json.RawMessage `json:"models"`
		}
		_ = json.Unmarshal(input.Config, &draft)
		for index := 1; ; index++ {
			suffix := "-copy"
			if index > 1 {
				suffix = fmt.Sprintf("-copy-%d", index)
			}
			name := input.Name + suffix
			_, saved := c.Models[name]
			_, pending := draft.Models[name]
			if !saved && !pending {
				copiedName = name
				break
			}
		}
		c.Models[copiedName] = one.Models[input.Name]
	default:
		c, err = configstore.MergeKeys(input.Config, c, true)
		if err != nil {
			return nil, err
		}
	}
	if err = s.Store.Save(c); err != nil {
		return nil, err
	}
	result := shared.Object{"config": configstore.Editable(c), "path": s.Store.Path, "modelContextDefaults": configstore.OfficialModelContexts(), "modelContextFallback": configstore.DefaultContextWindow}
	if copiedName != "" {
		result["name"] = copiedName
	}
	return result, nil
}

// selectedConfig 只取当前连接草稿，其他未完成的模型不影响测试、目录查询或复制。
func selectedConfig(data []byte, name string, concurrent int) ([]byte, error) {
	var input struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if json.Unmarshal(data, &input) != nil || input.Models[name] == nil {
		return nil, errors.New("该模型配置不存在，请刷新后重试。")
	}
	return shared.Marshal(shared.Object{"version": 1, "maxConcurrent": concurrent, "models": map[string]json.RawMessage{name: input.Models[name]}}), nil
}

type SetupServer struct {
	Server             *http.Server
	URL, Origin, Token string
	Listener           net.Listener
}

// StartSetup 仅监听回环，使用 Host、Origin、随机令牌及请求大小限制保护配置入口。
func StartSetup(service *ConfigService, assets fs.FS) (*SetupServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	bytes := make([]byte, 32)
	if _, err = rand.Read(bytes); err != nil {
		listener.Close()
		return nil, err
	}
	token := hex.EncodeToString(bytes)
	host := listener.Addr().String()
	origin := "http://" + host
	setup := &SetupServer{URL: origin + "/#" + token, Origin: origin, Token: token, Listener: listener}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Host != host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin) {
			writeJSON(w, 403, shared.Object{"error": "拒绝跨站请求。"})
			return
		}
		if r.Method == "GET" {
			name := strings.TrimPrefix(r.URL.Path, "/")
			if name == "" {
				name = "index.html"
			}
			if shared.Contains([]string{"index.html", "config-ui.js", "config.css", "desktop-ui.js", "desktop.css", "bridge.js", "shell-ui.js", "select-ui.js", "notification-ui.js", "model-picker-ui.js", "model-picker.css"}, name) {
				data, err := fs.ReadFile(assets, name)
				if err != nil {
					http.NotFound(w, r)
					return
				}
				switch {
				case strings.HasSuffix(name, ".html"):
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
				case strings.HasSuffix(name, ".css"):
					w.Header().Set("Content-Type", "text/css; charset=utf-8")
				default:
					w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
				}
				w.Write(data)
				return
			}
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			writeJSON(w, 401, shared.Object{"error": "请从配置启动器重新打开页面。"})
			return
		}
		var body []byte
		var err error
		if r.Method == "POST" {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeJSON(w, 415, shared.Object{"error": "需要 JSON 请求。"})
				return
			}
			body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 300000))
			if err != nil {
				writeJSON(w, 413, shared.Object{"error": "配置过大或无法读取。"})
				return
			}
		} else if r.Method != "GET" || r.URL.Path != "/api/config" {
			writeJSON(w, 404, shared.Object{"error": "未找到操作。"})
			return
		}
		value, err := service.Handle(r.Context(), r.URL.Path, body)
		if err != nil {
			writeJSON(w, 400, shared.Object{"error": err.Error()})
			return
		}
		writeJSON(w, 200, value)
	})
	setup.Server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second}
	go func() { _ = setup.Server.Serve(listener) }()
	return setup, nil
}

// writeJSON 统一返回 UTF-8 JSON，错误也可由现有页面直接显示。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
