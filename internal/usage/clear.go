package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/U109/api-subagents/internal/shared"
)

// ledgerState 用原子指针隔离清除前后的账本；其他 worker 也从磁盘读取代次，迟到请求不能复活旧记录。
type ledgerState struct {
	Generation string `json:"generation"`
	ClearedAt  int64  `json:"clearedAt"`
}

// directory 只由程序生成的 UUID 选择固定目录，不接受前端提供的路径。
func (state ledgerState) directory() string {
	if state.Generation == "" {
		return "records"
	}
	return "records-" + state.Generation
}

// readLedgerState 不存在时兼容旧账本，损坏或链接指针明确失败，不能重新展示已清除的历史。
func readLedgerState(root *os.Root) (ledgerState, error) {
	var state ledgerState
	info, err := root.Lstat("records-state.json")
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 {
		return state, errors.New("无法读取用量账本代次")
	}
	data, err := root.ReadFile("records-state.json")
	if err != nil || json.Unmarshal(data, &state) != nil || !shared.IDPattern.MatchString(state.Generation) || state.ClearedAt <= 0 {
		return state, errors.New("用量账本代次损坏，请检查本地账本")
	}
	return state, nil
}

// checkLedgerDirectory 允许尚未生成的账本目录，但拒绝链接和同名文件，不借父目录链接读取其他数据。
func checkLedgerDirectory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("账本目录不是普通目录")
	}
	return nil
}

// writeLedgerFile 在已打开的目录边界内原子替换文件，不跟随指向外部的目录链接。
func writeLedgerFile(root *os.Root, name string, data []byte) error {
	if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	temp := name + "." + shared.UUID() + ".tmp"
	defer root.Remove(temp)
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return root.Rename(temp, name)
}

// stampRecord 在实际上游尝试开始时捕获账本代次，支持其他进程清除和同毫秒内的新请求。
func (s *Store) stampRecord(record *Record) {
	root, err := os.OpenRoot(s.Root)
	if os.IsNotExist(err) {
		record.generation = "records"
		return
	}
	if err != nil {
		record.generation = "unavailable"
		return
	}
	defer root.Close()
	state, err := readLedgerState(root)
	if err != nil {
		record.generation = "unavailable"
		return
	}
	record.generation = state.directory()
}

// Clear 只切换请求账本后删除旧目录；价格不变，切换失败保留原记录，清理失败则返回明确降级信息。
func (s *Store) Clear(body []byte) (shared.Object, error) {
	var input struct {
		Confirm bool `json:"confirm"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || !input.Confirm || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("清除全部请求统计需要显式确认")
	}
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	if s.Root == "" {
		return nil, errors.New("用量账本目录无效")
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return nil, errors.New("无法打开用量账本，原记录未清除")
	}
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return nil, errors.New("无法打开用量账本，原记录未清除")
	}
	defer root.Close()
	previous, err := readLedgerState(root)
	if err != nil {
		return nil, err
	}
	old := previous.directory()
	if err := checkLedgerDirectory(root, old); err != nil {
		return nil, errors.New("账本目录不是普通目录，原记录未清除")
	}
	next := ledgerState{Generation: shared.UUID(), ClearedAt: time.Now().UnixMilli()}
	if err := root.Mkdir(next.directory(), 0700); err != nil {
		return nil, errors.New("无法建立新账本，原记录未清除")
	}
	if err := writeLedgerFile(root, "records-state.json", shared.Marshal(next)); err != nil {
		_ = root.Remove(next.directory())
		return nil, errors.New("无法切换用量账本，原记录未清除")
	}
	s.lost.Store(0)
	// 删除目标由已校验代次构造且限定在 os.Root 内，不能递归删除配置、价格或外部链接目标。
	cleanupErr := root.RemoveAll(old)
	return shared.Object{"cleared": true, "cleanupPending": cleanupErr != nil, "now": next.ClearedAt}, nil
}
