package updates

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/U109/api-subagents/internal/shared"
)

// TestCancelLateUpdateCheck 模拟忽略取消的迟到发布响应；期间不能开启新操作，结束后不可下载或安装。
func TestCancelLateUpdateCheck(t *testing.T) {
	u := NewUpdater(Release{Owner: "U109", Repo: "api-subagents", Version: "0.4.1"}, t.TempDir(), true)
	started, release := make(chan struct{}), make(chan struct{})
	u.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v0.4.4"}`))}, nil
	})
	done := make(chan error, 1)
	go func() { done <- u.Check(context.Background()) }()
	<-started
	if !u.Cancel() || u.Snapshot().Phase != "cancelling" {
		t.Fatal("cancel not acknowledged")
	}
	if err := u.Check(context.Background()); err != nil {
		t.Fatal("already running check should not start another operation", err)
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || u.Snapshot().Phase != "cancelled" {
			t.Fatal("late result became available", err, u.Snapshot())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled check did not finish")
	}
	if err := u.Download(context.Background()); err == nil {
		t.Fatal("cancelled check can download")
	}
}

// TestCancelStreamingDownload 通过本地 HTTP 分块响应确认取消确实断开在途下载，而非只把界面标为取消。
func TestCancelStreamingDownload(t *testing.T) {
	data := []byte(strings.Repeat("synthetic-stream", 200))
	started, stopped := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/latest"):
			w.Write([]byte(`{"tag_name":"v0.4.4"}`))
		case strings.HasSuffix(r.URL.Path, "/update.json"):
			w.Write(shared.Marshal(UpdateManifest{Version: "0.4.4", File: "API-Subagents-Setup-0.4.4-x64.exe", SHA256: shared.Hash(data), Size: int64(len(data))}))
		default:
			w.Write(data[:500])
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			close(stopped)
		}
	}))
	defer server.Close()
	u := NewUpdater(Release{Owner: "U109", Repo: "api-subagents", Version: "0.4.1"}, t.TempDir(), true)
	endpoint, _ := url.Parse(server.URL)
	u.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		local := r.Clone(r.Context())
		local.URL.Scheme, local.URL.Host = endpoint.Scheme, endpoint.Host
		return http.DefaultTransport.RoundTrip(local)
	})
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- u.Download(context.Background()) }()
	<-started
	u.Cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || u.Snapshot().Phase != "cancelled" {
			t.Fatal("stream did not cancel", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("download still running")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request not cancelled")
	}
	files, _ := os.ReadDir(u.Cache)
	if len(files) != 0 {
		t.Fatal("stream cancellation left an installable file")
	}
}

// TestCancelLateDownloadAndPreserveCache 迟到的完整包不具备安装资格、临时文件清理；取消复查仍保留此前已校验的包。
func TestCancelLateDownloadAndPreserveCache(t *testing.T) {
	f := &updateCheckFixture{latest: "0.4.4", body: []byte(strings.Repeat("synthetic-installer", 100))}
	u := f.updater(t)
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	transport := u.Client.Transport
	started, release := make(chan struct{}), make(chan struct{})
	u.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(f.body)))}, nil
	})
	done := make(chan error, 1)
	go func() { done <- u.Download(context.Background()) }()
	<-started
	u.Cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) || u.Snapshot().Phase != "cancelled" {
		t.Fatal("late download not cancelled", err)
	}
	if _, err := u.Installer(); err == nil {
		t.Fatal("late cancelled download installable")
	}
	files, _ := os.ReadDir(u.Cache)
	if len(files) != 0 {
		t.Fatal("cancelled download left files", files)
	}
	u.Client.Transport = transport
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := u.Download(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := u.Check(ctx); !errors.Is(err, context.Canceled) || u.Snapshot().Phase != "downloaded" {
		t.Fatal("cancelled recheck lost verified package", err, u.Snapshot())
	}
	if _, err := u.Installer(); err != nil {
		t.Fatal("verified cache lost", err)
	}
}

// TestUpdatePreferencesPersistence 自动检查与跳过版本跨重建保留；同版本不下载，新版本仍可用，说明仅按纯文本限长。
func TestUpdatePreferencesPersistence(t *testing.T) {
	f := &updateCheckFixture{latest: "0.4.4", body: []byte(strings.Repeat("synthetic", 200))}
	u := f.updater(t)
	if err := u.SetAutoCheck(false); err != nil {
		t.Fatal(err)
	}
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := u.Skip(); err != nil {
		t.Fatal(err)
	}
	next := NewUpdater(u.Release, u.Cache, true)
	next.Client = u.Client
	if next.Snapshot().AutoCheck || next.Snapshot().SkippedVersion != "0.4.4" {
		t.Fatal("preferences lost")
	}
	if err := next.Check(context.Background()); err != nil || next.Snapshot().Phase != "skipped" {
		t.Fatal("skipped version reappeared", err)
	}
	if err := next.Download(context.Background()); err == nil {
		t.Fatal("skipped version can download")
	}
	f.latest = "0.4.5"
	if err := next.Check(context.Background()); err != nil || next.Snapshot().Phase != "available" {
		t.Fatal("new version also skipped", err)
	}
	if len([]rune(boundedNotes(strings.Repeat("中文", 10000)))) > 16002 {
		t.Fatal("notes not bounded")
	}
}
