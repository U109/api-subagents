//go:build windows

// 桌面入口只负责注入打包状态，业务与窗口生命周期位于 internal/desktop。
package main

import "github.com/U109/api-subagents/internal/desktop"

var packaged = "false"

// main 启动 Wails 桌面应用，源码运行时不自动下载更新。
func main() { desktop.Run(packaged == "true") }
