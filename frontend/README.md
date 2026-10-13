# 正式工作台前端

## 实现基准

以当前确认的 `ui-prototype-2026-10-13-unified-workspace` 为唯一视觉基准。正式版直接复用生成该原型的组件树、CSS 令牌、字号、间距、图标和交互库，不叠加旧主题或重新设计。原型与正式版只在数据绑定上不同：原型使用合成内存数据，正式版走固定 Go 绑定或带令牌的本地配置 API。

运行入口只加载 `index.html`、`bridge.js`、`workbench-app.js` 和 `workbench-app.css`；Go 嵌入与本地 HTTP 静态白名单保持一致。旧脚本留作历史参考，但不再加载或提供。`src/` 是手写源码，两个 `workbench-app.*` 文件是可复现构建产物。

| 文件 | 职责 |
| --- | --- |
| `src/app.jsx` | 原型组件、页面、Radix 对话框／菜单和 dnd-kit 鼠标／触屏／键盘排序 |
| `src/styles.css` | 基础组件样式、真实后台等待、进度和发布说明样式 |
| `src/macos-settings.css` | 已确认的接入配置设计、灰色侧栏、蓝色状态及统一字号令牌 |
| `src/macos-workspace.css` | 我的连接、插件、帮助及弹窗的同主题布局与控件对齐 |
| `src/usage-page.jsx`、`src/usage.css`、`src/usage-domain.mjs` | 四个用量页签、真实用量与费率、蓝／绿／黄优先的模型趋势色 |
| `src/usage-state.mjs` | 最新请求身份、费率读写边界、跨页签草稿和外部修订冲突保护 |
| `src/controller.jsx` | 真实状态、定点保存、明确启用、取消、错误反馈和退出草稿保护 |
| `src/adapter.mjs` | 脱敏配置映射；保留协议、环境凭据来源、原生容量和候选顺序 |
| `src/domain.mjs` | 接入配置独立模型范围、默认项、排序和上下文策略 |
| `src/transport.mjs` | 固定 Go 绑定或带令牌回环 API，不接受任意命令或文件路径 |

## 构建与测试

开发机需要 Node.js／npm。依赖版本由 `package-lock.json` 锁定，最终程序不需要 Node.js。

```powershell
npm --prefix frontend ci --ignore-scripts --no-audit --no-fund
npm --prefix frontend run build
npm --prefix frontend test
node frontend/config-operations.test.cjs
npm --prefix frontend run test:browser
npm --prefix frontend run test:real-service
npm --prefix frontend run test:macos
npm --prefix frontend run test:workspace-design
npm --prefix frontend run test:visual
```

不要在仓库根目录运行裸 `npm install`。前端源码变化后，必须重建再运行浏览器测试或 Go 构建，避免验证旧静态产物。

常规浏览器测试使用合成内存数据及本地资源，阻断外部请求，不读取真实连接或 Key。`test:real-service` 另行构建 worker，在 `.tools/real-service-*` 下以独立 `API_SUBAGENTS_HOME` 和 `CODEX_HOME` 启动真实带令牌的 Go 服务，不注入模拟绑定；通过本地模拟上游验证创建／复制／改名／模型目录／默认／排序／接入配置／费率／用量／取消／删除和服务重启持久化。不会调用真实付费接口或改动已安装程序的数据。默认使用 Windows 已安装 Chrome；可设置 `CHROME_PATH`。视觉测试需要本地已批准的统一工作区 HTML；默认位于 `output/ui-prototype-2026-10-13-unified-workspace/interactive-prototype.html`，也可设置 `APPROVED_PROTOTYPE`。视觉对照只读取基准，不自动重写；`test:workspace-design` 会显式生成可点击原型和设计回归报告，应在需要更新原型时运行。基准和报告不属于发布白名单。

验收输出默认在 `output/approved-implementation-qa`，包括 `browser-qa.json`、`visual-comparison.json` 及截图。视觉对照从固定原型中读取合成数据，两侧固定相同日期，覆盖完整侧栏、接入配置、四个连接页签、四个用量页签、插件、帮助、添加连接与模型弹窗。在 375、1019、1180、1440px 下检查组件的 geometry、字体、色彩、间距、边框、SVG 和像素；CSS 和绑定桥接仍须与基准一致，功能修复允许 JS 指纹变化但不能重写视觉基准。最多允许极少量 1/255 的独立栅格抗锯齿取整，不容许布局或可见色彩差异。报告记录原型、原型 JS 和正式四文件的 SHA-256，并验证视觉查看没有保存配置或切换接入。

正式桌面程序通过 `scripts/build.ps1 -Target desktop` 构建到 `build/bin/API Subagents.exe`；不要用旧可执行文件验收新前端。构建脚本先重建四个白名单静态资源，再将其嵌入 Wails/WebView2 桌面程序，原型、测试夹具、工具及本机配置不进入可执行文件的发布载荷。

后台还需执行 `go test ./...` 和 `go vet ./...`。真实桌面启动使用 `scripts/smoke.ps1`，其 `API_SUBAGENTS_HOME` 与 `CODEX_HOME` 都是独立测试目录；校验 WebView2、固定绑定、配置级模型目录及退出时的 Codex 恢复。

## 必要的正式产品边界

- 不加载演示场景、示例连接、示例模型或原型标记。
- Key 使用真实编辑和脱敏逻辑，新建连接保留正式支持的接口类型选择。
- 插件与应用更新使用真实后台版本、网络取消、校验和安装状态，不模拟成功。
- 保存连接／配置不自动改变运行范围；配置模型选择不回写共享连接。总开关恢复最后明确启用的配置，参数变化必须明确重新应用。
- 保存／启用有明确等待反馈，冲突动作不能重复提交。保存不再让整个内容区 `inert`：表单可继续编辑，提交／删除／还原按钮暂时锁定。保存只确认发起时的提交副本，后续编辑保留为未保存草稿。已提交的 Key 清空，新输入的 Key 不被迟到结果清除；改名和复制后凭据引用更新为后台确认的目标。
- 用量刷新只接受最新读取；保存费率前或期间的旧读取不能回退费率修订。费率草稿切换页签／页面仍保留，关闭程序时也参与草稿保护；外部窗口修改后保留草稿并要求核对或确认重新载入，不默默覆盖对方。保存可见模型不会删除目录外的历史费率。
- 真实失败在当前操作附近展示；空工作区不能假装启用成功。

这些边界不属于静态原型逐像素对照范围，需用绑定、后台及桌面集成测试另行验证。不要将本机配置、凭据、任务、备份、测试工具或原型输出放入 Git 或安装包。
