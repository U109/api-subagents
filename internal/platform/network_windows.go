//go:build windows

package platform

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// systemProxy 优先环境变量，其次读取 Windows 的手动系统代理；回环接口始终直连。
func systemProxy(req *http.Request) (*url.URL, error) {
	if req.URL.Hostname() == "localhost" || req.URL.Hostname() == "127.0.0.1" || req.URL.Hostname() == "::1" {
		return nil, nil
	}
	if p, err := http.ProxyFromEnvironment(req); p != nil || err != nil {
		return p, err
	}
	// 明确设置环境代理时，nil 代表 NO_PROXY 命中或该协议未配置，不能再回退系统代理。
	if os.Getenv("HTTP_PROXY") != "" || os.Getenv("HTTPS_PROXY") != "" || os.Getenv("http_proxy") != "" || os.Getenv("https_proxy") != "" {
		return nil, nil
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil, nil
	}
	defer key.Close()
	enabled, _, _ := key.GetIntegerValue("ProxyEnable")
	if enabled == 0 {
		return nil, nil
	}
	value, _, _ := key.GetStringValue("ProxyServer")
	if strings.Contains(value, "=") {
		for _, part := range strings.Split(value, ";") {
			scheme, addr, _ := strings.Cut(part, "=")
			if scheme == req.URL.Scheme {
				value = addr
				break
			}
		}
		if strings.Contains(value, "=") {
			return nil, nil
		}
	}
	if value == "" {
		return nil, nil
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	return url.Parse(value)
}
