package tasks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
)

// TestContinuationAndCompact 续接限定相同连接和工作区，长结论默认截短且完整记录仍可读取。
func TestContinuationAndCompact(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if requests > 0 && (!strings.Contains(string(body), "first task") || !strings.Contains(string(body), "next task")) {
			t.Error("continuation history lost")
		}
		requests++
		json.NewEncoder(w).Encode(modelReply("compatible", nil, strings.Repeat("结论", 1500)))
	}))
	defer server.Close()
	store, _ := testutil.Config(t, "compatible", server.URL)
	m := NewManager(store, t.TempDir(), nil)
	defer m.Close()
	root := t.TempDir()
	first, err := m.Submit(TaskRequest{Model: "demo", Task: "first task", Workspace: root})
	if err != nil {
		t.Fatal(err)
	}
	id := shared.Str(first["task_id"])
	value, err := m.Wait(context.Background(), id, 5000)
	if err != nil || value["status"] != "completed" {
		t.Fatal(value, err)
	}
	compact, err := m.Present(value, "")
	if err != nil || compact["resultTruncated"] != true || len([]rune(shared.Str(compact["result"]))) != 2000 || len([]rune(shared.Str(value["result"]))) != 3000 {
		t.Fatal("compact result or full record damaged")
	}
	if _, err := m.Submit(TaskRequest{Model: "demo", Task: "next task", Workspace: t.TempDir(), Continuation: id}); err == nil {
		t.Fatal("cross-workspace continuation accepted")
	}
	next, err := m.Submit(TaskRequest{Model: "demo", Task: "next task", Workspace: root, Continuation: id})
	if err != nil {
		t.Fatal(err)
	}
	value, err = m.Wait(context.Background(), shared.Str(next["task_id"]), 5000)
	if err != nil || value["status"] != "completed" || requests != 2 {
		t.Fatal(value, err, requests)
	}
}
