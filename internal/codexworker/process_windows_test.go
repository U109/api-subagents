//go:build windows

package codexworker

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/shared"
	"github.com/U109/api-subagents/internal/testutil"
	"golang.org/x/sys/windows"
)

// TestCancellationKillsDescendants 确认 Windows Job Object 不只杀 App Server，也回收其已启动的命令子进程。
func TestCancellationKillsDescendants(t *testing.T) {
	_, profile := testutil.Config(t, "responses", "http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := t.TempDir()
	var handle windows.Handle
	var openErr error
	request := Request{Profile: profile, Task: "spawn synthetic command", Workspace: root, HomeRoot: t.TempDir(), Access: "workspace-write", MaxRequests: 2, Progress: func(progress shared.Object) {
		if progress["phase"] != "executing_codex" || handle != 0 {
			return
		}
		data, err := os.ReadFile(filepath.Join(root, "child.pid"))
		if err != nil {
			openErr = err
			cancel()
			return
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil {
			openErr = err
			cancel()
			return
		}
		handle, openErr = windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
		cancel()
	}}
	_, err := helperRunner(t, "spawn-child").Run(ctx, request)
	if handle != 0 {
		defer windows.CloseHandle(handle)
	}
	if err == nil || openErr != nil || handle == 0 {
		t.Fatal("descendant not observed", err, openErr)
	}
	status, err := windows.WaitForSingleObject(handle, 3000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("worker command survived cancellation", status, err)
	}
}
