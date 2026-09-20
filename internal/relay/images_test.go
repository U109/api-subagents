package relay

import (
	"bytes"
	"context"
	"encoding/base64"
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

const testImage = "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEElEQVR4nGMwaDgARAwQCgAmDgXBriEUMQAAAABJRU5ErkJggg=="

// TestImageForwarding 验证四种连接不会丢失图片数据；Responses 还必须逐字保留原始图片项与质量字段。
func TestImageForwarding(t *testing.T) {
	for _, protocol := range []string{"responses", "compatible", "anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			input := shared.Object{"model": "api-subagents", "stream": true, "input": []any{shared.Object{"role": "user", "content": []any{shared.Object{"type": "input_text", "text": "Describe the image"}, shared.Object{"type": "input_image", "image_url": "data:image/png;base64," + testImage, "detail": "high"}}}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, _ := io.ReadAll(req.Body)
				if !bytes.Contains(body, []byte(testImage)) {
					t.Error("image data dropped during forwarding")
				}
				if protocol == "responses" {
					want := bytes.Replace(shared.Marshal(input), []byte(`"model":"api-subagents"`), []byte(`"model":"mock-model"`), 1)
					if !bytes.Equal(body, want) {
						t.Error("Responses image request modified beyond model routing")
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, upstreamReply(protocol, true))
			}))
			defer server.Close()
			r := testRelay(t, protocol, server.URL, true)
			if status, body := requestRelay(t, r, input); status != 200 || !strings.Contains(body, "response.completed") {
				t.Fatal("image request failed", status)
			}
		})
	}
}

// TestCodexImageInput 用隔离 Codex 验证图片能力能从模型目录进入选择器，并将本地图片编码发送到模拟上游。
func TestCodexImageInput(t *testing.T) {
	bin := codexBinary(t)
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		count.Add(1)
		body, _ := io.ReadAll(req.Body)
		var input shared.Object
		json.Unmarshal(body, &input)
		found := false
		for _, item := range shared.Arr(input["input"]) {
			for _, content := range shared.Arr(shared.Obj(item)["content"]) {
				part := shared.Obj(content)
				found = found || (part["type"] == "input_image" && strings.HasPrefix(shared.Str(part["image_url"]), "data:image/"))
			}
		}
		if !found {
			t.Error("Codex did not send the attached image")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, upstreamReply("responses", true))
	}))
	defer server.Close()
	r := testRelay(t, "responses", server.URL, true)
	config, _ := r.Store.Read()
	p := config.Models["demo"]
	p.ModelImageInputs = map[string]bool{p.Model: true}
	config.Models["demo"] = p
	if err := r.Store.Save(config); err != nil {
		t.Fatal(err)
	}
	if err := r.RefreshCatalog(); err != nil {
		t.Fatal(err)
	}
	alias := codexconfig.RelayModelAlias("demo", p.Model)
	root := t.TempDir()
	t.Run("model-list", func(t *testing.T) {
		client := startCodexRPC(t, bin, r.Codex.Home, root)
		result := client.call(t, "model/list", shared.Object{})
		for _, item := range shared.Arr(result["data"]) {
			model := shared.Obj(item)
			if model["model"] == alias {
				if !bytes.Contains(shared.Marshal(model["inputModalities"]), []byte(`"image"`)) {
					t.Fatal("model list still disables image uploads")
				}
				return
			}
		}
		t.Fatal("image model missing from model list")
	})
	pixel, _ := base64.StdEncoding.DecodeString(testImage)
	imagePath := filepath.Join(root, "sample.png")
	if err := os.WriteFile(imagePath, pixel, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := codexTestCommand(ctx, bin, "exec", "--ephemeral", "--skip-git-repo-check", "--json", "-s", "read-only", "-m", alias, "-i", imagePath, "-C", root, "Describe the attached image without tools.")
	cmd.Env = codexTestEnv(r.Codex.Home)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil || count.Load() != 1 {
		t.Fatalf("image input failed: %v, requests=%d\n%s", err, count.Load(), output)
	}
}
