package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	codexconfig "github.com/U109/api-subagents/internal/codex"
	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
)

// TestWorkerGatewayProtocols 验证四类上游共用隔离入口、固定单模型与请求额度，不依赖模型品牌。
func TestWorkerGatewayProtocols(t *testing.T) {
	for _, protocol := range []string{"responses", "compatible", "anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls++
				if req.Header.Get("X-Api-Subagents-Token") != "" {
					t.Error("local token leaked")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, upstreamReply(protocol, false))
			}))
			defer upstream.Close()
			_, profile := testutil.Config(t, protocol, upstream.URL)
			profile.Stream = false
			profile.RelayModels = []string{"not-selected"}
			gateway, err := StartWorkerGateway(context.Background(), profile, 1, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer gateway.Close()
			for _, test := range []struct {
				model, token, origin string
				status               int
			}{
				{profile.Model, "", "", 403}, {profile.Model, gateway.Token, "https://untrusted.invalid", 403},
				{codexconfig.RelayModelAlias("worker", "not-selected"), gateway.Token, "", 400},
				{codexconfig.RelayModelAlias("worker", profile.Model), gateway.Token, "", 200},
				{profile.Model, gateway.Token, "", 429},
			} {
				request, _ := http.NewRequest("POST", gateway.Address+"/responses", strings.NewReader(string(shared.Marshal(shared.Object{"model": test.model, "input": "explicit task", "stream": false}))))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-Api-Subagents-Token", test.token)
				request.Header.Set("Origin", test.origin)
				res, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, res.Body)
				res.Body.Close()
				if res.StatusCode != test.status {
					t.Fatal("unexpected gateway response", test.model, res.StatusCode, test.status)
				}
			}
			count, exceeded := gateway.Usage()
			if count != 1 || !exceeded || calls != 1 {
				t.Fatal(count, exceeded, calls)
			}
		})
	}
}
