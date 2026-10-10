//go:build windows

package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// historyDatabaseFixture 在隔离目录创建合成 SQLite/WAL 索引并保持写连接打开，不接触真实 CODEX_HOME。
func historyDatabaseFixture(t *testing.T, path, sql string) {
	t.Helper()
	name, err := windows.BytePtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	var database uintptr
	status, _, _ := historySQLiteOpen.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&database)), 6, 0)
	runtime.KeepAlive(name)
	if database != 0 {
		t.Cleanup(func() { historySQLiteClose.Call(database) })
	}
	if status != 0 {
		t.Fatal("unable to create isolated SQLite fixture", status)
	}
	query, _ := windows.BytePtrFromString("PRAGMA journal_mode=WAL; CREATE TABLE threads(model_provider TEXT);" + sql)
	status, _, _ = historySQLiteExec.Call(database, uintptr(unsafe.Pointer(query)), 0, 0, 0)
	runtime.KeepAlive(query)
	if status != 0 {
		t.Fatal("unable to populate isolated SQLite fixture", status)
	}
}

// TestHistorySQLiteReadOnly 验证读取最新数字版本和尚在 WAL 中的身份，不写入数据库或改变会话行。
func TestHistorySQLiteReadOnly(t *testing.T) {
	c := CodexConfig{Home: t.TempDir(), DataRoot: t.TempDir()}
	historyDatabaseFixture(t, filepath.Join(c.Home, "state_2.sqlite"), "INSERT INTO threads VALUES('stale');")
	path := filepath.Join(c.Home, "state_10.sqlite")
	historyDatabaseFixture(t, path, "INSERT INTO threads VALUES('custom'),('deleted'),('custom'),('openai'),('旧身份'),(''),(NULL);")
	before, _ := os.ReadFile(path)
	walBefore, _ := os.ReadFile(path + "-wal")
	for range 3 {
		ids, err := c.historyProviderIDs()
		if err != nil || !reflect.DeepEqual(ids, []string{"custom", "deleted", "openai", "旧身份"}) {
			t.Fatal("latest historical providers missing", ids, err)
		}
	}
	after, _ := os.ReadFile(path)
	walAfter, _ := os.ReadFile(path + "-wal")
	if !bytes.Equal(before, after) || !bytes.Equal(walBefore, walAfter) {
		t.Fatal("read-only history query modified SQLite state")
	}
}
