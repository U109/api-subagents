//go:build windows

package platform

import (
	"os/exec"
	"syscall"
)

// HideProcess 防止安装、探测和浏览器启动等后台操作弹出终端窗口。
func HideProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
