package tasks

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// imageFixture 保存仅用于模拟生图的已选模型，默认文字模型不被工具误用。
func imageFixture(t *testing.T, endpoint string) *Manager {
	t.Helper()
	store, _ := testutil.Config(t, "responses", endpoint)
	config, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	profile := config.Models["demo"]
	profile.RelayModels = []string{"gpt-image-2.5-sunburst"}
	config.Models["demo"] = profile
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	m := NewManager(store, "", nil)
	t.Cleanup(m.Close)
	return m
}

// tinyPNG 生成有效的一像素图片，测试只走本地模拟接口而不触发模型计费。
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 240, G: 100, B: 30, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// TestGenerateImageMCP 验证连接凭据、图片专用端点、受限落盘和 MCP 图片预览的完整模拟链路。
func TestGenerateImageMCP(t *testing.T) {
	want := tinyPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-private-key" {
			t.Errorf("wrong image request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var body shared.Object
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "gpt-image-2.5-sunburst" || body["prompt"] != "画一只橘猫" || body["size"] != "1024x1024" || body["quality"] != "low" || body["n"] != float64(1) || body["stream"] != nil {
			t.Errorf("unexpected image payload: %v, %v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(shared.Object{"data": []any{shared.Object{"b64_json": base64.StdEncoding.EncodeToString(want)}}})
	}))
	defer server.Close()
	m := imageFixture(t, server.URL+"/v1")
	workspace := t.TempDir()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := NewMCPServer(m).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "image-test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "generate_image", Arguments: shared.Object{"model": "demo", "prompt": "画一只橘猫", "workspace": workspace}})
	if err != nil || result.IsError || len(result.Content) != 2 {
		t.Fatalf("image tool result: %v, %v", result, err)
	}
	var value shared.Object
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || json.Unmarshal([]byte(text.Text), &value) != nil {
		t.Fatal("missing structured image path")
	}
	path := shared.Str(value["path"])
	actualDir, actualErr := os.Stat(filepath.Dir(path))
	expectedDir, expectedErr := os.Stat(filepath.Join(workspace, imageDirectory))
	if !filepath.IsAbs(path) || actualErr != nil || expectedErr != nil || !os.SameFile(actualDir, expectedDir) || value["mimeType"] != "image/png" || strings.Contains(text.Text, "synthetic-private-key") {
		t.Fatalf("unsafe image result: path=%q workspace=%q actualDirErr=%v expectedDirErr=%v", path, workspace, actualErr, expectedErr)
	}
	file, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(file, want) {
		t.Fatal("saved image mismatch", err)
	}
	preview, ok := result.Content[1].(*mcp.ImageContent)
	if !ok || preview.MIMEType != "image/png" {
		t.Fatal("missing image preview", result.Content[1])
	}
	decoded, err := base64.StdEncoding.DecodeString(string(preview.Data))
	if err != nil || !bytes.Equal(decoded, want) {
		t.Fatal("MCP preview mismatch", err)
	}
}

// TestGenerateImageValidation 在发出任何计费请求前拒绝错误模型、路径和提示词。
func TestGenerateImageValidation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	m := imageFixture(t, server.URL+"/v1")
	workspace := t.TempDir()
	for _, tc := range []struct {
		name string
		req  ImageRequest
	}{
		{"text model", ImageRequest{Model: "demo", ModelID: "mock-model", Prompt: "cat", Workspace: workspace}},
		{"unknown model", ImageRequest{Model: "demo", ModelID: "gpt-image-unknown", Prompt: "cat", Workspace: workspace}},
		{"blank prompt", ImageRequest{Model: "demo", Prompt: " ", Workspace: workspace}},
		{"relative workspace", ImageRequest{Model: "demo", Prompt: "cat", Workspace: "relative"}},
		{"missing workspace", ImageRequest{Model: "demo", Prompt: "cat", Workspace: filepath.Join(workspace, "missing")}},
		{"invalid size", ImageRequest{Model: "demo", Prompt: "cat", Workspace: workspace, Size: "512x512"}},
		{"invalid quality", ImageRequest{Model: "demo", Prompt: "cat", Workspace: workspace, Quality: "ultra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := m.GenerateImage(context.Background(), tc.req); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatal("invalid input reached image API")
	}
}

// TestGenerateImageOptions 验证显式尺寸和 Sunburst 高画质被完整转发，其他型号不能误用专属画质。
func TestGenerateImageOptions(t *testing.T) {
	var requests atomic.Int32
	image := tinyPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body shared.Object
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.URL.Path != "/v1/images/generations" || body["size"] != "1536x1024" || body["quality"] != "xhigh" {
			t.Errorf("image options were not forwarded: %v, %v", body, err)
		}
		_ = json.NewEncoder(w).Encode(shared.Object{"data": []any{shared.Object{"b64_json": base64.StdEncoding.EncodeToString(image)}}})
	}))
	defer server.Close()
	m := imageFixture(t, server.URL+"/v1/responses")
	workspace := t.TempDir()
	if _, err := m.GenerateImage(context.Background(), ImageRequest{Model: "demo", Prompt: "cat", Size: "1536x1024", Quality: "xhigh", Workspace: workspace}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("expected one image request")
	}
	if _, _, err := imageOptions("gpt-image-1", "1024x1024", "max"); err == nil {
		t.Fatal("non-Sunburst model accepted max quality")
	}
}

// TestGenerateImageCancellation 验证取消会中断等待中的网络请求，并且不会留下图片文件。
func TestGenerateImageCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer server.Close()
	defer close(release)
	m := imageFixture(t, server.URL+"/v1")
	workspace := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := m.GenerateImage(ctx, ImageRequest{Model: "demo", Prompt: "cat", Workspace: workspace})
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("image request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation did not propagate: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled image request did not stop")
	}
	entries, err := os.ReadDir(filepath.Join(workspace, imageDirectory))
	if err != nil || len(entries) != 0 {
		t.Fatal("canceled request left image files", entries, err)
	}
}

// TestGenerateImageRedirect 验证生图请求不跟随上游跳转，避免把连接凭据发送到另一服务。
func TestGenerateImageRedirect(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	m := imageFixture(t, server.URL+"/v1")
	if _, err := m.GenerateImage(context.Background(), ImageRequest{Model: "demo", Prompt: "cat", Workspace: t.TempDir()}); err == nil {
		t.Fatal("image redirect accepted")
	}
	if forwarded.Load() != 0 {
		t.Fatal("credentials may have followed image redirect")
	}
}

// TestGenerateImageUpstreamFailure 不回显上游密钥或错误正文，且失败时不创建图片文件。
func TestGenerateImageUpstreamFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "synthetic-private-key upstream failed", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	m := imageFixture(t, server.URL+"/v1")
	workspace := t.TempDir()
	_, err := m.GenerateImage(context.Background(), ImageRequest{Model: "demo", Prompt: "cat", Workspace: workspace})
	if err == nil || !strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "synthetic-private-key") {
		t.Fatal("upstream error leaked or lost status", err)
	}
	entries, err := os.ReadDir(filepath.Join(workspace, imageDirectory))
	if err != nil || len(entries) != 0 {
		t.Fatal("failed request left image files", entries, err)
	}
}

// TestGenerateImageBadPayload 拒绝不是图片的 base64 正文，不把上游错误页写入工作区。
func TestGenerateImageBadPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"`+base64.StdEncoding.EncodeToString([]byte("<script>not an image</script>"))+`"}]}`)
	}))
	defer server.Close()
	m := imageFixture(t, server.URL+"/v1")
	workspace := t.TempDir()
	if _, err := m.GenerateImage(context.Background(), ImageRequest{Model: "demo", Prompt: "cat", Workspace: workspace}); err == nil {
		t.Fatal("non-image payload accepted")
	}
	entries, err := os.ReadDir(filepath.Join(workspace, imageDirectory))
	if err != nil || len(entries) != 0 {
		t.Fatal("non-image file was saved", entries, err)
	}
}

// TestGeneratedImageDirectorySymlink 拒绝工作区内指向其他目录的输出链接，避免覆盖项目文件。
func TestGeneratedImageDirectorySymlink(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, imageDirectory)); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := saveGeneratedImage(context.Background(), workspace, ".png", tinyPNG(t)); err == nil {
		t.Fatal("linked output directory accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("image escaped workspace", entries, err)
	}
}

// TestImagePreviewSymlink 验证 MCP 预览不会通过伪造的图片链接读取工作区外的文件。
func TestImagePreviewSymlink(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, imageDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.png")
	if err := os.WriteFile(secret, tinyPNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, imageDirectory, "image-test.png")
	if err := os.Symlink(secret, path); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if data, err := imagePreview(path); err == nil || data != nil {
		t.Fatal("linked image was exposed in MCP preview")
	}
}
