//go:build !windows

package platform

import (
	"net/http"
	"net/url"
)

// systemProxy 在其他平台遵循标准 HTTP_PROXY、HTTPS_PROXY 与 NO_PROXY。
func systemProxy(req *http.Request) (*url.URL, error) { return http.ProxyFromEnvironment(req) }
