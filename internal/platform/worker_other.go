//go:build !windows

package platform

import (
	"os/exec"
	"sync"
	"syscall"
)

// StartWorkerProcess 使用独立进程组收拢 App Server 和命令子进程，结束任务时一起终止。
func StartWorkerProcess(cmd *exec.Cmd) (func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }) }, nil
}
