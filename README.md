# API Subagents

用桌面 App 配置外部模型，一键安装 Codex 插件，把批量补注释、机械修改、定点排查等明确任务交给这些模型。

你可以为这些任务选择更便宜的模型，让 Codex 负责确定范围、检查结果和应用修改。支持 OpenAI 兼容服务 / CPA、OpenAI Responses、Claude 和 Gemini。

[快速开始](#快速开始) · [源码安装](#源码安装) · [配置模型](#配置模型) · [日常使用](#日常使用) · [发布新版本](#发布新版本) · [开发与仓库文件](#开发与仓库文件)

## 快速开始

推荐使用 **Windows 10/11 x64 桌面版**。电脑上需要先安装并启动 Codex；API Subagents 安装包自带运行环境，无需安装 Node.js 或运行 npm 命令。

1. 在 [GitHub Releases](https://github.com/U109/api-subagents/releases/latest) 下载 `API-Subagents-Setup-版本号-x64.exe`，双击安装。
2. 打开 **API Subagents**，点击 **安装到 Codex**。
3. 点击 **添加模型**，填写接口和 Key，选择模型并保存。
4. 在 Codex **新建对话**，即可委派任务。

当前发布仓库为私有仓库，需要登录有访问权限的 GitHub 账号后下载。安装包尚未配置代码签名证书，Windows 可能显示“发布者未知”。

桌面 App 和 `Configure.cmd` 使用同一份本机模型配置。关闭 App 后，已经安装的插件仍可独立运行。仅修改模型连接时，保存即可对新任务生效，不需要重新安装插件。

## 源码安装

二开或希望直接使用源码时，保留这两个入口：**`Install.cmd` 安装，`Configure.cmd` 配置**。

先克隆仓库，或选择 GitHub 的 **Code → Download ZIP** 并解压：

在 PowerShell 中执行：

```powershell
git clone https://github.com/U109/api-subagents.git
cd api-subagents
.\Install.cmd
.\Configure.cmd
```

也可以依次双击这两个文件。`Install.cmd` 会自动完成：

1. 检查 Node.js ≥ 22.12；缺少时下载到项目内的 `.runtime/`，并核对官方 SHA-256。
2. 执行 `npm ci` 安装锁定的依赖，再执行 `npm run build`。
3. 注册并安装到本机 Codex 的个人插件目录。

首次运行需要联网，终端会显示各步骤进度；任何一步失败都会停止并显示原因。不会修改系统 PATH。源码安装插件时，不会额外下载 Electron 的桌面运行程序。

如果 Codex 尚未安装或其命令行入口不可用，先安装并启动 Codex，再运行一次 `Install.cmd`；也可在 Codex 的 **Plugins → Personal** 中安装 **API Subagents**。

## 配置模型

打开配置页，点击 **添加模型**，按四个 Tab 填写：

| Tab          | 填写内容                                                                           |
| ------------ | ---------------------------------------------------------------------------------- |
| **连接信息** | 调用名称（如 `commenter`）、接口类型、API 地址和 API Key。                         |
| **模型选择** | 点击“拉取模型列表”，选择默认模型；接口不支持列表时，切换“手动输入”填写模型 ID。    |
| **任务分工** | 写明擅长的工作，如“便宜，适合批量补中文注释与机械替换”。Codex 会参考这里选择模型。 |
| **高级设置** | 按需调整输出长度、并发数和等待时间。一般可以保留默认值。                           |

点击 **保存配置** 完成。切换 Tab 不会丢失草稿。**测试连接** 会发送一条简短 API 请求，可能产生少量费用；测试通过表示基础连接可用，实际任务还需要模型支持工具调用。

- **重命名**：在“连接信息 → 调用名称”中修改并保存。名称以小写字母开头，只含小写字母、数字、`_` 或 `-`，最多 48 字符。
- **复制**：侧栏“⋯ → 复制”会保存一份独立副本，保留参数和密钥，自动生成不重复的名称。
- **删除**：侧栏“⋯ → 删除”，确认后立即生效。取消或按 Esc 不会删除。

<details>
<summary>接口类型和地址怎么选？</summary>

| 接口类型          | API 根地址示例                                                | 适用情况                                                                       |
| ----------------- | ------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| OpenAI 兼容 / CPA | CPA 常用 `http://127.0.0.1:8317/v1`；其他服务填写其提供的地址 | 提供 `/chat/completions` 接口的服务。                                          |
| OpenAI Responses  | `https://api.openai.com/v1`                                   | 提供 `/responses` 接口的服务。CPA 也可使用此类型，地址改为 CPA 的 API 根地址。 |
| Claude 原生       | `https://api.anthropic.com/v1`                                | Claude 原生接口。                                                              |
| Gemini 原生       | `https://generativelanguage.googleapis.com/v1beta`            | Gemini 原生接口。                                                              |

模型 ID 必须是服务实际提供的名称。重新拉取或筛选列表不会自动替换当前模型，修改选择后需要保存。

</details>

<details>
<summary>流式响应、等待时间和环境变量 Key</summary>

默认使用流式响应，接收期间持续更新任务进度。旧网关不支持流式请求时，可在“高级设置 → 响应方式”切换普通响应。

| 等待设置         | 默认值  | 可调范围  |
| ---------------- | ------- | --------- |
| 首个数据等待时间 | 180 秒  | 10–600 秒 |
| 响应中断等待时间 | 120 秒  | 10–600 秒 |
| 任务总时长上限   | 15 分钟 | 1–60 分钟 |

每次收到数据或心跳会重置中断等待时间，但不会延长任务总时长。断流不会自动重发生成请求。

也可以填写“从环境变量读取 Key”。环境变量优先于保存的 Key；需让 Codex 进程拥有该变量，并将变量名加入已安装插件 `.mcp.json` 的 `env_vars`。

</details>

## 日常使用

在 Codex 中描述要委派的工作，例如：

> 把 src/utils 下批量补中文注释的工作交给我配置的低价模型。你负责检查修改片段，并运行必要测试。

> 用 commenter 检查配置保存逻辑，只读取相关文件，找出可复现的问题并提出修改建议。

> 列出 API Subagents 中可用的模型及各自用途。

工作流程是：**Codex 确定任务和文件范围 → 外部模型读取文件并提出修改 → Codex 验收、应用并验证**。外部模型不能直接写入项目或执行终端命令。

插件是否自动使用，取决于 Codex 是否选择并加载相应 Skill。它不会拦截每条消息并强制转交外部模型；需要明确委派时，可以像上面的示例直接说明。

## 常见问题

**一定能节省 Codex 的额度吗？**

不能保证。适合委派的是范围明确、重复量较大的工作；简单问答和单行修改通常直接处理更划算。插件默认精简结果、优先返回局部修改，减少重复传递完整文件，但无法读取或保证 Codex 套餐额度的变化。外部模型 API 单独计费，流式响应本身不减少 token 用量。

**为什么拉取不到模型，或连接成功但任务失败？**

先确认接口类型、API 根地址、Key 和模型 ID。服务不提供模型列表时，直接手动输入 ID。连接测试只检查基础连通性，执行任务还需要模型支持工具调用；超时可在高级设置中调整。

**代码和 Key 存在哪里？**

任务描述及外部模型读取的文件内容会发送给你选择的 API 服务。Key 保存在本机配置中，配置页不会回显已保存的 Key。

| 内容               | 默认位置                                       |
| ------------------ | ---------------------------------------------- |
| 模型配置与 Key     | `%LOCALAPPDATA%\CodexApiSubagents\models.json` |
| 任务结果与修改建议 | `%LOCALAPPDATA%\CodexApiSubagents\tasks\`      |
| 已安装插件         | `%USERPROFILE%\plugins\api-subagents\`         |

可通过 `API_SUBAGENTS_HOME` 自定义数据目录。任务记录可能包含项目内容，按需清理；模型配置、任务记录和 `.env` 已列入 Git 忽略规则。

**如何更新插件？**

- **桌面版**：在 App 中点击“检查更新”。公开发布源可直接下载并重启更新；私有发布源会引导到 GitHub 登录下载，运行新版安装包覆盖安装即可。然后点击“更新插件”，并在 Codex 新建对话。
- **源码版**：运行 `git pull --ff-only`，然后双击 `Install.cmd`，依赖安装和构建会自动完成。

更新保留本机模型配置、Key 和自定义环境变量透传名单。App 不会在普通退出时擅自安装更新；有未保存的配置时，会要求先保存。

## 发布新版本

**是的，安装包通过 GitHub Release 分发。** 仓库已包含 Windows 构建与发布工作流。

1. 同步修改 `package.json`、`package-lock.json` 和 `.codex-plugin/plugin.json` 中的版本号，更新 `desktop/release-notes.md`。
2. 提交并推送源码，再推送与版本一致的标签，例如 `v0.2.1`。
3. GitHub Actions 自动测试、打包，并把安装包和更新信息上传到 Release；所有文件上传完成后才发布。

不发版本也能在 **Actions → Windows desktop → Run workflow** 手动构建，并在运行结果下载 `windows-x64` 产物。

手动打包可运行 `npm ci`、`npm run desktop:dist`。产物在 `release/`，手动上传 Release 时需要一起上传这三项，不能只传 exe：

| 文件                                 | 用途                                      |
| ------------------------------------ | ----------------------------------------- |
| `API-Subagents-Setup-版本号-x64.exe` | 用户安装包。                              |
| 同名 `.exe.blockmap`                 | 更新下载所需的文件块信息。                |
| `latest.yml`                         | 最新版本、安装包文件名及 SHA-512 校验值。 |

发布目标在 `desktop/release.json` 中配置。当前 `private: true` 使用登录下载；要让 App 免登录在线更新，需要公开的 Release 仓库，并改为 `private: false`。可单独建公开的安装包仓库，源码仓库继续保持私有。**安装包本身含有运行代码，公开安装包不等于代码无法被提取。**

如果安装包发布到另一个仓库，需要在源码仓库的 Actions Secrets 中配置 `RELEASE_TOKEN`，仅授予目标发布仓库的 Contents 写入权限。相同仓库直接使用 GitHub 提供的 `GITHUB_TOKEN`。这些令牌只用于发布工作流，不会进入安装包。

## 开发与仓库文件

修改源码后，先构建再测试；测试读取的是 `dist/` 中的构建结果：

```powershell
npm ci
npm run build
npm test
```

桌面开发使用 `npm run desktop:dev`；生成可运行目录使用 `npm run desktop:pack`；生成 Windows 安装包使用 `npm run desktop:dist`。桌面包包含校验后的独立 Node.js，构建时需要访问 nodejs.org 和 Electron 下载服务。

测试使用本地模拟 API，不需要真实 Key，也不会产生模型 API 费用。Windows 安装脚本的测试只在 Windows 上运行。其他平台可以开发 Node 部分，目前没有对应的安装脚本。

| 文件或目录                                                                                | 用途                                                       | 是否提交到 Git                       |
| ----------------------------------------------------------------------------------------- | ---------------------------------------------------------- | ------------------------------------ |
| `src/`                                                                                    | 插件服务、模型接口、任务处理和配置页源码。                 | 是                                   |
| `src/desktop/`、`desktop/`、`electron-builder.config.cjs`                                 | 桌面主进程、更新源、图标和安装包配置。                     | 是                                   |
| `.github/workflows/desktop.yml`                                                           | Windows 自动测试、打包和 Release 发布。                    | 是                                   |
| `tests/`                                                                                  | 流式响应、密钥保护、文件访问、配置操作和安装等自动化测试。 | 是                                   |
| `scripts/`、`Install.cmd`、`Configure.cmd`                                                | 构建、安装和配置入口。                                     | 是                                   |
| `skills/`、`.codex-plugin/`、`.mcp.json`                                                  | Codex 加载插件、工具与使用规则所需的文件。                 | 是                                   |
| `package.json`、`package-lock.json`                                                       | 依赖声明与锁定版本，使安装可复现。                         | 是                                   |
| `AGENTS.md`                                                                               | 项目开发约定，包括源码方法的中文注释要求。                 | 是                                   |
| `THIRD-PARTY-NOTICES.txt`                                                                 | 第三方依赖的版权及许可证说明。                             | 是                                   |
| `node_modules/`、`dist/`、`dist-desktop/`、`.desktop-resources/`、`.runtime/`、`release/` | 本地依赖、运行环境缓存、构建结果和安装包。                 | 否，可重新生成；安装包上传到 Release |
| `models.json`、`runtime.json`、`.env`、任务记录                                           | 本机配置、凭据与运行数据。                                 | 否                                   |

`tests/` 属于可维护源码的一部分，运行插件时不会执行它们。不要把测试代码与运行产生的日志、临时文件混为一类。

构建会将第三方依赖打包进运行文件，安装脚本也会复制 [THIRD-PARTY-NOTICES.txt](THIRD-PARTY-NOTICES.txt)。分发构建后的插件时，应一起保留相关版权和许可证文本；这个文件是集中保存这些说明的方式。

`THIRD-PARTY-NOTICES.txt` 是常见命名，文件名本身不是许可证要求。它记录第三方依赖的许可，**不代表本项目采用相同许可证**。本项目源码自身的授权应由单独的 `LICENSE` 声明；目前尚未选择并添加。
