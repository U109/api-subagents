package tasks

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/providers"
	"github.com/U109/api-subagents/internal/shared"
)

const (
	imageDirectory        = "api-subagents-images"
	maxImageResponseBytes = 32 << 20
	maxImageFileBytes     = 16 << 20
	maxImagePreviewBytes  = 8 << 20
)

// ImageRequest 描述一次独立生图请求；输出目录固定由 workspace 派生，不接受任意文件名。
type ImageRequest struct {
	Model     string `json:"model"`
	ModelID   string `json:"model_id"`
	Prompt    string `json:"prompt"`
	Size      string `json:"size"`
	Quality   string `json:"quality"`
	Workspace string `json:"workspace"`
}

// imageModel 仅从连接已选的 GPT 生图型号中选取；多型号时要求明确指定，避免误用文字默认模型。
func imageModel(models []string, requested string) (string, error) {
	if requested != "" {
		if !strings.HasPrefix(requested, "gpt-image-") || !shared.Contains(models, requested) {
			return "", errors.New("model_id 必须是该连接已选的 gpt-image-* 生图模型。")
		}
		return requested, nil
	}
	selected := ""
	for _, model := range models {
		if !strings.HasPrefix(model, "gpt-image-") {
			continue
		}
		if selected != "" {
			return "", errors.New("该连接有多个生图模型，请指定 model_id。")
		}
		selected = model
	}
	if selected == "" {
		return "", errors.New("该连接未选择 gpt-image-* 生图模型。")
	}
	return selected, nil
}

// imageOptions 为省略的选项设置低成本默认值，并在发出请求前校验尺寸及型号专属画质。
func imageOptions(model, size, quality string) (string, string, error) {
	if size == "" {
		size = "1024x1024"
	}
	switch size {
	case "1024x1024", "1536x1024", "1024x1536":
	default:
		return "", "", errors.New("size 只支持 1024x1024、1536x1024 或 1024x1536。")
	}
	if quality == "" {
		quality = "low"
	}
	switch quality {
	case "low", "medium", "high", "auto":
	case "xhigh", "max":
		if model != "gpt-image-2.5-sunburst" {
			return "", "", errors.New("xhigh/max 画质只支持 gpt-image-2.5-sunburst。")
		}
	default:
		return "", "", errors.New("quality 参数无效。")
	}
	return size, quality, nil
}

// imageFormat 只接受图片接口返回的 PNG、JPEG 或 WebP，防止把错误页或脚本写成图片。
func imageFormat(data []byte) (mime, extension string, err error) {
	switch http.DetectContentType(data) {
	case "image/png":
		return "image/png", ".png", nil
	case "image/jpeg":
		return "image/jpeg", ".jpg", nil
	case "image/webp":
		return "image/webp", ".webp", nil
	default:
		return "", "", errors.New("上游没有返回受支持的图片格式。")
	}
}

// ensureImageDirectory 预先创建并核验固定输出目录，避免成功计费后才因目录不可写而丢失图片。
func ensureImageDirectory(root *os.Root) error {
	if err := root.MkdirAll(imageDirectory, 0700); err != nil {
		return errors.New("无法创建生图目录。")
	}
	info, err := root.Lstat(imageDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("生图目录必须是项目内的普通目录。")
	}
	return nil
}

// saveGeneratedImage 使用受限根句柄写入固定目录；拒绝目录链接，取消时删除不完整文件。
func saveGeneratedImage(ctx context.Context, workspace, extension string, data []byte) (string, error) {
	if !filepath.IsAbs(workspace) {
		return "", errors.New("workspace 必须是绝对目录。")
	}
	actual, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", errors.New("无法打开项目目录。")
	}
	root, err := os.OpenRoot(actual)
	if err != nil {
		return "", errors.New("workspace 必须是可访问的目录。")
	}
	defer root.Close()
	if err := ensureImageDirectory(root); err != nil {
		return "", err
	}
	relative := filepath.Join(imageDirectory, "image-"+shared.UUID()+extension)
	file, err := root.OpenFile(relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("无法创建图片文件。")
	}
	committed := false
	defer func() {
		if !committed {
			_ = root.Remove(relative)
		}
	}()
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return "", errors.New("无法完整保存图片。")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	committed = true
	return filepath.Join(actual, relative), nil
}

// imagePreview 仅从固定输出目录读取普通文件用于 MCP 预览，大小超限或链接逃逸时省略预览。
func imagePreview(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(filepath.Dir(path)))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if info, err := root.Lstat(imageDirectory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("生图目录不是普通目录。")
	}
	relative := filepath.Join(imageDirectory, filepath.Base(path))
	if info, err := root.Lstat(relative); err != nil || !info.Mode().IsRegular() || info.Size() > maxImagePreviewBytes {
		return nil, errors.New("图片不可预览。")
	}
	file, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("图片不可预览。")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxImagePreviewBytes+1))
	if err != nil || len(data) > maxImagePreviewBytes {
		return nil, errors.New("图片预览超过大小上限。")
	}
	return data, nil
}

// GenerateImage 通过已保存的兼容连接请求单张图片，限制响应大小、总时限及落盘路径；失败不重试也不暴露凭据。
func (m *Manager) GenerateImage(parent context.Context, input ImageRequest) (shared.Object, error) {
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" || len([]rune(prompt)) > 4000 {
		return nil, errors.New("prompt 需要 1–4000 个字符。")
	}
	if !filepath.IsAbs(input.Workspace) {
		return nil, errors.New("workspace 必须是绝对目录。")
	}
	config, err := m.Store.Read()
	if err != nil {
		return nil, err
	}
	profile, err := configstore.ResolveProfile(config, input.Model)
	if err != nil {
		return nil, err
	}
	if profile.Protocol != "responses" && profile.Protocol != "compatible" {
		return nil, errors.New("生图需要支持 OpenAI Images API 的 Responses 或兼容连接。")
	}
	if profile.APIKey == "" {
		return nil, errors.New("该连接尚未配置 API Key。")
	}
	model, err := imageModel(profile.OrderedModels(), input.ModelID)
	if err != nil {
		return nil, err
	}
	size, quality, err := imageOptions(model, input.Size, input.Quality)
	if err != nil {
		return nil, err
	}
	// 在计费请求之前确认工作区可访问，避免生成成功后才发现目标目录无效。
	actual, err := filepath.EvalSymlinks(input.Workspace)
	if err != nil {
		return nil, errors.New("无法打开项目目录。")
	}
	root, err := os.OpenRoot(actual)
	if err != nil {
		return nil, errors.New("workspace 必须是可访问的目录。")
	}
	if err := ensureImageDirectory(root); err != nil {
		_ = root.Close()
		return nil, err
	}
	_ = root.Close()
	limit := time.Duration(profile.TaskTimeout) * time.Minute
	if limit <= 0 || limit > 9*time.Minute {
		limit = 9 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()
	body, _ := json.Marshal(struct {
		Model   string `json:"model"`
		Prompt  string `json:"prompt"`
		Size    string `json:"size"`
		Quality string `json:"quality"`
		N       int    `json:"n"`
	}{Model: model, Prompt: prompt, Size: size, Quality: quality, N: 1})
	base := strings.TrimSuffix(profile.BaseURL, "/responses")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, providers.Endpoint(base, "images/generations"), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("生图 API 地址无效。")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+profile.APIKey)
	response, err := m.Provider.Client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("无法连接图像服务。")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("图像服务返回 HTTP %d；请检查模型、接口及上游权限。", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxImageResponseBytes+1))
	if err != nil || len(data) > maxImageResponseBytes {
		return nil, errors.New("图像响应中断或超过大小上限。")
	}
	var payload struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &payload) != nil || len(payload.Data) != 1 || payload.Data[0].B64JSON == "" {
		return nil, errors.New("图像服务没有返回单张 base64 图片。")
	}
	if base64.StdEncoding.DecodedLen(len(payload.Data[0].B64JSON)) > maxImageFileBytes+2 {
		return nil, errors.New("图片超过文件大小上限。")
	}
	image, err := base64.StdEncoding.DecodeString(payload.Data[0].B64JSON)
	if err != nil || len(image) == 0 || len(image) > maxImageFileBytes {
		return nil, errors.New("图像数据无效或超过文件大小上限。")
	}
	mime, extension, err := imageFormat(image)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	path, err := saveGeneratedImage(ctx, actual, extension, image)
	if err != nil {
		return nil, err
	}
	return shared.Object{"path": path, "mimeType": mime, "model": model, "bytes": len(image), "note": "图片已保存到工作区；可以用返回的绝对路径向用户展示。"}, nil
}
