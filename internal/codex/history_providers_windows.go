//go:build windows

package codex

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var historySQLite = windows.NewLazySystemDLL("winsqlite3.dll")
var historySQLiteOpen = historySQLite.NewProc("sqlite3_open_v2")
var historySQLiteClose = historySQLite.NewProc("sqlite3_close")
var historySQLiteExec = historySQLite.NewProc("sqlite3_exec")
var historySQLiteBusy = historySQLite.NewProc("sqlite3_busy_timeout")
var historyQueryID atomic.Uint32
var historyQueries sync.Map
var historyRowCallback = syscall.NewCallbackCDecl(collectProviderRow)

// collectProviderRow 接收 Windows SQLite 同步回调，只复制单列提供商身份；上下文编号隔离并发查询。
func collectProviderRow(context uintptr, count uintptr, values **byte, _ **byte) uintptr {
	query, exists := historyQueries.Load(context)
	if !exists || count != 1 || values == nil || *values == nil {
		return 1
	}
	ids := query.(*[]string)
	*ids = append(*ids, windows.BytePtrToString(*values))
	return 0
}

// readDatabaseProviderIDs 使用系统 SQLite 只读打开最新会话索引，兼容 WAL，不引入工具程序或聊天写入。
// 数据库被锁定时最多等待一秒；错误仅返回固定提示，不暴露数据库正文、地址或凭据。
func readDatabaseProviderIDs(path string) ([]string, error) {
	for _, procedure := range []*windows.LazyProc{historySQLiteOpen, historySQLiteClose, historySQLiteExec, historySQLiteBusy} {
		if err := procedure.Find(); err != nil {
			return nil, errors.New("系统 SQLite 不可用，无法读取旧对话提供商，未修改配置。")
		}
	}
	name, err := windows.BytePtrFromString(path)
	if err != nil {
		return nil, errors.New("Codex 会话索引路径无效，未修改配置。")
	}
	var database uintptr
	// SQLITE_OPEN_READONLY=1；不允许创建数据库，也不升级或修复会话表。
	status, _, _ := historySQLiteOpen.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&database)), 1, 0)
	runtime.KeepAlive(name)
	if database != 0 {
		defer historySQLiteClose.Call(database)
	}
	if status != 0 {
		return nil, errors.New("无法只读打开 Codex 会话索引，未修改配置。")
	}
	historySQLiteBusy.Call(database, 1000)
	query, _ := windows.BytePtrFromString("SELECT DISTINCT model_provider FROM threads WHERE model_provider IS NOT NULL AND model_provider <> ''")
	ids := []string{}
	context := uintptr(historyQueryID.Add(1))
	historyQueries.Store(context, &ids)
	defer historyQueries.Delete(context)
	status, _, _ = historySQLiteExec.Call(database, uintptr(unsafe.Pointer(query)), historyRowCallback, context, 0)
	runtime.KeepAlive(query)
	if status != 0 {
		return nil, errors.New("无法读取 Codex 旧对话提供商，未修改配置或聊天记录。")
	}
	return ids, nil
}
