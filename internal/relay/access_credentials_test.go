package relay

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// TestAccessEndpointCredentialBoundary 验证保存新供应商地址后不会把新 Key 发往旧运行快照；所有地址均为本地模拟接口。
func TestAccessEndpointCredentialBoundary(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	c, _ := r.Store.Read()
	p := c.Models["demo"]
	plan := configstore.AccessConfig{ID: "a", Name: "配置 A", ConnectionIDs: []string{p.ID}, ModelIDs: map[string][]string{p.ID: {p.Model}}, DefaultKey: `["demo","mock-model"]`}
	if err := r.EnableAccess(c, plan); err != nil {
		t.Fatal(err)
	}
	p.BaseURL = server.URL + "/new-provider"
	p.APIKey = "synthetic-new-provider-key"
	c.Models["demo"] = p
	if err := r.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	status, _ := requestRelay(t, r, shared.Object{"model": codexconfig.RelayModelAlias("demo", p.Model), "input": "test", "stream": true})
	if status == http.StatusOK || calls.Load() != 0 {
		t.Fatal("new provider credentials sent to old endpoint")
	}
}
