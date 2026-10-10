package providers

import (
	"context"
	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDeclaredModelContext 仅接受明确整数容量，输出限制、缺失和非法数据不能冒充上下文。
func TestDeclaredModelContext(t *testing.T) {
	for _, tc := range []struct {
		entry shared.Object
		want  int
	}{
		{shared.Object{"context_window": float64(128000)}, 128000},
		{shared.Object{"context_length": float64(1000000)}, 1000000},
		{shared.Object{"inputTokenLimit": float64(64000)}, 64000},
		{shared.Object{"outputTokenLimit": float64(128000)}, 0},
		{shared.Object{"context_length": "128000"}, 0},
		{shared.Object{"context_length": float64(1.5)}, 0},
		{shared.Object{"context_length": float64(-1)}, 0},
		{shared.Object{}, 0},
	} {
		if got := declaredModelContext(tc.entry); got != tc.want {
			t.Fatalf("got %d want %d", got, tc.want)
		}
	}
}

// TestListModelsContextMetadata 使用本地模拟目录验证容量传给界面，未知模型不自动填成 256K。
func TestListModelsContextMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"small","context_window":128000},{"id":"large","context_length":1000000},{"id":"unknown","max_output_tokens":8192}]}`)
	}))
	defer server.Close()
	result, err := NewProvider().ListModels(context.Background(), configstore.Profile{Protocol: "responses", BaseURL: server.URL, APIKey: "synthetic-test"})
	if err != nil {
		t.Fatal(err)
	}
	items, ok := result["models"].([]any)
	if !ok {
		t.Fatalf("unexpected list type %T", result["models"])
	}
	if len(items) != 3 {
		t.Fatal("wrong count")
	}
	for _, raw := range items {
		item := shared.Obj(raw)
		switch item["id"] {
		case "small":
			if item["contextWindow"] != 128000 {
				t.Fatal("small metadata lost")
			}
		case "large":
			if item["contextWindow"] != 1000000 {
				t.Fatal("raw official capacity changed before UI")
			}
		case "unknown":
			if _, exists := item["contextWindow"]; exists {
				t.Fatal("invented capacity")
			}
		}
	}
}
