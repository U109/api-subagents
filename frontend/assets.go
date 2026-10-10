package frontend

import "embed"

// Files 只嵌入明确列出的静态界面资源，配置和本机凭据没有进入可执行文件的路径。
//
//go:embed index.html config-ui.js config-operations.js config.css desktop-ui.js desktop.css approved-workbench.css workbench-views.js bridge.js shell-ui.js select-ui.js notification-ui.js model-picker-ui.js model-picker.css relay-diagnostics-ui.js
var Files embed.FS
