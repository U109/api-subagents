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
	"path/filepath"
	"strings"
	"sync"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/usage"
)

type ConfigService struct {
	Store                  *configstore.ConfigStore
	Provider               *providers.Provider
	Usage                  *usage.Store
	mu                     sync.Mutex
	BeforeRemoveConnection func(string) error
	BeforeRemoveAccess     func(string) error
}

// NewConfigService 让桌面绑定和浏览器入口共享同一套配置合并与校验逻辑。
func NewConfigService(store *configstore.ConfigStore) *ConfigService {
	if store == nil {
		store = configstore.NewConfigStore("")
	}
	ledger := usage.New(filepath.Join(filepath.Dir(store.Path), "usage"))
	provider := providers.NewProvider()
	provider.Client = usage.WrapClient(provider.Client, ledger)
	return &ConfigService{Store: store, Provider: provider, Usage: ledger}
}

// Handle 处理固定路由，网络测试不占写锁；定点保存、复制和删除只修改目标连接，其他草稿不会写入。
func (s *ConfigService) Handle(ctx context.Context, route string, body []byte) (shared.Object, error) {
	if route == "/api/usage" || route == "/api/usage/prices" {
		return s.Usage.Handle(route, body)
	}
	if len(body) > 300000 {
		return nil, errors.New("配置过大。")
	}
	if route == "/api/config" && len(body) == 0 {
		c, err := s.Store.Read()
		if err != nil {
			return nil, err
		}
		return shared.Object{"config": configstore.Editable(c), "path": s.Store.Path, "modelOutputDefaults": configstore.OfficialModelOutputs(), "lastAccessCurrent": c.LastAccessCurrent()}, nil
	}
	var input struct {
		Config json.RawMessage           `json:"config"`
		Name   string                    `json:"name"`
		ID     string                    `json:"id"`
		Plan   *configstore.AccessConfig `json:"plan"`
	}
	if json.Unmarshal(body, &input) != nil || strings.TrimSpace(string(body)) == "null" {
		return nil, errors.New("请求 JSON 无效，请检查配置格式。")
	}
	if route == "/api/probe" || route == "/api/models" {
		previous, err := s.Store.Read()
		if err != nil {
			return nil, err
		}
		single, err := selectedConfig(input.Config, input.Name)
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
		return s.Provider.Probe(usage.WithSource(ctx, "test", "", input.Name), p)
	}
	if route != "/api/config" && route != "/api/config/save" && route != "/api/config/copy" && route != "/api/config/remove" && route != "/api/access/save" && route != "/api/access/remove" {
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
	case "/api/access/save":
		if input.Plan == nil {
			return nil, errors.New("请提供接入配置。")
		}
		if err := c.ValidateAccess(*input.Plan); err != nil {
			return nil, err
		}
		index := -1
		for i, p := range c.AccessConfigs {
			if p.ID == input.Plan.ID {
				index = i
				break
			}
		}
		if index < 0 {
			c.AccessConfigs = append([]configstore.AccessConfig{*configstore.CloneAccess(input.Plan)}, c.AccessConfigs...)
		} else {
			c.AccessConfigs[index] = *configstore.CloneAccess(input.Plan)
		}
	case "/api/access/remove":
		index := -1
		for i, p := range c.AccessConfigs {
			if p.ID == input.ID {
				index = i
				break
			}
		}
		if index < 0 {
			return nil, errors.New("该接入配置已不存在。")
		}
		if s.BeforeRemoveAccess != nil {
			if err := s.BeforeRemoveAccess(input.ID); err != nil {
				return nil, err
			}
		}
		c.AccessConfigs = append(c.AccessConfigs[:index:index], c.AccessConfigs[index+1:]...)
		if c.LastAccess != nil && c.LastAccess.ID == input.ID {
			c.LastAccess = nil
			c.LastAccessFingerprint = ""
		}
	case "/api/config/save":
		single, selectErr := selectedConfig(input.Config, input.Name)
		if selectErr != nil {
			return nil, selectErr
		}
		one, mergeErr := configstore.MergeKeys(single, c, true)
		if mergeErr != nil {
			return nil, mergeErr
		}
		profile := one.Models[input.Name]
		if profile.SavedName == nil {
			if _, exists := c.Models[input.Name]; exists {
				return nil, errors.New("连接名称已被另一连接使用。")
			}
		} else if *profile.SavedName != input.Name {
			delete(c.Models, *profile.SavedName)
		}
		c.Models[input.Name] = profile
	case "/api/config/remove":
		profile, ok := c.Models[input.Name]
		if !ok {
			return nil, errors.New("该模型配置不存在，请刷新后重试。")
		}
		if s.BeforeRemoveConnection != nil {
			if err := s.BeforeRemoveConnection(profile.ID); err != nil {
				return nil, err
			}
		}
		c.RemoveAccessConnection(profile.ID)
		delete(c.Models, input.Name)
	case "/api/config/copy":
		single, err := selectedConfig(input.Config, input.Name)
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
		profile := one.Models[input.Name]
		profile.ID = newConnectionID()
		c.Models[copiedName] = profile
	default:
		previous := c
		c, err = configstore.MergeKeys(input.Config, c, true)
		if err != nil {
			return nil, err
		}
		c.AccessConfigs, c.LastAccess = previous.AccessConfigs, previous.LastAccess
		c.LastAccessFingerprint = previous.LastAccessFingerprint
		for _, profile := range previous.Models {
			if _, _, exists := c.FindConnection(profile.ID); !exists {
				if s.BeforeRemoveConnection != nil {
					if err := s.BeforeRemoveConnection(profile.ID); err != nil {
						return nil, err
					}
				}
				c.RemoveAccessConnection(profile.ID)
			}
		}
	}
	if err = s.Store.Save(c); err != nil {
		return nil, err
	}
	result := shared.Object{"config": configstore.Editable(c), "path": s.Store.Path, "modelOutputDefaults": configstore.OfficialModelOutputs(), "lastAccessCurrent": c.LastAccessCurrent()}
	if copiedName != "" {
		result["name"] = copiedName
	}
	return result, nil
}

// newConnectionID 为复制且名称恰好撞上既有身份的连接生成随机身份，不包含连接凭据。
func newConnectionID() string {
	data := make([]byte, 16)
	_, _ = rand.Read(data)
	return "connection-" + hex.EncodeToString(data)
}

// ActivateAccess 在配置写锁内持久化最后生效范围并调用受控接入；失败还原快照，普通保存与开启不会交错。
func (s *ConfigService) ActivateAccess(id string, apply func(configstore.Config, configstore.AccessConfig) error) error {
	if !s.mu.TryLock() {
		return errors.New("正在保存，请稍后重试。")
	}
	defer s.mu.Unlock()
	c, err := s.Store.Read()
	if err != nil {
		return err
	}
	var plan *configstore.AccessConfig
	if id == "" {
		plan = configstore.CloneAccess(c.LastAccess)
	} else {
		for _, p := range c.AccessConfigs {
			if p.ID == id {
				plan = configstore.CloneAccess(&p)
				break
			}
		}
	}
	if plan == nil {
		return errors.New("请先选择并启用一份已保存的接入配置。")
	}
	if err := c.ValidateAccess(*plan); err != nil {
		return err
	}
	previous := configstore.CloneAccess(c.LastAccess)
	previousFingerprint := c.LastAccessFingerprint
	fingerprint, err := c.AccessFingerprint(*plan)
	if err != nil {
		return err
	}
	if id == "" && previousFingerprint != "" && previousFingerprint != fingerprint {
		return errors.New("上次使用的连接设置已有变化，请明确重新启用接入配置。")
	}
	c.LastAccess = plan
	c.LastAccessFingerprint = fingerprint
	if err := s.Store.Save(c); err != nil {
		return err
	}
	if err := apply(c, *plan); err != nil {
		c.LastAccess = previous
		c.LastAccessFingerprint = previousFingerprint
		if rollback := s.Store.Save(c); rollback != nil {
			return errors.New("接入失败且快照未能还原，请刷新配置后重试。")
		}
		return err
	}
	return nil
}

// selectedConfig 只取当前连接草稿，其他未完成的模型不影响测试、目录查询、复制或定点保存。
func selectedConfig(data []byte, name string) ([]byte, error) {
	var input struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if json.Unmarshal(data, &input) != nil || input.Models[name] == nil {
		return nil, errors.New("该模型配置不存在，请刷新后重试。")
	}
	return shared.Marshal(shared.Object{"version": 1, "models": map[string]json.RawMessage{name: input.Models[name]}}), nil
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
			if shared.Contains([]string{"index.html", "workbench-app.js", "workbench-app.css", "bridge.js"}, name) {
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
