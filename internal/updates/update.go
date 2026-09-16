package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/U109/api-subagents/internal/platform"
	"github.com/U109/api-subagents/internal/shared"
)

type Release struct {
	Owner     string `json:"owner"`
	Repo      string `json:"repo"`
	Private   bool   `json:"private"`
	Version   string `json:"version"`
	GoVersion string `json:"goVersion"`
}

type UpdateManifest struct {
	Version string `json:"version"`
	File    string `json:"file"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

type UpdateState struct {
	Phase            string  `json:"phase"`
	Version          string  `json:"version"`
	AvailableVersion string  `json:"availableVersion"`
	Progress         float64 `json:"progress"`
	Message          string  `json:"message"`
}

type Updater struct {
	Release           Release
	Client            *http.Client
	Cache             string
	Packaged          bool
	OnChange          func()
	mu                sync.Mutex
	state             UpdateState
	manifest          UpdateManifest
	downloadURL, file string
}

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

var checksumPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// NewUpdater 更新源编译时确定，普通退出不下载或安装，不把作者令牌植入客户端。
func NewUpdater(release Release, cache string, packaged bool) *Updater {
	return &Updater{Release: release, Cache: cache, Packaged: packaged, state: UpdateState{Phase: "idle", Version: release.Version, Message: "检查是否有新版本"}, Client: &http.Client{Transport: platform.DefaultTransport(), CheckRedirect: releaseRedirect}}
}

// releaseRedirect 只允许 GitHub 及官方发布资源域名的 HTTPS 跳转。
func releaseRedirect(req *http.Request, via []*http.Request) error {
	host := req.URL.Hostname()
	if len(via) > 8 || req.URL.Scheme != "https" || (host != "github.com" && host != "api.github.com" && host != "release-assets.githubusercontent.com" && host != "objects.githubusercontent.com") {
		return errors.New("更新下载地址不可信。")
	}
	return nil
}

// ReleaseURL 返回固定下载页，界面无法指定外部链接或下载源。
func (u *Updater) ReleaseURL() string {
	return "https://github.com/" + u.Release.Owner + "/" + u.Release.Repo + "/releases"
}

// Snapshot 拷贝可展示状态，不暴露安装器路径或内部网络参数。
func (u *Updater) Snapshot() UpdateState { u.mu.Lock(); defer u.mu.Unlock(); return u.state }

// notify 在释放状态锁后发布进度，避免 UI 回调再次读取快照时死锁。
func (u *Updater) notify() {
	if u.OnChange != nil {
		u.OnChange()
	}
}

// change 将网络处理的结果写入状态，再通知前端。
func (u *Updater) change(phase, message string) {
	u.mu.Lock()
	u.state.Phase = phase
	u.state.Message = message
	u.mu.Unlock()
	u.notify()
}

// NewerVersion 仅比较稳定的三段版本，不自动降级或安装预发布版本。
func NewerVersion(next, current string) bool {
	n := versionPattern.FindStringSubmatch(next)
	c := versionPattern.FindStringSubmatch(current)
	if n == nil || c == nil {
		return false
	}
	for index := 1; index <= 3; index++ {
		a, errA := strconv.ParseUint(n[index], 10, 32)
		b, errB := strconv.ParseUint(c[index], 10, 32)
		if errA != nil || errB != nil {
			return false
		}
		if a != b {
			return a > b
		}
	}
	return false
}

// getJSON 限制发布元数据为 1 MB，网络错误不回显 URL 或响应原文。
func (u *Updater) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "API-Subagents/"+u.Release.Version)
	req.Header.Set("Accept", "application/json")
	res, err := u.Client.Do(req)
	if err != nil {
		return errors.New("无法连接更新服务。")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("更新服务 HTTP %d。", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 || json.Unmarshal(data, target) != nil {
		return errors.New("更新元数据无效。")
	}
	return nil
}

// Check 仅显式操作时查询最新稳定 Release；发现版本后核对固定名称的下载清单。
func (u *Updater) Check(ctx context.Context) error {
	u.mu.Lock()
	if shared.Contains([]string{"checking", "downloading", "downloaded", "installing"}, u.state.Phase) {
		u.mu.Unlock()
		return nil
	}
	if !shared.MarketPattern.MatchString(u.Release.Owner) || !regexp.MustCompile(`^[\w.-]+$`).MatchString(u.Release.Repo) {
		u.mu.Unlock()
		return errors.New("更新仓库配置无效。")
	}
	if u.Release.Private || !u.Packaged {
		u.state.Phase = "manual"
		u.state.Message = "请在版本下载页获取安装包。"
		u.mu.Unlock()
		u.notify()
		return nil
	}
	u.state.Phase = "checking"
	u.state.AvailableVersion = ""
	u.state.Message = "正在检查更新…"
	u.file = ""
	u.mu.Unlock()
	u.notify()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var release struct {
		Tag               string `json:"tag_name"`
		Draft, Prerelease bool
	}
	err := u.getJSON(ctx, "https://api.github.com/repos/"+u.Release.Owner+"/"+u.Release.Repo+"/releases/latest", &release)
	if err != nil {
		u.change("error", "暂时无法检查更新，请检查网络后重试。")
		return err
	}
	if release.Draft || release.Prerelease || !versionPattern.MatchString(release.Tag) {
		u.change("error", "发布版本信息无效。")
		return errors.New("发布版本信息无效。")
	}
	if !NewerVersion(release.Tag, u.Release.Version) {
		u.change("latest", "已经是最新版本")
		return nil
	}
	version := strings.TrimPrefix(release.Tag, "v")
	var manifest UpdateManifest
	root := u.ReleaseURL() + "/download/" + release.Tag + "/"
	err = u.getJSON(ctx, root+"update.json", &manifest)
	if err != nil {
		u.change("error", "新版本缺少完整更新文件，请稍后重试。")
		return err
	}
	expected := "API-Subagents-Setup-" + version + "-x64.exe"
	if manifest.Version != version || manifest.File != expected || !checksumPattern.MatchString(manifest.SHA256) || manifest.Size < 1024 || manifest.Size > 300*1024*1024 {
		u.change("error", "更新清单校验失败。")
		return errors.New("更新清单校验失败。")
	}
	u.mu.Lock()
	u.manifest = manifest
	u.downloadURL = root + url.PathEscape(manifest.File)
	u.state.Phase = "available"
	u.state.AvailableVersion = version
	u.state.Message = "发现新版本 " + version
	u.state.Progress = 0
	u.mu.Unlock()
	u.notify()
	return nil
}

// Download 流式写入临时文件并报告进度，大小和 SHA-256 均匹配后才允许安装。
func (u *Updater) Download(ctx context.Context) error {
	u.mu.Lock()
	if u.state.Phase != "available" {
		u.mu.Unlock()
		return errors.New("请先检查更新并确认有新版本。")
	}
	manifest, endpoint := u.manifest, u.downloadURL
	u.state.Phase = "downloading"
	u.state.Progress = 0
	u.state.Message = "正在下载更新…"
	u.mu.Unlock()
	u.notify()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	err := u.downloadFile(ctx, manifest, endpoint)
	if err != nil {
		u.change("error", "更新下载或校验失败，请重新检查后重试。")
		return err
	}
	u.change("downloaded", "更新已下载，重启后安装")
	return nil
}

// downloadFile 将校验完整的安装包原子移入更新缓存，失败时仅删除本次临时文件。
func (u *Updater) downloadFile(ctx context.Context, manifest UpdateManifest, endpoint string) error {
	if err := os.MkdirAll(u.Cache, 0700); err != nil {
		return err
	}
	temp := filepath.Join(u.Cache, shared.UUID()+".tmp")
	defer os.Remove(temp)
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	req.Header.Set("User-Agent", "API-Subagents/"+u.Release.Version)
	res, err := u.Client.Do(req)
	if err != nil {
		return errors.New("更新下载连接失败。")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("更新服务没有返回安装包。")
	}
	hash := sha256.New()
	reader := io.LimitReader(res.Body, manifest.Size+1)
	buffer := make([]byte, 65536)
	var size int64
	last := time.Time{}
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			size += int64(n)
			if size > manifest.Size {
				return errors.New("更新文件大小超出清单。")
			}
			if _, err = file.Write(buffer[:n]); err != nil {
				return err
			}
			hash.Write(buffer[:n])
			if time.Since(last) > 100*time.Millisecond {
				u.mu.Lock()
				u.state.Progress = float64(size) * 100 / float64(manifest.Size)
				u.mu.Unlock()
				u.notify()
				last = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return errors.New("更新下载中断。")
		}
	}
	if size != manifest.Size || hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 {
		return errors.New("更新安装包校验失败。")
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	target := filepath.Join(u.Cache, manifest.File)
	if err = os.Rename(temp, target); err != nil {
		return err
	}
	u.mu.Lock()
	u.file = target
	u.state.Progress = 100
	u.mu.Unlock()
	return nil
}

// Installer 重新校验缓存文件，防止下载后被替换；只返回已完整校验的安装器。
func (u *Updater) Installer() (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.state.Phase != "downloaded" {
		return "", errors.New("更新尚未下载完成。")
	}
	file, err := os.Open(u.file)
	if err != nil {
		return "", errors.New("更新缓存不可用，请重新下载。")
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, u.manifest.Size+1))
	if err != nil || size != u.manifest.Size || hex.EncodeToString(hash.Sum(nil)) != u.manifest.SHA256 {
		u.state.Phase = "error"
		u.state.Message = "更新缓存校验失败，请重新下载。"
		return "", errors.New("更新缓存校验失败。")
	}
	return u.file, nil
}

// Installing 在操作系统成功启动安装器后切换状态，普通退出不会触发此方法。
func (u *Updater) Installing() { u.change("installing", "正在关闭应用并安装更新…") }
