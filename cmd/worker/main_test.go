package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestWorkerStdio 用实际发布 EXE 验证 MCP 握手、工具描述与一次委派；配置和模型服务均为隔离模拟。
func TestWorkerStdio(t *testing.T) {
	bin := os.Getenv("WORKER_TEST_BIN")
	if bin == "" {
		t.Skip("set WORKER_TEST_BIN after building the plugin")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"short answer"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	store, _ := testutil.Config(t, "compatible", server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(), "API_SUBAGENTS_HOME="+filepath.Dir(store.Path))
	client := mcp.NewClient(&mcp.Implementation{Name: "worker-tests", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 7 {
		t.Fatal(tools, err)
	}
	if strings.Contains(string(shared.Marshal(tools)), "synthetic-private-key") {
		t.Fatal("secret in tool definitions")
	}
	foundImage := false
	for _, tool := range tools.Tools {
		if tool.Annotations == nil {
			t.Fatal("missing annotations", tool.Name)
		}
		if tool.Name == "generate_image" {
			foundImage = true
			if tool.Annotations.ReadOnlyHint {
				t.Fatal("image generation must be marked as a write operation")
			}
		}
	}
	if !foundImage {
		t.Fatal("generate_image is not registered")
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "delegate_task", Arguments: shared.Object{"model": "demo", "task": "bounded task", "workspace": t.TempDir()}})
	if err != nil || result.IsError || len(result.Content) != 1 {
		t.Fatal(result, err)
	}
	var value shared.Object
	if json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &value) != nil || value["status"] != "completed" || value["result"] != "short answer" || value["workspace"] != nil {
		t.Fatal(value)
	}
}
