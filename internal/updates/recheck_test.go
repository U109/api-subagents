package updates

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

// updateCheckFixture 提供可推进版本和故障的本地发布源，记录实际下载版本，不触及已安装应用。
type updateCheckFixture struct {
	latest    string
	body      []byte
	fail      bool
	checks    int
	downloads []string
}

// updater 构造固定为 0.4.1 的隔离更新器，只接受最新版本清单和对应安装包路径。
func (f *updateCheckFixture) updater(t *testing.T) *Updater {
	t.Helper()
	u := NewUpdater(Release{Owner: "U109", Repo: "api-subagents", Version: "0.4.1"}, t.TempDir(), true)
	u.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body []byte
		switch {
		case strings.HasSuffix(req.URL.Path, "/latest"):
			f.checks++
			if req.Header.Get("Cache-Control") != "no-cache" {
				t.Error("metadata cache was not revalidated")
			}
			if f.fail {
				return nil, errors.New("synthetic offline")
			}
			body = shared.Marshal(shared.Object{"tag_name": "v" + f.latest})
		case strings.HasSuffix(req.URL.Path, "/v"+f.latest+"/update.json"):
			body = shared.Marshal(UpdateManifest{Version: f.latest, File: "API-Subagents-Setup-" + f.latest + "-x64.exe", SHA256: shared.Hash(f.body), Size: int64(len(f.body))})
		case strings.HasSuffix(req.URL.Path, "/v"+f.latest+"/API-Subagents-Setup-"+f.latest+"-x64.exe"):
			f.downloads = append(f.downloads, f.latest)
			body = f.body
		default:
			t.Fatalf("unexpected release path: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	return u
}

// TestUpdateSkipsIntermediateVersions 覆盖首次检查、发现旧版和已下载旧版三种状态，直接从 0.4.1 获取 0.4.4。
func TestUpdateSkipsIntermediateVersions(t *testing.T) {
	for _, stage := range []string{"fresh", "available", "downloaded"} {
		t.Run(stage, func(t *testing.T) {
			f := &updateCheckFixture{latest: "0.4.2", body: []byte(strings.Repeat("synthetic-installer", 100))}
			u := f.updater(t)
			if stage != "fresh" {
				if err := u.Check(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "downloaded" {
				if err := u.Download(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			f.latest = "0.4.4"
			before := len(f.downloads)
			if err := u.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			state := u.Snapshot()
			if state.Phase != "available" || state.AvailableVersion != "0.4.4" || len(f.downloads) != before {
				t.Fatal("did not offer latest without downloading", state)
			}
			if _, err := u.Installer(); err == nil {
				t.Fatal("old installer still executable")
			}
			if err := u.Download(context.Background()); err != nil {
				t.Fatal(err)
			}
			file, err := u.Installer()
			if err != nil || !strings.HasSuffix(file, "API-Subagents-Setup-0.4.4-x64.exe") || f.downloads[len(f.downloads)-1] != "0.4.4" {
				t.Fatal("wrong installer", err)
			}
		})
	}
}

// TestUpdateRecheckDownloaded 验证同包无需重复下载、网络失败保留完整缓存、回退清单不降级且安装时仍拒绝篡改。
func TestUpdateRecheckDownloaded(t *testing.T) {
	for _, kind := range []string{"same", "offline", "stale", "changed-manifest", "tampered"} {
		t.Run(kind, func(t *testing.T) {
			f := &updateCheckFixture{latest: "0.4.4", body: []byte(strings.Repeat("synthetic-installer", 100))}
			u := f.updater(t)
			if err := u.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := u.Download(context.Background()); err != nil {
				t.Fatal(err)
			}
			file, err := u.Installer()
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "offline":
				f.fail = true
			case "stale":
				f.latest = "0.4.2"
			case "changed-manifest":
				f.body = append(f.body, []byte("changed")...)
			case "tampered":
				if err := os.WriteFile(file, []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = u.Check(context.Background())
			if (err != nil) != (kind == "offline" || kind == "stale") {
				t.Fatal("unexpected check result", err)
			}
			if f.checks != 2 || len(f.downloads) != 1 {
				t.Fatal("check skipped or silently redownloaded")
			}
			if kind == "changed-manifest" {
				if u.Snapshot().Phase != "available" {
					t.Fatal("changed manifest reused old bytes")
				}
				if _, err := u.Installer(); err == nil {
					t.Fatal("old package installable")
				}
				if err := u.Download(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else if u.Snapshot().Phase != "downloaded" || u.Snapshot().Progress != 100 {
				t.Fatal("download state lost", u.Snapshot())
			}
			_, err = u.Installer()
			if (err != nil) != (kind == "tampered") {
				t.Fatal("cache integrity check failed", err)
			}
		})
	}
}

// TestPluginRecheckPreservesDownloaded 验证独立插件检查复用同一份清单，但安装输入仍需要再次验证。
func TestPluginRecheckPreservesDownloaded(t *testing.T) {
	u, downloads := pluginUpdateFixture(t, "complete")
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := u.Download(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u.Snapshot().Phase != "downloaded" || *downloads != 1 {
		t.Fatal("plugin download lost")
	}
	if _, _, err := u.PluginArchive(); err != nil {
		t.Fatal(err)
	}
}
