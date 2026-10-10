//go:build windows

package desktop

import (
	"path/filepath"
	"testing"
)

// TestSmokeWebviewIsIsolated 验证启动测试不会复用正式浏览器目录，两个测试报告也不会共享同一 WebView2 状态。
func TestSmokeWebviewIsIsolated(t *testing.T) {
	production := desktopWindowsOptions("")
	if production.WebviewUserDataPath != "" || production.WebviewIsTransparent || production.WindowIsTranslucent {
		t.Fatal("smoke isolation changed the production window options")
	}
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	first := desktopWindowsOptions(filepath.Join(firstRoot, "report.json"))
	second := desktopWindowsOptions(filepath.Join(secondRoot, "report.json"))
	if first.WebviewUserDataPath != filepath.Join(firstRoot, "webview") || second.WebviewUserDataPath != filepath.Join(secondRoot, "webview") || first.WebviewUserDataPath == second.WebviewUserDataPath {
		t.Fatal("smoke windows share browser data")
	}
}

// TestRelaySelectionWithOtherDrafts 接入只读取已保存连接；其他草稿不挡切换，未知草稿也不能成为上游，退出仍需确认。
func TestRelaySelectionWithOtherDrafts(t *testing.T) {
	a := closeTestApp(t, "http://127.0.0.1:9/v1")
	a.SetDirty(true)
	if _, err := a.EnableRelay("demo"); err != nil {
		t.Fatal("another draft blocked saved relay connection", err)
	}
	if _, err := a.EnableRelay("unsaved"); err == nil {
		t.Fatal("unsaved connection became relay target")
	}
	if a.prepareClose(false) || a.closeSnapshot().Phase != "confirm" {
		t.Fatal("allowing saved relay connection weakened draft close protection")
	}
	a.CancelClose()
}
