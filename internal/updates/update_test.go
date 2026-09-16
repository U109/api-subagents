package updates

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/U109/api-subagents/internal/shared"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip 把固定 GitHub URL 的测试请求交给本地服务，测试不依赖实际发布或网络。
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestVerifiedUpdate 验证检查不会下载、下载不会安装、内容被篡改时安装入口拒绝继续。
func TestVerifiedUpdate(t *testing.T) {
	for _, kind := range []string{"complete", "truncated", "wrong-hash", "bad-manifest", "modified-cache"} {
		t.Run(kind, func(t *testing.T) {
			data := []byte(strings.Repeat("synthetic-installer", 100))
			manifest := UpdateManifest{Version: "0.3.0", File: "API-Subagents-Setup-0.3.0-x64.exe", SHA256: shared.Hash(data), Size: int64(len(data))}
			if kind == "bad-manifest" {
				manifest.File = "../other.exe"
			}
			downloads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/latest"):
					io.WriteString(w, `{"tag_name":"v0.3.0"}`)
				case strings.HasSuffix(r.URL.Path, "update.json"):
					json.NewEncoder(w).Encode(manifest)
				default:
					downloads++
					body := append([]byte{}, data...)
					if kind == "truncated" {
						body = body[:100]
					}
					if kind == "wrong-hash" {
						body[0] = 'X'
					}
					w.Write(body)
				}
			}))
			defer server.Close()
			u := NewUpdater(Release{Owner: "U109", Repo: "api-subagents", Version: "0.2.2"}, t.TempDir(), true)
			base, _ := url.Parse(server.URL)
			u.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				copy := r.Clone(r.Context())
				copy.URL.Scheme, copy.URL.Host = base.Scheme, base.Host
				return http.DefaultTransport.RoundTrip(copy)
			})
			if err := u.Check(context.Background()); kind == "bad-manifest" {
				if err == nil || downloads != 0 {
					t.Fatal("unsafe manifest accepted")
				}
				return
			} else if err != nil {
				t.Fatal(err)
			}
			if downloads != 0 || u.Snapshot().Phase != "available" {
				t.Fatal("check performed download")
			}
			err := u.Download(context.Background())
			if kind == "truncated" || kind == "wrong-hash" {
				if err == nil {
					t.Fatal("bad download accepted")
				}
				if _, err = u.Installer(); err == nil {
					t.Fatal("bad download installable")
				}
				return
			}
			if err != nil || downloads != 1 || u.Snapshot().Phase != "downloaded" {
				t.Fatal(err, downloads, u.Snapshot())
			}
			if kind == "modified-cache" {
				os.WriteFile(u.file, []byte("replaced"), 0600)
				if _, err = u.Installer(); err == nil {
					t.Fatal("modified cache accepted")
				}
			} else if _, err = u.Installer(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestUpdateBoundaries 检查稳定版本比较、私有源手动路径和下载重定向域名限制。
func TestUpdateBoundaries(t *testing.T) {
	for _, pair := range [][2]string{{"0.2.2", "0.3.0"}, {"0.3.0", "0.3.0"}, {"0.4.0-beta", "0.3.0"}, {"01.0.0", "0.3.0"}} {
		if NewerVersion(pair[0], pair[1]) {
			t.Fatal(pair)
		}
	}
	if !NewerVersion("v0.3.0", "0.2.2") {
		t.Fatal("newer version rejected")
	}
	u := NewUpdater(Release{Owner: "U109", Repo: "api-subagents", Version: "0.3.0", Private: true}, t.TempDir(), true)
	if err := u.Check(context.Background()); err != nil || u.Snapshot().Phase != "manual" {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://github.com/a", "https://evil.invalid/a", "https://github.com.evil.invalid/a"} {
		r, _ := http.NewRequest("GET", endpoint, nil)
		if releaseRedirect(r, nil) == nil {
			t.Fatal("unsafe redirect accepted")
		}
	}
}
