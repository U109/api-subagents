package frontend

import "embed"

// Files 只嵌入明确列出的静态界面资源，配置和本机凭据没有进入可执行文件的路径。
//
//go:embed index.html config-ui.js config.css desktop-ui.js desktop.css bridge.js shell-ui.js select-ui.js notification-ui.js relay-models-ui.js
var Files embed.FS
