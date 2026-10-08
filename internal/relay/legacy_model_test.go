package relay

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// TestLegacyModelFollowsCurrentConnection 验证旧模型名只使用当前选中连接的模型与 Key，切换后不再访问原连接。
func TestLegacyModelFollowsCurrentConnection(t *testing.T) {
	type request struct{ model, key string }
	requests := make(chan request, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body shared.Object
		json.NewDecoder(req.Body).Decode(&body)
		requests <- request{shared.Str(body["model"]), req.Header.Get("Authorization")}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamReply("responses", true))
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	config, err := r.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	profile := config.Models["demo"]
	profile.Model, profile.APIKey = "second-model", "synthetic-second-key"
	config.Models["second"] = profile
	if err := r.Store.Save(config); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{"demo", "second"} {
		if err := r.Enable(selected); err != nil {
			t.Fatal(err)
		}
		for _, legacy := range []string{"unconfigured-old-model", "mock-model"} {
			status, body := requestRelay(t, r, shared.Object{"model": legacy, "input": "Reply OK", "stream": true})
			if status != 200 {
				t.Fatal("legacy request failed", status, body)
			}
			got := <-requests
			want := config.Models[selected]
			if got.model != want.Model || got.key != "Bearer "+want.APIKey || r.Snapshot().ActiveModel != selected {
				t.Fatal("legacy request used previous connection", selected)
			}
		}
	}
}

// TestBuiltinRelayGuards 验证内置身份入口仍需要随机令牌、固定 Host 和无跨站 Origin，不转发旧登录凭据。
func TestBuiltinRelayGuards(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Header.Get("Authorization") != "Bearer synthetic-relay-key" || strings.Contains(req.URL.Path, "builtin") || req.Header.Get("X-Api-Subagents-Token") != "" {
			t.Error("built-in credentials or local token crossed the upstream boundary")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamReply("responses", true))
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	for _, tc := range []struct{ token, origin, host, upgrade string }{
		{"wrong", "", "", ""}, {r.token, "https://evil.invalid", "", ""}, {r.token, "", "evil.invalid", ""}, {r.token, "", "", "websocket"}, {r.token, "", "", ""},
	} {
		req, _ := http.NewRequest("POST", r.Snapshot().Address+"/builtin/"+tc.token+"/responses", strings.NewReader(`{"model":"old-model","input":"Reply OK","stream":true}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer synthetic-original-login")
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Upgrade", tc.upgrade)
		if tc.host != "" {
			req.Host = tc.host
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		want := http.StatusForbidden
		if tc.token == r.token && tc.origin == "" && tc.host == "" {
			want = http.StatusOK
			if tc.upgrade != "" {
				want = http.StatusUpgradeRequired
			}
		}
		if res.StatusCode != want {
			t.Fatal("incorrect built-in authentication result", res.StatusCode, want)
		}
	}
	if calls != 1 {
		t.Fatal("invalid built-in requests reached upstream", calls)
	}
}
