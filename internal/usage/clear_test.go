package usage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// clearRecord 只构造临时账本用的合成记录，不访问用户配置或付费接口。
func clearRecord(start int64) Record {
	return Record{ID: shared.UUID(), LogicalID: shared.UUID(), ConnectionID: "synthetic", Model: "local-model", StartedAt: start, Outcome: "success"}
}

// TestClearAllLedgerRecords 验证清除跨连接和跨时间历史，保留价格，并拒绝清除前发起的迟到完成。
func TestClearAllLedgerRecords(t *testing.T) {
	store := New(t.TempDir())
	prices, err := store.SavePrices(Prices{Rates: map[string]Price{Key("synthetic", "local-model"): {Input: 1, Output: 2, Read: 0, Write: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	oldWorker := New(store.Root)
	pending := clearRecord(now)
	oldWorker.stampRecord(&pending)
	for _, days := range []int64{0, 4, 80} {
		record := clearRecord(now - days*86400000)
		record.ConnectionID = shared.UUID()
		store.Save(record, prices)
	}
	result, err := store.Handle("/api/usage/clear", []byte(`{"confirm":true}`))
	if err != nil || result["cleared"] != true || result["cleanupPending"] != false {
		t.Fatalf("%v %#v", err, result)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "records")); !os.IsNotExist(err) {
		t.Fatal("legacy records not removed")
	}
	oldWorker.Save(pending, prices)
	// 重新创建句柄模拟独立 worker：新代次内同毫秒开始的请求应正常入账。
	current := clearRecord(now)
	New(store.Root).stampRecord(&current)
	New(store.Root).Save(current, prices)
	rows, _, err := New(store.Root).List(Query{From: now - 86400000, To: now + 1})
	if err != nil || len(rows) != 1 || rows[0].ID != current.ID {
		t.Fatalf("old attempt resurrected: %v %#v", err, rows)
	}
	after, err := store.Prices()
	if err != nil || !reflect.DeepEqual(after, prices) {
		t.Fatal("prices changed during clear", err)
	}
	if _, err := New(store.Root).Clear([]byte(`{"confirm":true}`)); err != nil {
		t.Fatal(err)
	}
	rows, _, err = New(store.Root).List(Query{})
	if err != nil || len(rows) != 0 {
		t.Fatal("second clear did not retire the new generation", err)
	}
}

// TestClearRequiresConfirmationAndAtomicSwitch 验证非法输入和账本指针损坏不会删除旧文件或价格。
func TestClearRequiresConfirmationAndAtomicSwitch(t *testing.T) {
	for _, body := range []string{"", "null", `{}`, `{"confirm":false}`, `{"scope":"all"}`, `{"confirm":true,"path":".."}`, `{"confirm":true} {}`} {
		store := New(t.TempDir())
		record := clearRecord(time.Now().UnixMilli() - 100)
		store.Save(record, Prices{})
		if _, err := store.Handle("/api/usage/clear", []byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
		rows, _, err := store.List(Query{})
		if err != nil || len(rows) != 1 {
			t.Fatal("invalid confirmation changed records", err)
		}
	}
	store := New(t.TempDir())
	record := clearRecord(time.Now().UnixMilli() - 100)
	store.Save(record, Prices{})
	if err := os.Mkdir(filepath.Join(store.Root, "records-state.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Clear([]byte(`{"confirm":true}`)); err == nil {
		t.Fatal("corrupt state accepted")
	}
	if _, err := os.Stat(filepath.Join(store.Root, "records")); err != nil {
		t.Fatal("failed switch removed old data")
	}
}

// TestClearDoesNotFollowDirectoryLinks 验证递归清理严格限定账本根目录，不触及外部目录。
func TestClearDoesNotFollowDirectoryLinks(t *testing.T) {
	store := New(t.TempDir())
	outside := t.TempDir()
	file := filepath.Join(outside, "preserve.json")
	if err := os.WriteFile(file, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store.Root, "records")); err != nil {
		t.Skip("symbolic links unavailable:", err)
	}
	if _, err := store.Clear([]byte(`{"confirm":true}`)); err == nil {
		t.Fatal("linked record directory accepted")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "preserve" {
		t.Fatal("outside file changed")
	}
}

// TestClearConcurrentWorkers 验证并行写入和清除后，清除前的尝试不会重新出现在新账本。
func TestClearConcurrentWorkers(t *testing.T) {
	store := New(t.TempDir())
	const count = 12
	var workers sync.WaitGroup
	ready, release := make(chan struct{}, count), make(chan struct{})
	for range count {
		workers.Go(func() {
			worker := New(store.Root)
			record := clearRecord(time.Now().UnixMilli())
			worker.stampRecord(&record)
			ready <- struct{}{}
			<-release
			worker.Save(record, Prices{})
		})
	}
	for range count {
		<-ready
	}
	if _, err := store.Clear([]byte(`{"confirm":true}`)); err != nil {
		t.Fatal(err)
	}
	close(release)
	workers.Wait()
	rows, _, err := New(store.Root).List(Query{})
	if err != nil || len(rows) != 0 {
		t.Fatalf("stale workers visible: %v %d", err, len(rows))
	}
}

// TestClearDuringRealTransport 验证实际上游请求在清除后才结束时不复活记录，清除后的新请求仍正常统计。
func TestClearDuringRealTransport(t *testing.T) {
	store := New(t.TempDir())
	arrived, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Do(func() {
			close(arrived)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"completed","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	profile := configstore.Profile{ID: "synthetic", Model: "local-model", Protocol: "responses"}
	client := WrapClient(server.Client(), New(store.Root))
	done := make(chan error, 1)
	go func() {
		request, _ := http.NewRequestWithContext(EnsureCall(ctx, profile, "synthetic", "proposal", "generate"), "POST", server.URL, nil)
		response, err := client.Do(request)
		if err == nil {
			_, err = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-arrived:
	case <-ctx.Done():
		t.Fatal("local upstream did not start")
	}
	if _, err := store.Clear([]byte(`{"confirm":true}`)); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rows, _, err := store.List(Query{To: time.Now().UnixMilli() + 1000})
	if err != nil || len(rows) != 0 {
		t.Fatal("late HTTP attempt resurrected records", err)
	}
	request, _ := http.NewRequestWithContext(EnsureCall(ctx, profile, "synthetic", "proposal", "generate"), "POST", server.URL, nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err = store.List(Query{To: time.Now().UnixMilli() + 1000})
	if err != nil || len(rows) != 1 {
		t.Fatalf("new generation not recorded: %v %d", err, len(rows))
	}
}
