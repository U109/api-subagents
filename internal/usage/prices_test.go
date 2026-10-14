package usage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	configstore "github.com/U109/api-subagents/internal/config"
)

// TestOfficialPricesAndOverrides 验证官网价格、独立副本、未知模型不猜价、用户覆盖和当前模型范围。
func TestOfficialPricesAndOverrides(t *testing.T) {
	p, known := OfficialPrice("gpt-5.4")
	if !known || p.Input != 2.5 || p.Output != 15 || p.Fast == nil || p.Fast.Input != 5 || p.Fast.Output != 30 {
		t.Fatalf("official preset: %+v", p)
	}
	p.Fast.Input = 99
	p, _ = OfficialPrice("gpt-5.4")
	if p.Fast.Input == 99 {
		t.Fatal("shared preset mutation")
	}
	if _, ok := OfficialPrice("gpt-5.4-fast-private"); ok {
		t.Fatal("guessed private alias price")
	}
	store := New(t.TempDir())
	c := configstore.EmptyConfig()
	c.Models["demo"] = configstore.Profile{ID: "stable", Model: "gpt-5.4", RelayModels: []string{"private"}, ModelCatalog: []configstore.CatalogModel{{ID: "gpt-4.1"}}}
	prices, err := store.EffectivePrices(c)
	if err != nil || len(prices.Rates) != 1 {
		t.Fatal(err, prices)
	}
	key := Key("stable", "gpt-5.4")
	if _, err := store.SavePrices(Prices{Rates: map[string]Price{key: {Input: 0, Output: 0, Read: 0, Write: 0}}}); err != nil {
		t.Fatal(err)
	}
	prices, err = store.EffectivePrices(c)
	if err != nil || prices.Rates[key].Input != 0 || prices.Rates[key].Fast != nil {
		t.Fatal("explicit zero override replaced", err, prices)
	}
}

// TestFastAndLongSettlement 验证 Fast 与长上下文边界，以及缺价、缺输入不能伪装成零或普通费用。
func TestFastAndLongSettlement(t *testing.T) {
	p, _ := OfficialPrice("gpt-6-astra")
	for _, tc := range []struct {
		input int64
		fast  bool
		want  float64
	}{{272000, false, 10}, {272001, false, 20}, {272000, true, 20}, {272001, true, 40}} {
		value, known := settlementPrice(p, tc.fast, Tokens{Input: &tc.input})
		if !known || value.Input != tc.want {
			t.Fatal(tc, value, known)
		}
	}
	if _, known := settlementPrice(p, true, Tokens{}); known {
		t.Fatal("unknown input priced at short-context rate")
	}
	if _, known := settlementPrice(Price{Input: 1}, true, Tokens{}); known {
		t.Fatal("missing Fast price treated as normal")
	}
	store := New(t.TempDir())
	for _, raw := range []string{`{"rates":{"[\"c\",\"m\"]":{"input":1,"output":2,"read":0,"write":0,"fast":{"input":3}}}}`, `{"rates":{"[\"c\",\"m\"]":{"input":1,"output":2,"read":0,"write":0,"fast":{"input":3,"output":4,"read":null,"write":0}}}}`} {
		if _, err := store.Handle("/api/usage/prices", []byte(raw)); err == nil {
			t.Fatal("incomplete Fast rate accepted", raw)
		}
	}
}

// TestFastTierDowngradeLedger 用本地接口验证 Fast 默认快照、实际降级、只旁观原字节和损坏价格不回退。
func TestFastTierDowngradeLedger(t *testing.T) {
	for _, tier := range []string{"priority", "default", "flex", "untrusted-secret"} {
		store := New(t.TempDir())
		profile := configstore.Profile{ID: "c", Model: "gpt-5.4", Protocol: "responses", ModelFastModes: map[string]bool{"gpt-5.4": true}}
		body := `{"service_tier":"` + tier + `","status":"completed","usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":0}}}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}))
		client := WrapClient(server.Client(), store)
		ctx := EnsureCall(context.Background(), profile, "demo", "main", "generate")
		request, _ := http.NewRequestWithContext(ctx, "POST", server.URL, nil)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		server.Close()
		rows, _, err := store.List(Query{})
		if err != nil || len(rows) != 1 || string(data) != body {
			t.Fatal(err, rows)
		}
		want := int64(1100)
		if tier == "flex" {
			if rows[0].CostMicroUSD != nil || rows[0].ServiceTier != "flex" {
				t.Fatal("unconfigured tier estimated as Standard or Fast", rows[0])
			}
			continue
		}
		if tier == "default" {
			want = 550
		}
		if rows[0].CostMicroUSD == nil || *rows[0].CostMicroUSD != want || rows[0].Fast != (tier != "default") {
			t.Fatalf("tier %s: %+v", tier, rows[0])
		}
		if tier == "untrusted-secret" && rows[0].ServiceTier != "" {
			t.Fatal("untrusted field persisted")
		}
	}
	store := New(t.TempDir())
	if err := os.WriteFile(filepath.Join(store.Root, "prices.json"), []byte(`{"rates":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EffectivePrices(configstore.EmptyConfig()); err == nil {
		t.Fatal("corrupt prices silently replaced")
	}
}

// TestTinyPricePrecision 防止合法极小费率在结算前被舍入成零，金额只在最终微美元边界截取。
func TestTinyPricePrecision(t *testing.T) {
	input, output := int64(10000000), int64(100000000)
	price := Price{Input: 0.000000125, Output: 0.00000013, Read: 0.000000125, Write: 0.000000125}
	value := cost(Tokens{Input: &input, Output: &output}, price, map[string]Price{"tiny": price}, "tiny")
	if value == nil || *value != 14 {
		t.Fatal("tiny nonzero rates lost precision", value)
	}
}

// TestCompactFastFallback 验证未上报服务档位时，生成沿用模型 Fast，而不发送 Fast 的压缩请求使用普通费率。
func TestCompactFastFallback(t *testing.T) {
	for _, operation := range []string{"generate", "compact"} {
		t.Run(operation, func(t *testing.T) {
			store := New(t.TempDir())
			profile := configstore.Profile{ID: "c", Model: "gpt-5.4", Protocol: "responses", ModelFastModes: map[string]bool{"gpt-5.4": true}}
			body := `{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":0}}}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			ctx := EnsureCall(context.Background(), profile, "demo", "main", operation)
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
			response, err := WrapClient(server.Client(), store).Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.ReadAll(response.Body)
			_ = response.Body.Close()
			rows, _, err := store.List(Query{})
			if err != nil || len(rows) != 1 {
				t.Fatal(err, rows)
			}
			want, fast := int64(550), false
			if operation == "generate" {
				want, fast = 1100, true
			}
			if rows[0].Fast != fast || rows[0].CostMicroUSD == nil || *rows[0].CostMicroUSD != want {
				t.Fatalf("%s used an incorrect Fast fallback: %+v", operation, rows[0])
			}
		})
	}
}

// TestFastStreamLedger 用分片本地 SSE 验证三个协议的实际 Fast／降级费率，旁观器不改响应字节或重复计量。
func TestFastStreamLedger(t *testing.T) {
	for _, protocol := range []string{"responses", "compatible", "anthropic"} {
		for _, fast := range []bool{false, true} {
			tier := "default"
			if fast {
				tier = "priority"
			}
			t.Run(protocol+"/"+tier, func(t *testing.T) {
				store := New(t.TempDir())
				model, standardCost := "gpt-5.4", int64(550)
				var frames []string
				switch protocol {
				case "responses":
					frames = []string{`{"type":"response.created","response":{"service_tier":"priority"}}`, `{"type":"response.completed","response":{"status":"completed","service_tier":"` + tier + `","usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":0}}}}`}
				case "compatible":
					frames = []string{`{"service_tier":"priority"}`, `{"service_tier":"` + tier + `","choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":0}}}`, "[DONE]"}
				case "anthropic":
					model, standardCost = "claude-opus-5-5", 800
					tier = "standard"
					if fast {
						tier = "fast"
					}
					frames = []string{`{"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"speed":"fast"}}}`, `{"type":"message_delta","usage":{"output_tokens":20,"speed":"` + tier + `"}}`, `{"type":"message_stop"}`}
				}
				body := "data: " + strings.Join(frames, "\n\ndata: ") + "\n\n"
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, body)
				}))
				defer server.Close()
				profile := configstore.Profile{ID: "c", Model: model, Protocol: protocol, ModelFastModes: map[string]bool{model: true}}
				ctx := EnsureCall(context.Background(), profile, "demo", "main", "generate")
				request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
				response, err := WrapClient(server.Client(), store).Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				buffer, data := make([]byte, 3), []byte{}
				for {
					n, err := response.Body.Read(buffer)
					data = append(data, buffer[:n]...)
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				_ = response.Body.Close()
				rows, _, err := store.List(Query{})
				if err != nil || len(rows) != 1 || string(data) != body {
					t.Fatal("stream bytes or ledger changed", err, rows)
				}
				want := standardCost
				if fast {
					want *= 2
				}
				if rows[0].Fast != fast || rows[0].ServiceTier != tier || rows[0].CostMicroUSD == nil || *rows[0].CostMicroUSD != want || rows[0].Outcome != "success" {
					t.Fatalf("incorrect stream settlement: %+v", rows[0])
				}
			})
		}
	}
}
