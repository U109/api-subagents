package usage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// TestObserverSnapshots 验证拆包、累计快照、真实零和缺失值，不把流事件重复计量。
func TestObserverSnapshots(t *testing.T) {
	for _, protocol := range []string{"responses", "compatible", "anthropic", "gemini"} {
		var data string
		switch protocol {
		case "responses":
			data = "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":100,\"output_tokens\":20,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens_details\":{\"reasoning_tokens\":5}}}}\r\n\r\n"
		case "compatible":
			data = "data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"prompt_tokens_details\":{\"cached_tokens\":0}}}\n\ndata: [DONE]\n\n"
		case "anthropic":
			data = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":90,\"cache_read_input_tokens\":10,\"cache_creation_input_tokens\":0,\"output_tokens\":0}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":20}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
		case "gemini":
			data = "data: {\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":15,\"thoughtsTokenCount\":5,\"totalTokenCount\":120},\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"
		}
		o := observer{protocol: protocol, streaming: true}
		for i := 0; i < len(data); i++ {
			o.feed([]byte(data[i : i+1]))
		}
		o.finish()
		if o.tokens.Total == nil || *o.tokens.Total != 120 || o.terminal != "success" {
			t.Fatalf("%s: %+v %s", protocol, o.tokens, o.terminal)
		}
		if protocol == "responses" && (o.tokens.Read == nil || *o.tokens.Read != 0 || o.tokens.Write != nil) {
			t.Fatal("zero and absent fields merged")
		}
	}
}

// TestMissingAndBounded 确认缺失、非法和超限统计均未知，绝不以补零或字符估算替代。
func TestMissingAndBounded(t *testing.T) {
	o := observer{protocol: "responses"}
	o.feed([]byte(`{"status":"completed","usage":{"input_tokens":-1,"output_tokens":1.2}}`))
	o.finish()
	if o.tokens.Input != nil || o.tokens.Output != nil || o.tokens.Total != nil {
		t.Fatal("invalid usage accepted")
	}
	o = observer{protocol: "responses"}
	o.feed([]byte(strings.Repeat("x", observationBytes+1)))
	o.finish()
	if !o.overflow || o.tokens.Input != nil {
		t.Fatal("observation budget not respected")
	}
}

// TestTransportLedger 确认 HTTP 原字节不改、关闭幂等、进程间读取、价格快照和秘密隔离。
func TestTransportLedger(t *testing.T) {
	store := New(t.TempDir())
	profile := configstore.Profile{ID: "connection-a", Model: "model-a", Protocol: "responses", APIKey: "synthetic-secret"}
	prices, err := store.SavePrices(Prices{Rates: map[string]Price{Key(profile.ID, profile.Model): {Input: 2, Output: 8, Read: 2, Write: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"status":"completed","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120},"output":[{"text":"synthetic-secret"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	defer server.Close()
	client := WrapClient(server.Client(), store)
	ctx := EnsureCall(WithSource(context.Background(), "proposal", "task-a", "worker"), profile, "worker", "proposal", "generate")
	request, _ := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader(`{}`))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	if string(data) != body {
		t.Fatal("response modified")
	}
	response.Body.Close()
	response.Body.Close()
	rows, _, err := New(store.Root).List(Query{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %#v", err, rows)
	}
	if rows[0].Source != "proposal" || rows[0].Outcome != "success" || rows[0].CostMicroUSD == nil || *rows[0].CostMicroUSD != 360 || rows[0].PriceVersion != prices.Revision {
		t.Fatalf("bad record: %+v", rows[0])
	}
	entries, _ := os.ReadDir(filepath.Join(store.Root, "records", time.Now().UTC().Format("2006-01-02")))
	raw, _ := os.ReadFile(filepath.Join(store.Root, "records", time.Now().UTC().Format("2006-01-02"), entries[0].Name()))
	if strings.Contains(string(raw), "synthetic-secret") || strings.Contains(string(raw), "output\"") && strings.Contains(string(raw), "text\"") {
		t.Fatal("response content persisted")
	}
	if _, err = store.SavePrices(prices); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SavePrices(prices); err == nil {
		t.Fatal("stale revision accepted")
	}
}

// TestAttemptsAndUnknown 确认同一逻辑请求的多次尝试分别结算，未知用量与 HTTP 失败仍可查询。
func TestAttemptsAndUnknown(t *testing.T) {
	store := New(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		io.WriteString(w, `{"error":"not recorded"}`)
	}))
	defer server.Close()
	ctx := EnsureCall(context.Background(), configstore.Profile{ID: "id", Model: "m", Protocol: "responses"}, "name", "main", "generate")
	client := WrapClient(server.Client(), store)
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequestWithContext(ctx, "POST", server.URL, nil)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}
	rows, _, err := store.List(Query{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("%v %d", err, len(rows))
	}
	if rows[0].LogicalID != rows[1].LogicalID || rows[0].Outcome != "failed" || rows[0].Input != nil {
		t.Fatal("attempt semantics incorrect")
	}
}

// TestQueryAndPriceValidation 验证超大范围、畸形计价标识和负费率在服务端被拒绝。
func TestQueryAndPriceValidation(t *testing.T) {
	store := New(t.TempDir())
	if _, _, err := store.List(Query{From: 1, To: time.Now().UnixMilli()}); err == nil {
		t.Fatal("unbounded query accepted")
	}
	if _, err := store.SavePrices(Prices{Rates: map[string]Price{"invalid": {Input: 1}}}); err == nil {
		t.Fatal("invalid identity accepted")
	}
	if _, err := store.SavePrices(Prices{Rates: map[string]Price{Key("id", "m"): {Input: -1}}}); err == nil {
		t.Fatal("negative price accepted")
	}
}

// TestConcurrentAndCancelled 验证独立尝试文件的并发写入和客户端取消，不用真实模型服务。
func TestConcurrentAndCancelled(t *testing.T) {
	store := New(t.TempDir())
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			store.Save(Record{ID: shared.UUID(), LogicalID: shared.UUID(), StartedAt: time.Now().UnixMilli(), Outcome: "unknown", Quality: "unreported"}, Prices{})
		}()
	}
	workers.Wait()
	rows, _, err := store.List(Query{})
	if err != nil || len(rows) != 32 {
		t.Fatalf("concurrent records: %v %d", err, len(rows))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	parent, cancel := context.WithCancel(context.Background())
	ctx := EnsureCall(parent, configstore.Profile{ID: "id", Model: "m", Protocol: "responses"}, "name", "main", "generate")
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL, nil)
	res, err := WrapClient(server.Client(), store).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	rows, _, err = store.List(Query{})
	if err != nil || len(rows) != 33 {
		t.Fatal("cancelled attempt missing")
	}
	found := false
	for _, row := range rows {
		if row.Outcome == "cancelled" {
			found = true
		}
	}
	if !found {
		t.Fatal("cancellation became success")
	}
}

// TestMissingPriceFields 确认直接调用本地 API 也不能把缺失价格字段悄悄变成免费。
func TestMissingPriceFields(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Handle("/api/usage/prices", []byte(`{"revision":"","rates":{"[\"id\",\"m\"]":{"input":0}}}`)); err == nil {
		t.Fatal("absent rates accepted as zero")
	}
}
