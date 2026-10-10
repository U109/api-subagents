package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	"github.com/U109/api-subagents/internal/shared"
)

// TestCodexResponsesPreludeRecovery 使用独立 CODEX_HOME 和本机模拟接口，验证真实 Codex 的工具声明可进入安全前导恢复，最终只收到成功回答而非失败思考片段。
func TestCodexResponsesPreludeRecovery(t *testing.T) {
	bin := codexBinary(t)
	for _, tc := range []struct {
		name, tail string
	}{
		{"silentEOF", ""},
		{"failedEvent", syntheticResponsesPreludeFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			bodies := make(chan []byte, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Error(err)
					return
				}
				bodies <- body
				if !responsesPreludeEligible(body) {
					var payload shared.Object
					json.Unmarshal(body, &payload)
					var types []string
					for _, raw := range shared.Arr(payload["tools"]) {
						types = append(types, shared.Str(shared.Obj(raw)["type"]))
					}
					t.Errorf("native Codex request not eligible for prelude recovery; tool types=%v", types)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if count.Add(1) == 1 {
					io.WriteString(w, syntheticResponsesPrelude+tc.tail)
					return
				}
				io.WriteString(w, upstreamReply("responses", true))
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, true)
			r.responsesRetryWait = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			root := t.TempDir()
			answer := filepath.Join(root, "answer.txt")
			cmd := codexTestCommand(ctx, bin, "exec", "--ephemeral", "--skip-git-repo-check", "--json", "-s", "read-only", "-m", codexconfig.RelayModelAlias("demo", "mock-model"), "-C", root, "-o", answer, "Reply OK without using tools.")
			cmd.Env = codexTestEnv(r.Codex.Home)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Codex did not recover prelude: %v\n%s", err, output)
			}
			data, err := os.ReadFile(answer)
			if err != nil || strings.TrimSpace(string(data)) != "OK" || count.Load() != 2 || len(bodies) != 2 {
				t.Fatalf("answer=%q requests=%d err=%v\n%s", data, count.Load(), err, output)
			}
			if first, last := <-bodies, <-bodies; !bytes.Equal(first, last) {
				t.Fatal("native Codex recovery changed the request")
			}
			if bytes.Contains(output, []byte("failed-prelude")) || bytes.Contains(output, []byte("synthetic reasoning not yet committed")) {
				t.Fatal("failed prelude leaked to Codex")
			}
		})
	}
}
