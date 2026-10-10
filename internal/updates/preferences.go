package updates

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/U109/api-subagents/internal/shared"
)

// Preferences 仅保存更新检查偏好，不包含模型配置、凭据或任意更新源。
type Preferences struct {
	AutoCheck      bool   `json:"autoCheck"`
	SkippedVersion string `json:"skippedVersion,omitempty"`
}

// readPreferences 读取固定缓存目录内的小文件；损坏或旧配置回退到打开弹窗时检查，不在启动时联网。
func readPreferences(cache string) Preferences {
	p := Preferences{AutoCheck: true}
	file, err := os.Open(filepath.Join(cache, "preferences.json"))
	if err != nil {
		return p
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &p) != nil || (p.SkippedVersion != "" && (!versionPattern.MatchString(p.SkippedVersion) || p.SkippedVersion[0] == 'v')) {
		return Preferences{AutoCheck: true}
	}
	return p
}

// boundedNotes 限制发布说明展示长度；前端只用纯文本展示，不解释远程 HTML、链接或图片。
func boundedNotes(text string) string {
	runes := []rune(text)
	if len(runes) > 16000 {
		return string(runes[:16000]) + "\n…"
	}
	return text
}

// SetAutoCheck 原子持久化弹窗自动检查偏好；保存失败保留内存原值，插件更新不使用此设置。
func (u *Updater) SetAutoCheck(enabled bool) error {
	u.mu.Lock()
	if u.plugin {
		u.mu.Unlock()
		return errors.New("插件更新不使用应用检查偏好。")
	}
	next := u.preferences
	next.AutoCheck = enabled
	err := shared.AtomicWrite(filepath.Join(u.Cache, "preferences.json"), shared.Marshal(next), 0600)
	if err == nil {
		u.preferences = next
	}
	u.mu.Unlock()
	u.notify()
	return err
}

// Skip 仅跳过已验证且尚未下载的当前版本，新版本仍会显示；检查或下载期间不改变状态。
func (u *Updater) Skip() error {
	u.mu.Lock()
	if u.plugin || u.cancel != nil || u.state.Phase != "available" {
		u.mu.Unlock()
		return errors.New("当前版本不可跳过。")
	}
	next := u.preferences
	next.SkippedVersion = u.state.AvailableVersion
	err := shared.AtomicWrite(filepath.Join(u.Cache, "preferences.json"), shared.Marshal(next), 0600)
	if err == nil {
		u.preferences = next
		u.state.Phase, u.state.Message = "skipped", "已跳过版本 "+next.SkippedVersion
	}
	u.mu.Unlock()
	u.notify()
	return err
}

// Cancel 中止当前检查或下载；必须等原请求退出后才能开始下一次，不能取消安装或让迟到结果可安装。
func (u *Updater) Cancel() bool {
	u.mu.Lock()
	if u.cancel == nil {
		u.mu.Unlock()
		return false
	}
	u.cancel()
	u.state.Phase, u.state.Message = "cancelling", "正在取消当前更新操作…"
	u.mu.Unlock()
	u.notify()
	return true
}
