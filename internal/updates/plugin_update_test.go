package updates

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// pluginUpdateFixture 模拟 App 已是最新版本、插件仍可单独升级的公开发布接口，不访问网络或模型。
func pluginUpdateFixture(t *testing.T, kind string) (*Updater, *int) {
	t.Helper()
	data := []byte(strings.Repeat("synthetic-plugin-archive", 100))
	manifest := UpdateManifest{Version: "0.4.0", File: "api-subagents-plugin-0.4.0-windows-amd64.zip", SHA256: shared.Hash(data), Size: int64(len(data)), FormatVersion: 1, MinAppVersion: "0.3.3"}
	switch kind {
	case "incompatible":
		manifest.MinAppVersion = "2.0.0"
	case "format":
		manifest.FormatVersion = 2
	case "unsafe-path":
		manifest.File = "../worker.exe"
	case "invalid-version":
		manifest.Version = "0.4.0-beta"
	case "overflow-version":
		manifest.Version = "1.0.4294967296"
	case "overflow-minimum":
		manifest.MinAppVersion = "2.0.4294967296"
	}
	downloads := 0
	u := NewPluginUpdater(Release{Owner: "U109", Repo: "api-subagents", Version: "1.0.0"}, t.TempDir(), "0.3.3")
	if kind == "no-downgrade" {
		u.ResetPluginVersion("0.5.0")
	}
	u.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || (r.URL.Host != "github.com" && r.URL.Host != "api.github.com") || r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected update destination or credentials")
		}
		var body []byte
		switch {
		case strings.HasSuffix(r.URL.Path, "/latest"):
			body = []byte(`{"tag_name":"v1.0.0"}`)
		case strings.HasSuffix(r.URL.Path, "/plugin-update.json"):
			body = shared.Marshal(manifest)
		case strings.HasSuffix(r.URL.Path, "/"+manifest.File):
			downloads++
			body = append([]byte(nil), data...)
			if kind == "truncated" {
				body = body[:100]
			}
			if kind == "wrong-hash" {
				body[0] ^= 1
			}
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})
	return u, &downloads
}

// TestIndependentPluginUpdate 验证插件升级独立于 App 版本，并拒绝不兼容清单、降级、损坏下载与替换缓存。
func TestIndependentPluginUpdate(t *testing.T) {
	for _, kind := range []string{"complete", "incompatible", "format", "unsafe-path", "invalid-version", "overflow-version", "overflow-minimum", "no-downgrade", "truncated", "wrong-hash", "changed-cache"} {
		t.Run(kind, func(t *testing.T) {
			u, downloads := pluginUpdateFixture(t, kind)
			err := u.Check(context.Background())
			if kind == "format" || kind == "unsafe-path" || kind == "invalid-version" || strings.HasPrefix(kind, "overflow-") {
				if err == nil || *downloads != 0 {
					t.Fatal("invalid manifest accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "incompatible" || kind == "no-downgrade" {
				if u.Snapshot().Phase == "available" || *downloads != 0 {
					t.Fatal("unexpected plugin upgrade")
				}
				return
			}
			if u.Snapshot().Phase != "available" || *downloads != 0 {
				t.Fatal("check downloaded or compared the App version")
			}
			err = u.Download(context.Background())
			if kind == "truncated" || kind == "wrong-hash" {
				if err == nil {
					t.Fatal("corrupt archive accepted")
				}
				if files, _ := os.ReadDir(u.Cache); len(files) != 0 {
					t.Fatal("failed download left an installable file")
				}
				return
			}
			if err != nil || *downloads != 1 {
				t.Fatal(err)
			}
			if _, err = u.Installer(); err == nil {
				t.Fatal("plugin archive became executable installer")
			}
			if kind == "changed-cache" {
				if err = os.WriteFile(u.file, []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			data, version, err := u.PluginArchive()
			if kind == "changed-cache" {
				if err == nil {
					t.Fatal("modified cache accepted")
				}
				return
			}
			if err != nil || len(data) == 0 || version != "0.4.0" {
				t.Fatal(err, version)
			}
			u.ResetPluginVersion(version)
			if err = u.Check(context.Background()); err != nil || u.Snapshot().Phase != "latest" {
				t.Fatal("installed version was not updated", err)
			}
		})
	}
}

// TestPluginDownloadCancellation 用本地慢响应验证取消会停止流式下载，并清除本次临时文件。
func TestPluginDownloadCancellation(t *testing.T) {
	u, _ := pluginUpdateFixture(t, "complete")
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	u.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host = endpoint.Scheme, endpoint.Host
		return http.DefaultTransport.RoundTrip(copy)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- u.Download(ctx) }()
	<-started
	cancel()
	if err := <-finished; err == nil {
		t.Fatal("cancelled download succeeded")
	}
	if files, _ := filepath.Glob(filepath.Join(u.Cache, "*")); len(files) != 0 {
		t.Fatal("cancelled download left files")
	}
	if _, _, err := u.PluginArchive(); err == nil {
		t.Fatal("cancelled archive installable")
	}
}
