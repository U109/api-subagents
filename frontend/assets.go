package frontend

import "embed"

// Files 只嵌入明确列出的静态界面资源，配置和本机凭据没有进入可执行文件的路径。
//
//go:embed index.html config-ui.js config-operations.js config.css workbench.css desktop-ui.js desktop.css bridge.js shell-ui.js select-ui.js notification-ui.js model-picker-ui.js model-picker.css relay-diagnostics-ui.js radix-palette.css workbench-theme.css
var Files embed.FS
