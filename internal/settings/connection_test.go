package settings

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
)

// TestFreeTextConnectionLifecycle 验证自由名称的改名、保存、完整复制和删除，隐藏 Key 不因改名丢失。
func TestFreeTextConnectionLifecycle(t *testing.T) {
	for _, name := range []string{"日常助手", "My Gemini / Flash #1", "123", "__proto__", "constructor", "prototype", "toString", strings.Repeat("长连接名称", 32)} {
		t.Run(name, func(t *testing.T) {
			store, _ := testutil.Config(t, "compatible", "http://127.0.0.1:9/v1")
			service := NewConfigService(store)
			c, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			draft := configstore.Editable(c)
			draft.Models[name] = draft.Models["demo"]
			delete(draft.Models, "demo")
			result, err := service.Handle(context.Background(), "/api/config", shared.Marshal(shared.Object{"config": draft}))
			if err != nil {
				t.Fatal("free-text rename rejected", err)
			}
			if strings.Contains(string(shared.Marshal(result)), "synthetic-private-key") {
				t.Fatal("saved credential exposed")
			}
			result, err = service.Handle(context.Background(), "/api/config/copy", shared.Marshal(shared.Object{"name": name, "config": result["config"]}))
			if err != nil || result["name"] != name+"-copy" {
				t.Fatal("copy rejected or name truncated", err)
			}
			c, err = store.Read()
			if err != nil || len(c.Models) != 2 || c.Models[name].APIKey != "synthetic-private-key" || c.Models[name+"-copy"].APIKey != "synthetic-private-key" {
				t.Fatal("rename or copy lost saved connection", err)
			}
			if _, err = service.Handle(context.Background(), "/api/config/remove", shared.Marshal(shared.Object{"name": name + "-copy"})); err != nil {
				t.Fatal(err)
			}
			c, err = store.Read()
			if err != nil || len(c.Models) != 1 || c.Models[name].APIKey != "synthetic-private-key" {
				t.Fatal("deletion changed original connection", err)
			}
		})
	}
}

// TestConnectionChangesKeepCredentials 使用本地目录接口验证更换地址和协议后沿用 Key，环境变量仍优先且新 Key 可替换旧值。
func TestConnectionChangesKeepCredentials(t *testing.T) {
	for _, tc := range []struct{ name, protocol, env, replacement, wantKey string }{
		{"address", "compatible", "", "", "synthetic-private-key"},
		{"responses", "responses", "", "", "synthetic-private-key"},
		{"gemini", "gemini", "", "", "synthetic-private-key"},
		{"environment", "anthropic", "API_SUBAGENTS_TEST_CONNECTION_KEY", "", "synthetic-env-key"},
		{"replacement", "responses", "", "synthetic-new-key", "synthetic-new-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv(tc.env, "synthetic-env-key")
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				header, want := "Authorization", "Bearer "+tc.wantKey
				if tc.protocol == "anthropic" {
					header, want = "x-api-key", tc.wantKey
				} else if tc.protocol == "gemini" {
					header, want = "x-goog-api-key", tc.wantKey
				}
				if r.Method != "GET" || r.URL.Path != "/v1/models" || r.Header.Get(header) != want {
					t.Error("changed connection used wrong route or credential")
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.protocol == "gemini" {
					io.WriteString(w, `{"models":[{"name":"models/mock-model"}]}`)
				} else {
					io.WriteString(w, `{"data":[{"id":"mock-model"}]}`)
				}
			}))
			defer server.Close()
			store, _ := testutil.Config(t, "compatible", "http://127.0.0.1:9/old")
			c, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			p := c.Models["demo"]
			p.APIKeyEnv = tc.env
			c.Models["demo"] = p
			if err = store.Save(c); err != nil {
				t.Fatal(err)
			}
			draft := configstore.Editable(c)
			p = draft.Models["demo"]
			p.BaseURL, p.Protocol, p.APIKey = server.URL+"/v1", tc.protocol, tc.replacement
			draft.Models["demo"] = p
			service := NewConfigService(store)
			body := shared.Marshal(shared.Object{"name": "demo", "config": draft})
			if _, err = service.Handle(context.Background(), "/api/models", body); err != nil {
				t.Fatal("draft query required re-entering credentials", err)
			}
			result, err := service.Handle(context.Background(), "/api/config", body)
			if err != nil {
				t.Fatal("saving changed connection failed", err)
			}
			if _, err = service.Handle(context.Background(), "/api/models", shared.Marshal(shared.Object{"name": "demo", "config": result["config"]})); err != nil {
				t.Fatal("saved connection lost credentials", err)
			}
			c, err = store.Read()
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := configstore.ResolveProfile(c, "demo")
			if err != nil || resolved.APIKey != tc.wantKey || resolved.APIKeyEnv != tc.env || requests != 2 {
				t.Fatal("credential selection changed after saving", err)
			}
		})
	}
}
