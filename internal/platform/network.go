package platform

import (
	"net/http"
)

// DefaultTransport 复用系统代理设置与标准 TLS 校验，不向配置或发布包写入代理地址。
func DefaultTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = systemProxy
	return t
}
