package settings

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
)

// TestConfigValidation 验证旧配置默认值、非法地址、参数类型和密钥隐藏边界。
func TestConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch shared.Object
	}{
		{"credential-url", shared.Object{"baseUrl": "https://user:secret@example.com"}}, {"query-url", shared.Object{"baseUrl": "https://example.com?key=secret"}}, {"file-url", shared.Object{"baseUrl": "file:///secret"}}, {"bad-protocol", shared.Object{"protocol": "constructor"}}, {"tokens-low", shared.Object{"maxTokens": 127}}, {"tokens-high", shared.Object{"maxTokens": 32001}}, {"bad-stream", shared.Object{"stream": "true"}}, {"short-first", shared.Object{"firstResponseTimeoutSeconds": 9}}, {"long-idle", shared.Object{"streamIdleTimeoutSeconds": 601}}, {"long-task", shared.Object{"taskTimeoutMinutes": 61}}, {"bad-key", shared.Object{"apiKey": 123}}, {"bad-env", shared.Object{"apiKeyEnv": "BAD-NAME"}}, {"empty-model", shared.Object{"model": " "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := shared.Object{"protocol": "compatible", "model": "mock-model"}
			for k, v := range tc.patch {
				p[k] = v
			}
			if _, err := configstore.ValidateConfig(shared.Marshal(shared.Object{"version": 1, "models": shared.Object{"demo": p}}), true); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	c, err := configstore.ValidateConfig([]byte(`{"version":1,"models":{"demo":{"protocol":"compatible","model":"m"}}}`), true)
	if err != nil {
		t.Fatal(err)
	}
	p := c.Models["demo"]
	if !p.Stream || p.FirstTimeout != 180 || p.IdleTimeout != 120 || p.TaskTimeout != 15 || p.MaxTokens != 4096 {
		t.Fatalf("defaults: %+v", p)
	}
	if _, err = configstore.ValidateConfig([]byte(`{"synthetic-private-key`), true); err == nil || strings.Contains(err.Error(), "synthetic") {
		t.Fatal("parse error leaked input")
	}
}

// TestSavedCredentials 确认改名保留 Key，而跨地址、重复来源和名称碰撞均不能绕过凭据隔离。
func TestSavedCredentials(t *testing.T) {
	store, _ := testutil.Config(t, "compatible", "http://127.0.0.1:9/v1")
	c, _ := store.Read()
	draft := configstore.Editable(c)
	p := draft.Models["demo"]
	delete(draft.Models, "demo")
	draft.Models["renamed"] = p
	merged, err := configstore.MergeKeys(shared.Marshal(draft), c, true)
	if err != nil || merged.Models["renamed"].APIKey != "synthetic-private-key" {
		t.Fatal(err)
	}
	if strings.Contains(string(shared.Marshal(configstore.Editable(merged))), "synthetic-private-key") {
		t.Fatal("key exposed")
	}
	p.BaseURL = "https://another.invalid/v1"
	draft.Models["renamed"] = p
	if _, err = configstore.MergeKeys(shared.Marshal(draft), c, true); err == nil {
		t.Fatal("endpoint key forwarding allowed")
	}
	p.BaseURL = c.Models["demo"].BaseURL
	draft.Models["renamed"] = p
	draft.Models["another"] = p
	if _, err = configstore.MergeKeys(shared.Marshal(draft), c, true); err == nil {
		t.Fatal("duplicate rename accepted")
	}
	p.SavedName = nil
	p.APIKeyEnv = "API_SUBAGENTS_TEST_MISSING_KEY"
	p.APIKey = "saved-fallback"
	c.Models["demo"] = p
	t.Setenv(p.APIKeyEnv, "")
	if _, err = configstore.ResolveProfile(c, "demo"); err == nil {
		t.Fatal("missing environment key fell back")
	}
}

// TestConfigurationActions 验证复制与删除只作用于目标草稿，且不把未完成的其他模型写入磁盘。
func TestConfigurationActions(t *testing.T) {
	store, _ := testutil.Config(t, "compatible", "http://127.0.0.1:9/v1")
	service := NewConfigService(store)
	c, _ := store.Read()
	draft := configstore.Editable(c)
	draft.Models["unfinished"] = configstore.Profile{}
	draft.Models["demo-copy"] = configstore.Profile{}
	value, err := service.Handle(context.Background(), "/api/config/copy", shared.Marshal(shared.Object{"name": "demo", "config": draft}))
	if err != nil {
		t.Fatal(err)
	}
	if value["name"] != "demo-copy-2" {
		t.Fatal(value["name"])
	}
	saved, _ := store.Read()
	if len(saved.Models) != 2 || saved.Models["demo-copy-2"].APIKey != "synthetic-private-key" {
		t.Fatal("copy saved wrong content")
	}
	if _, err = service.Handle(context.Background(), "/api/config/remove", shared.Marshal(shared.Object{"name": "demo"})); err != nil {
		t.Fatal(err)
	}
	saved, _ = store.Read()
	if len(saved.Models) != 1 {
		t.Fatal("remove affected another connection")
	}
	service.mu.Lock()
	_, err = service.Handle(context.Background(), "/api/config/remove", shared.Marshal(shared.Object{"name": "demo-copy-2"}))
	service.mu.Unlock()
	if err == nil {
		t.Fatal("saving lock bypassed")
	}
}

// TestSetupAuthentication 检查令牌、Origin、Host、超大请求和错误脱敏，浏览器入口不暴露配置 Key。
func TestSetupAuthentication(t *testing.T) {
	store, _ := testutil.Config(t, "compatible", "http://127.0.0.1:9/v1")
	setup, err := StartSetup(NewConfigService(store), os.DirFS(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Server.Close()
	for _, tc := range []struct {
		name, token, origin, host, body string
		status                          int
	}{
		{"no-token", "", "", "", "", 401}, {"cross-origin", setup.Token, "https://evil.invalid", "", "{}", 403}, {"wrong-host", setup.Token, "", "evil.invalid", "{}", 403}, {"malformed", setup.Token, "", "", "{synthetic-private-key", 400}, {"oversized", setup.Token, "", "", strings.Repeat("x", 300001), 413}, {"valid", setup.Token, "", "", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := "GET"
			if tc.body != "" {
				method = "POST"
			}
			req, _ := http.NewRequest(method, setup.Origin+"/api/config", strings.NewReader(tc.body))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			req.Header.Set("Content-Type", "application/json")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.host != "" {
				req.Host = tc.host
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			data, _ := io.ReadAll(res.Body)
			if res.StatusCode != tc.status {
				t.Fatalf("%d != %d: %s", res.StatusCode, tc.status, data)
			}
			if strings.Contains(string(data), "synthetic-private-key") {
				t.Fatal("key exposed")
			}
		})
	}
}

// TestModelCatalogPaging 验证四种目录接口的认证、去重、分页与 Gemini 非生成模型过滤。
func TestModelCatalogPaging(t *testing.T) {
	for _, protocol := range []string{"compatible", "responses", "anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" {
					t.Error("not GET")
				}
				switch protocol {
				case "anthropic":
					if r.Header.Get("x-api-key") != "synthetic-private-key" {
						t.Error("auth")
					}
					if calls == 1 {
						io.WriteString(w, `{"data":[{"id":"one","display_name":"One"}],"has_more":true,"last_id":"cursor"}`)
					} else {
						if r.URL.Query().Get("after_id") != "cursor" {
							t.Error("cursor")
						}
						io.WriteString(w, `{"data":[{"id":"one"},{"id":"two"}],"has_more":false}`)
					}
				case "gemini":
					if r.Header.Get("x-goog-api-key") != "synthetic-private-key" {
						t.Error("auth")
					}
					io.WriteString(w, `{"models":[{"name":"models/one","supportedGenerationMethods":["generateContent"]},{"name":"models/embed","supportedGenerationMethods":["embedContent"]}]}`)
				default:
					if r.Header.Get("Authorization") != "Bearer synthetic-private-key" {
						t.Error("auth")
					}
					io.WriteString(w, `{"data":[{"id":"one"},{"id":"one"},{"id":"two"}]}`)
				}
			}))
			defer server.Close()
			_, p := testutil.Config(t, protocol, server.URL+"/v1")
			result, err := providers.NewProvider().ListModels(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if protocol == "gemini" {
				want = 1
			}
			if len(shared.Arr(result["models"])) != want {
				t.Fatal(result)
			}
		})
	}
}

// TestProviderRedirectPrivacy 禁止认证请求跟随重定向，上游错误原文也不回显。
func TestProviderRedirectPrivacy(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	_, p := testutil.Config(t, "compatible", server.URL)
	_, err := providers.NewProvider().ListModels(context.Background(), p)
	if err == nil || targetCalls != 0 {
		t.Fatal("redirect followed")
	}
}

// TestPersistedConfigurationFlags 编辑标记不能进入配置文件，已有中文字段和默认值必须保留。
func TestPersistedConfigurationFlags(t *testing.T) {
	store, _ := testutil.Config(t, "compatible", "http://127.0.0.1:9")
	c, _ := store.Read()
	p := c.Models["demo"]
	p.Description = "中文用途"
	p.HasKey = true
	source := "demo"
	p.SavedName = &source
	c.Models["demo"] = p
	if err := store.Save(c); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(store.Path)
	var saved shared.Object
	json.Unmarshal(data, &saved)
	profile := shared.Obj(shared.Obj(saved["models"])["demo"])
	if profile["hasKey"] != nil || profile["savedName"] != nil || profile["description"] != "中文用途" {
		t.Fatal("edit metadata persisted")
	}
}
