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
| internal/codexworker | 执行型子代理：临时 CODEX_HOME、App Server RPC、权限核验、执行证据与进程回收 |
| internal/plugin、internal/updates | 独立插件安装、GitHub 更新与校验 |
| internal/platform、internal/shared | Windows 代理/进程差异与公共数据工具 |
| internal/buildinfo、internal/testutil | 版本与隔离测试辅助 |
| cmd/worker | 独立 MCP EXE；同时提供配置、安装和应用建议命令 |
| cmd/package、packaging | 发布白名单、许可证收集、NSIS 与版本信息 |
| bundle | 构建时生成的插件 ZIP 嵌入点 |

测试紧邻对应 Go 包。任务包的集成测试覆盖完整读文件、提出修改和应用流程。go.mod / go.sum 固定依赖，CPA 通过固定版本 SDK 引入，保留相应许可证。

委派默认使用 `proposal` 后端；`execution_mode: codex` 改走 `internal/codexworker`，父任务正文通过 `turn/start` 明文交付。每个执行任务使用独立 App Server、私有 home 和仅接受已选模型的带令牌回环网关，复用 `internal/relay` 的四类协议适配，但不调用挟持模式的配置改写/恢复。真实上游凭据留在 Go 进程内。首次生成前核验 provider、model、cwd 和沙箱；服务端交互请求失败关闭，不自动审批。Windows 使用 kill-on-close Job Object，任务完成、取消和宿主退出会回收进程树。任务记录区分建议和直接执行，并记录有限的原生命令/补丁事件；执行模式不能走 `--apply` 二次应用。

`CODEX_TEST_BIN` 同样可启用 `go test ./internal/codexworker -run TestCodexWorker -v -timeout 240s`：使用本地合成模型验证四类协议的回答、实际文件修改、命令和测试执行，以及只读权限和请求限额；不调用付费模型。协议模拟测试还覆盖模型/provider 不匹配、审批拒绝、无响应进程、取消和临时目录清理。运行身份和状态不进入发布包。

正式前端直接复用已批准原型的 React／Radix／Lucide／dnd-kit 组件树与样式：`frontend/src/app.jsx` 绘制页面和可访问交互，`styles.css` 保存原型令牌；`controller.jsx` 管理草稿、定点保存、异步取消和实际桌面状态，`adapter.mjs` 在脱敏 Go 配置与组件形状之间转换，`domain.mjs` 维护配置级独立模型范围、排序及上下文策略，`transport.mjs` 只访问固定绑定或带令牌 API。`bridge.js` 保留固定 Go 绑定。嵌入和浏览器访问白名单只有 `index.html`、`bridge.js`、`workbench-app.js`、`workbench-app.css`；旧版脚本、原型、测试、npm 依赖与本机配置均不作为运行资源提供。

`internal/config/access.go` 管理稳定连接身份和接入配置；`accessConfigs` 保存独立的连接／模型子集及配置默认，`lastAccess` 只记录最后明确启用范围。配置服务在写锁内持久化并启用，失败回滚；普通保存不改变运行范围。Relay 持有不公开序列化的参数快照，请求开始后不跨配置切换；凭据按稳定身份获取最新值，但地址或协议变化时拒绝向旧目的地发送新密钥。指纹只包含已选非凭据参数，新增未选候选或轮换密钥不会误判为扩大范围。

连接配置的 `model` 与可选 `relayModels` 存储实际发送上游的模型 ID，前端编辑直接替换对应值，默认模型仍供 Worker 和旧连接别名使用。可选 `modelNames` 按 ID 保存显示名称，只用于目录展示。`internal/codex/models.go` 同时生成启动目录与网关 `/v1/models`，避免两个列表不一致；连接名按单个路径段编码，额外模型再拼接模型 ID 的完整 SHA-256，中文、空格和斜线不会混淆路由。旧连接别名保持不变，顺序、显示名称和凭据变化不改变别名。修改 ID 会生成新别名，需要刷新并重新选择；网关只路由到已配置模型，对每次请求的连接副本替换模型，不修改原配置，同名模型不会跨连接复用 Key。

开启模式会暂存全部已配置提供商，以及从历史索引和旧版会话首条元数据发现的身份，并将入口指向本地网关；内置 OpenAI 通过专用带令牌的回环路径接入，不覆盖同名提供商表或登录文件。具体本地别名只映射到已选模型，旧的真实模型 ID 则跟随当前连接的星标模型。关闭时按 TOML 字段语义恢复托管值，保留非托管修改；冲突保留配置和恢复快照。只读身份元数据，不读取聊天正文或改写会话。模型的 `modelImageInputs` 可显式声明图片能力，未设置时按常见视觉模型系列识别，只影响目录能力而不改写 Responses 图片内容。

服务目录明确声明的上下文写入 `modelOfficialContexts`，实际采用声明值与 256000 的较小值；未知容量保留旧手动配置或兼容默认值，不根据模型 ID 猜测。`model_catalog_json` 中每项均为具体模型，当前连接星标置顶；普通保存保留有效的手动选择，切换连接时更新默认选择。

Responses 透传的主对话请求可以在 502／503 或安全流前导失败时有限恢复，最多三次并沿用原取消与时限。前导检查只缓冲有限原始字节，不改写 SSE；已经交付正文或工具后不恢复。插件 worker、压缩、连接错误，以及不符合安全检查条件的流失败均不重放。

Codex 的 `model_catalog_json` 在启动时加载，因此列表、图片能力或提供商路由修改后需重启 Codex；详见 [官方配置参考](https://developers.openai.com/codex/config-reference/#model_catalog_json)。

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

如果 Go 位于项目 .tools/go/bin，可直接使用该目录的 go.exe。开发构建需要 Node.js 和 npm：`npm --prefix frontend ci --ignore-scripts --no-audit --no-fund` 后运行 `npm --prefix frontend run build`；构建脚本及 Wails 配置会执行这两步。最终安装包不分发 Go、Node、npm 依赖或浏览器引擎。

前端纯逻辑测试运行 `npm --prefix frontend test` 和 `node frontend/config-operations.test.cjs`；浏览器回归运行 `npm --prefix frontend run test:browser`，只使用本地合成绑定并阻断外部请求。Windows 默认使用已安装 Chrome，也可用 `CHROME_PATH` 指定浏览器；其他系统需准备 Playwright Chromium。`test:visual` 使用 `APPROVED_PROTOTYPE` 指定已批准自包含 HTML，在相同数据与尺寸下比较组件、计算样式和截图；视觉基准不是演示运行数据。截图和报告默认写入 `output/approved-implementation-qa`，可用 `WORKBENCH_TEST_OUTPUT` 指定输出目录；测试依赖及产物不进入发布包。详见 [前端构建与验收](../frontend/README.md)。

真实 Codex 兼容测试为可选项：设置 CODEX_TEST_BIN 为已安装的 codex.exe 路径后运行 `go test ./internal/relay -run TestCodex -v -timeout 180s`。测试自动创建独立 CODEX_HOME，使用本地模拟服务，不调用真实模型。

## 发布文件

App 版本在 packaging/release.json、internal/buildinfo/version.go 与 wails.json 中保持一致；插件版本来自 .codex-plugin/plugin.json，可独立变化，Worker 编译时使用插件版本。标签工作流编译、测试、检查版本及包清单，再发布完整 Release。

Release 包含安装器、Go 更新器使用的 update.json 和兼容 0.2.2 Electron 客户端的 latest.yml。新更新器核对大小与 SHA-256，启动前再次核对，不在普通退出时安装。旧客户端可回退为整包下载。

独立插件更新额外发布 `api-subagents-plugin-<版本>-windows-amd64.zip` 与 `plugin-update.json`，包含版本、大小、SHA-256、格式版本和最低 App 版本。ZIP 与 App 内嵌插件来自同一白名单；安装前在内存中检查完整文件集合、CRC、展开大小、版本及固定 Worker 入口，再复用原安装流程。桌面和插件更新共享受限下载器，分别维护版本状态与缓存。`packaging/release.json` 的 `pluginMinAppVersion` 声明插件需要的最低桌面版本。

桌面内嵌 ZIP 只来自明确文件清单。安装时将 worker 存入内容寻址的独立缓存，桌面替换不会覆盖正在运行的 MCP 文件。用户配置、任务和 Codex 备份均不属于发布输入。

.tools/、build/、release/、bundle/payload.zip、Wails 生成绑定不提交。旧的本机 node_modules/、dist/ 等生成目录即使仍存在也不参与构建。手写函数与方法上方需要中文注释。
