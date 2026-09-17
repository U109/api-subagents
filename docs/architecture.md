# 目录与构建

## 代码职责

| 目录 | 职责 |
| --- | --- |
| main.go、internal/desktop | Wails 入口、窗口生命周期与固定前端绑定 |
| frontend | 配置 Tab、连接列表、桌面和模式状态；静态资源编译进 Go |
| internal/config、internal/settings | 本机配置、密钥合并、浏览器配置 API |
| internal/providers | 委派任务的四种协议、SSE、超时、模型列表 |
| internal/tasks | 任务队列、续接、精简结果与 MCP 工具 |
| internal/workspace | 受限读取、搜索、建议与父模型确认后的应用 |
| internal/relay | Codex Responses 网关、CPA 转换、流式兼容 |
| internal/codex | Codex TOML 备份、恢复、共享模型目录与本地别名解析 |
| internal/plugin、internal/updates | 独立插件安装、GitHub 更新与校验 |
| internal/platform、internal/shared | Windows 代理/进程差异与公共数据工具 |
| internal/buildinfo、internal/testutil | 版本与隔离测试辅助 |
| cmd/worker | 独立 MCP EXE；同时提供配置、安装和应用建议命令 |
| cmd/package、packaging | 发布白名单、许可证收集、NSIS 与版本信息 |
| bundle | 构建时生成的插件 ZIP 嵌入点 |

测试紧邻对应 Go 包。任务包的集成测试覆盖完整读文件、提出修改和应用流程。go.mod / go.sum 固定依赖，CPA 通过固定版本 SDK 引入，保留相应许可证。

前端按职责拆分：`shell-ui.js` 管理侧栏与弹窗，`select-ui.js` 管理下拉菜单和键盘操作，`notification-ui.js` 管理顶部提示，`config-ui.js` 维护连接草稿，`model-picker-ui.js` 统一管理模型搜索、勾选与默认值，配套样式位于 `model-picker.css`；`desktop-ui.js` 同步安装、更新与挟持状态，`bridge.js` 保留固定 Go 绑定。静态资源同时列入嵌入和浏览器访问白名单。

连接配置的 `model` 与可选 `relayModels` 存储实际发送上游的模型 ID，前端编辑直接替换对应值，默认模型仍供 Worker 和旧连接别名使用。可选 `modelNames` 按 ID 保存显示名称，只用于目录展示。`internal/codex/models.go` 同时生成启动目录与网关 `/v1/models`，避免两个列表不一致；额外模型采用「连接名 + 模型 ID 的完整 SHA-256」稳定别名，顺序、显示名称和凭据变化不改变别名。修改 ID 会生成新别名，需要刷新并重新选择；网关只解析已配置别名，对每次请求的连接副本替换模型，不修改原配置，同名模型不会跨连接复用 Key。

Codex 的 `model_catalog_json` 在启动时加载，因此列表修改后需重启 Codex；详见 [官方配置参考](https://developers.openai.com/codex/config-reference/#model_catalog_json)。

桌面退出由 `internal/desktop/lifecycle.go` 管理：首次关闭先拦截窗口销毁，在后台恢复 Codex、取消网关请求并释放端口，成功后再放行退出。仅未保存草稿需要页面确认，安装与配置操作受同一状态锁保护；恢复失败保留窗口和备份。隔离桌面启动测试会先开启网关，再验证真实窗口关闭后的配置恢复。

## 构建

Windows x64，Go ≥ 1.26，桌面运行需要 WebView2。脚本可以下载校验后的 Go 工具链；安装包构建另外需要 NSIS 3。

```powershell
# 自动准备 Go、编译独立插件并检查发布白名单
.\scripts\build.ps1 -Target plugin

# 构建 Wails 桌面 EXE
.\scripts\build.ps1 -Target desktop

# 生成安装器与更新元数据；makensis.exe 需在 PATH 中，或设置 NSIS_EXE
.\scripts\build.ps1 -Target installer

go test ./...
go vet ./...
.\scripts\smoke.ps1
```

如果 Go 位于项目 .tools/go/bin，可直接使用该目录的 go.exe。构建不需要 npm，不随安装包分发 Go、Node 或浏览器引擎。

真实 Codex 兼容测试为可选项：设置 CODEX_TEST_BIN 为已安装的 codex.exe 路径后运行 `go test ./internal/relay -run TestCodex -v -timeout 180s`。测试自动创建独立 CODEX_HOME，使用本地模拟服务，不调用真实模型。

## 发布文件

App 版本在 packaging/release.json、internal/buildinfo/version.go 与 wails.json 中保持一致；插件版本来自 .codex-plugin/plugin.json，可独立变化，Worker 编译时使用插件版本。标签工作流编译、测试、检查版本及包清单，再发布完整 Release。

Release 包含安装器、Go 更新器使用的 update.json 和兼容 0.2.2 Electron 客户端的 latest.yml。新更新器核对大小与 SHA-256，启动前再次核对，不在普通退出时安装。旧客户端可回退为整包下载。

独立插件更新额外发布 `api-subagents-plugin-<版本>-windows-amd64.zip` 与 `plugin-update.json`，包含版本、大小、SHA-256、格式版本和最低 App 版本。ZIP 与 App 内嵌插件来自同一白名单；安装前在内存中检查完整文件集合、CRC、展开大小、版本及固定 Worker 入口，再复用原安装流程。桌面和插件更新共享受限下载器，分别维护版本状态与缓存。`packaging/release.json` 的 `pluginMinAppVersion` 声明插件需要的最低桌面版本。

桌面内嵌 ZIP 只来自明确文件清单。安装时将 worker 存入内容寻址的独立缓存，桌面替换不会覆盖正在运行的 MCP 文件。用户配置、任务和 Codex 备份均不属于发布输入。

.tools/、build/、release/、bundle/payload.zip、Wails 生成绑定不提交。旧的本机 node_modules/、dist/ 等生成目录即使仍存在也不参与构建。手写函数与方法上方需要中文注释。
