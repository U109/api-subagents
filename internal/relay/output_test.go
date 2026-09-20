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

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// TestResponsesOutputBudget 验证只补缺省额度，显式参数、压缩请求、数值精度及不完整返回均原样保留且不重试。
func TestResponsesOutputBudget(t *testing.T) {
	for _, tc := range []struct {
		name, extra, path, mode string
		inject                  bool
	}{
		{"default", "", "/responses", "", true},
		{"explicit", `, "max_output_tokens":8192`, "/responses", "", false},
		{"null", `, "max_output_tokens":null`, "/responses", "", false},
		{"zero", `, "max_output_tokens":0`, "/responses", "", false},
		{"invalid", `, "max_output_tokens":"invalid"`, "/responses", "", false},
		{"chatCap", `, "max_tokens":8192`, "/responses", "", false},
		{"completionCap", `, "max_completion_tokens":8192`, "/responses", "", false},
		{"compact", "", "/responses/compact", "", false},
		{"optOut", "", "/responses", "upstream", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := []byte(`{ "model":"api-subagents", "input":[{"role":"user","content":"keep <thinking>text"}], "tools":[{"type":"function","name":"write","parameters":{"type":"object"}}], "vendor":{"integer":9007199254740993,"decimal":1.234567890123456789}` + tc.extra + ` }`)
			response := "event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				count.Add(1)
				body, _ := io.ReadAll(req.Body)
				want := bytes.Replace(original, []byte(`"model":"api-subagents"`), []byte(`"model":"deepseek-v4-flash"`), 1)
				if tc.inject {
					want, _ = sjson.SetBytes(want, "max_output_tokens", 384000)
				}
				if !bytes.Equal(body, want) {
					t.Error("request content or budget changed unexpectedly")
				}
				if req.Header.Get("Authorization") != "Bearer synthetic-relay-key" {
					t.Error("wrong credential")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, response)
			}))
			defer server.Close()
			r := testRelay(t, "responses", server.URL, false)
			c, _ := r.Store.Read()
			p := c.Models["demo"]
			p.Model = "deepseek-v4-flash"
			p.ModelOutputs = map[string]configstore.ModelOutputSetting{p.Model: {Mode: tc.mode}}
			c.Models["demo"] = p
			if err := r.Store.Save(c); err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest("POST", r.Snapshot().Address+tc.path, bytes.NewReader(original))
			req.Header.Set("X-Api-Subagents-Token", r.token)
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != 200 || string(body) != response || count.Load() != 1 {
				t.Fatal("response altered or retried", res.StatusCode, string(body))
			}
		})
	}
}

// TestResponsesLongToolOutput 用本地受额度约束的模拟服务回归长工具参数，补预算后返回完整 JSON，不调用付费接口。
func TestResponsesLongToolOutput(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"content": strings.Repeat("long source text\n", 1500)})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		value := string(args)
		if gjson.GetBytes(body, "max_output_tokens").Int() < 32768 {
			value = value[:4096]
		}
		result, _ := json.Marshal(map[string]any{"output": []any{map[string]any{"type": "function_call", "name": "write_file", "arguments": value}}})
		w.Header().Set("Content-Type", "application/json")
		w.Write(result)
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, false)
	c, _ := r.Store.Read()
	p := c.Models["demo"]
	p.Model = "deepseek-v4-flash"
	c.Models["demo"] = p
	if err := r.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"upstream", "official"} {
		p.ModelOutputs = map[string]configstore.ModelOutputSetting{p.Model: {Mode: mode}}
		c.Models["demo"] = p
		if err := r.Store.Save(c); err != nil {
			t.Fatal(err)
		}
		status, body := requestRelay(t, r, map[string]any{"model": "api-subagents", "input": "synthetic"})
		result := gjson.Get(body, "output.0.arguments").String()
		if status != 200 || json.Valid([]byte(result)) != (mode == "official") {
			t.Fatal("unexpected tool output", mode, status)
		}
	}
}

// TestCodexModelOutputBudget 验证真实 Codex 缺省生成请求获得型号预算，使用隔离 CODEX_HOME 和本地合成流。
func TestCodexModelOutputBudget(t *testing.T) {
	bin := codexBinary(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(req.Body)
		if gjson.GetBytes(body, "max_output_tokens").Int() != 384000 || gjson.GetBytes(body, "model").String() != "deepseek-v4-flash" {
			t.Error("Codex budget or model missing")
		}
		if req.Header.Get("Authorization") != "Bearer synthetic-relay-key" {
			t.Error("credential mismatch")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamReply("responses", true))
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	c, _ := r.Store.Read()
	p := c.Models["demo"]
	p.Model = "deepseek-v4-flash"
	c.Models["demo"] = p
	if err := r.Store.Save(c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	root := t.TempDir()
	answer := filepath.Join(root, "answer.txt")
	cmd := codexTestCommand(ctx, bin, "exec", "--ephemeral", "--skip-git-repo-check", "--json", "-s", "read-only", "-m", "api-subagents/demo", "-C", root, "-o", answer, "Reply OK without using tools.")
	cmd.Env = codexTestEnv(r.Codex.Home)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Codex: %v\n%s", err, output)
	}
	data, _ := os.ReadFile(answer)
	if strings.TrimSpace(string(data)) != "OK" || calls.Load() != 1 {
		t.Fatal("isolated Codex response failed")
	}
}
