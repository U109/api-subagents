package settings

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/U109/api-subagents/frontend"
	"github.com/U109/api-subagents/internal/testutil"
	"github.com/U109/api-subagents/internal/usage"
)

// TestUsageClearAuthenticatedRoute 验证浏览器清除必须携带令牌和确认；操作不修改连接、Key 或接入快照。
func TestUsageClearAuthenticatedRoute(t *testing.T) {
	store, _ := testutil.Config(t, "responses", "http://127.0.0.1:9/v1")
	before, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	service := NewConfigService(store)
	service.Usage.Save(usage.Record{ID: "synthetic-record", LogicalID: "synthetic-logical", StartedAt: time.Now().UnixMilli() - 100}, usage.Prices{})
	setup, err := StartSetup(service, frontend.Files)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Server.Close()
	for _, item := range []struct {
		token, body string
		status      int
	}{
		{"", `{"confirm":true}`, http.StatusUnauthorized},
		{setup.Token, `{}`, http.StatusBadRequest},
		{setup.Token, `{"confirm":true}`, http.StatusOK},
	} {
		request, err := http.NewRequest("POST", setup.Origin+"/api/usage/clear", strings.NewReader(item.body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", setup.Origin)
		request.Header.Set("Authorization", "Bearer "+item.token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != item.status {
			t.Fatalf("status=%d body=%s", response.StatusCode, data)
		}
	}
	rows, _, err := service.Usage.List(usage.Query{})
	if err != nil || len(rows) != 0 {
		t.Fatal("clear not routed", err)
	}
	after, err := os.ReadFile(store.Path)
	if err != nil || string(after) != string(before) {
		t.Fatal("clear changed configuration", err)
	}
}
