//go:build !windows

package platform

import (
	"os/exec"
)

// HideProcess 在没有 Windows 控制台窗口的平台无需额外设置。
func HideProcess(cmd *exec.Cmd) {}
