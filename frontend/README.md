# 正式工作台前端

## 实现基准

以 2026-10-10 确认的 `ui-prototype-2026-10-10-access-simplified` 为唯一视觉基准。正式版复用其组件树、CSS 令牌、字号、间距、图标和交互库，不叠加旧主题或重新设计。

运行入口只加载 `index.html`、`bridge.js`、`workbench-app.js` 和 `workbench-app.css`；Go 嵌入与本地 HTTP 静态白名单保持一致。旧脚本留作历史参考，但不再加载或提供。`src/` 是手写源码，两个 `workbench-app.*` 文件是可复现构建产物。

| 文件 | 职责 |
| --- | --- |
| `src/app.jsx` | 原型组件、页面、Radix 对话框／菜单和 dnd-kit 鼠标／触屏／键盘排序 |
| `src/styles.css` | 已批准原型样式；末尾仅增加真实后台等待、进度和发布说明样式 |
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
npm --prefix frontend run test:visual
```

不要在仓库根目录运行裸 `npm install`。前端源码变化后，必须重建再运行浏览器测试或 Go 构建，避免验证旧静态产物。

浏览器测试只使用合成内存数据及本地资源，阻断外部请求，不读取真实连接或 Key。默认使用 Windows 已安装 Chrome；可设置 `CHROME_PATH`。视觉测试需要本地已批准 HTML；默认位于 `output/ui-prototype-2026-10-10-access-simplified/interactive-prototype.html`，也可设置 `APPROVED_PROTOTYPE`。基准和报告不属于发布白名单。

验收输出默认在 `output/approved-implementation-qa`，包括 `browser-qa.json`、`visual-comparison.json` 及截图。视觉对照覆盖共享主内容及模型弹窗：在同数据、同视口下检查内部组件的 geometry、字体、色彩、间距、边框、SVG 和像素；最多允许极少量 1/255 的独立栅格抗锯齿取整，不容许布局或可见色彩差异。

后台还需执行 `go test ./...` 和 `go vet ./...`。真实桌面启动使用 `scripts/smoke.ps1`，其 `API_SUBAGENTS_HOME` 与 `CODEX_HOME` 都是独立测试目录；校验 WebView2、固定绑定、配置级模型目录及退出时的 Codex 恢复。

## 必要的正式产品边界

- 不加载演示场景、示例连接、示例模型或原型标记。
- Key 使用真实编辑和脱敏逻辑，新建连接保留正式支持的接口类型选择。
- 插件与应用更新使用真实后台版本、网络取消、校验和安装状态，不模拟成功。
- 保存连接／配置不自动改变运行范围；配置模型选择不回写共享连接。总开关恢复最后明确启用的配置，参数变化必须明确重新应用。
- 真实失败在当前操作附近展示；空工作区不能假装启用成功。

这些边界不属于静态原型逐像素对照范围，需用绑定、后台及桌面集成测试另行验证。不要将本机配置、凭据、任务、备份、测试工具或原型输出放入 Git 或安装包。
