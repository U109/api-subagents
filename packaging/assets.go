// Package packaging 保存随发行版编译的公开版本信息。
package packaging

import _ "embed"

//go:embed release.json
var Release []byte
