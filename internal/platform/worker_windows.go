//go:build windows

package platform

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// StartWorkerProcess 在发送任何任务前把 App Server 纳入 Job Object；关闭句柄或宿主退出会终止整个进程树。
// 无法建立进程约束时立即失败，不能降级为无人管理的后台执行。
func StartWorkerProcess(cmd *exec.Cmd) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	HideProcess(cmd)
	if err = cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		windows.CloseHandle(process)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		windows.CloseHandle(job)
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = windows.CloseHandle(job) }) }, nil
}
