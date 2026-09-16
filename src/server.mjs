import {Server} from '@modelcontextprotocol/sdk/server/index.js';
import {StdioServerTransport} from '@modelcontextprotocol/sdk/server/stdio.js';
import {CallToolRequestSchema, ListToolsRequestSchema} from '@modelcontextprotocol/sdk/types.js';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawn} from 'node:child_process';
import {TaskManager} from './manager.mjs';
import {configPath} from './config.mjs';
import {startSetup} from './setup.mjs';
export {TaskManager} from './manager.mjs';
export {Workspace} from './workspace.mjs';
export {readConfig, saveConfig, publicConfig, validateConfig} from './config.mjs';
export {turn, initialState, addResults, probe} from './providers.mjs';
export {startSetup} from './setup.mjs';
export {listRemoteModels} from './catalog.mjs';

/** 生成 MCP 工具输入定义，禁止参数对象携带未声明的字段。 */
const input = (properties, required = []) => ({
  type: 'object',
  properties,
  required,
  additionalProperties: false,
});
/** 为字符串参数附加简短说明，减少重复的工具 schema 代码。 */
const str = (description) => ({type: 'string', description});
export const tools = [
  {
    name: 'list_models',
    description:
      'List workers and purposes without keys. Reuse this list during the task; honor user model restrictions.',
    inputSchema: input({}),
    annotations: {readOnlyHint: true},
  },
  {
    name: 'delegate_task',
    description:
      'Offload a bounded routine task to a configured API worker. Waits up to 25s and returns compact results by default. Sends task/files to its provider; proposals require parent review and application.',
    inputSchema: input(
      {
        model: str(
          'Configured worker name selected by the parent from list_models; the user need not supply it',
        ),
        task: str('Bounded task, context, constraints and acceptance criteria'),
        workspace: str('Absolute project directory'),
        continuation_id: str('Optional completed task_id to continue in this server session'),
        max_steps: {type: 'integer', minimum: 1, maximum: 30, default: 8},
        wait_ms: {type: 'integer', minimum: 0, maximum: 25000, default: 25000},
        detail: {type: 'string', enum: ['compact', 'full'], default: 'compact'},
      },
      ['model', 'task', 'workspace'],
    ),
    annotations: {readOnlyHint: false, destructiveHint: false, openWorldHint: true},
  },
  {
    name: 'task_status',
    description:
      'Read compact task results. Use detail=full only when omitted content or diagnostics are needed. Redacted proposals must not be applied verbatim.',
    inputSchema: input(
      {task_id: str('Task ID'), detail: {type: 'string', enum: ['compact', 'full'], default: 'compact'}},
      ['task_id'],
    ),
    annotations: {readOnlyHint: true},
  },
  {
    name: 'wait_task',
    description:
      'Wait up to 25s; prefer this over rapid status polling. Running means unfinished. Compact results omit full file content; request full only when needed.',
    inputSchema: input(
      {
        task_id: str('Task ID'),
        timeout_ms: {type: 'integer', minimum: 0, maximum: 25000, default: 25000},
        detail: {type: 'string', enum: ['compact', 'full'], default: 'compact'},
      },
      ['task_id'],
    ),
    annotations: {readOnlyHint: true},
  },
  {
    name: 'cancel_task',
    description: 'Cancel a queued or running API worker task.',
    inputSchema: input({task_id: str('Task ID')}, ['task_id']),
    annotations: {readOnlyHint: false, destructiveHint: false},
  },
  {
    name: 'configuration_help',
    description: 'Show where model settings are stored and how to open the local configuration page.',
    inputSchema: input({}),
    annotations: {readOnlyHint: true},
  },
];
/** 启动配置页面或 stdio MCP 服务；工具仅管理任务，文件建议由主 Agent 另行应用。 */
export async function main() {
  if (Number(process.versions.node.split('.')[0]) < 20) throw new Error('需要 Node.js 20 或更新版本。');
  if (process.argv.includes('--configure')) {
    const ui = await startSetup({
      htmlPath: path.join(path.dirname(fileURLToPath(import.meta.url)), 'config.html'),
    });
    console.error(`API Subagents 配置页面：${ui.url}\n关闭此进程即可关闭配置服务。`);
    if (!process.argv.includes('--no-open')) {
      let child;
      if (process.platform === 'win32')
        child = spawn('rundll32.exe', ['url.dll,FileProtocolHandler', ui.url], {
          windowsHide: true,
          stdio: 'ignore',
        });
      else
        child = spawn(process.platform === 'darwin' ? 'open' : 'xdg-open', [ui.url], {stdio: 'ignore'});
      child.on('error', () => console.error('请手动打开上面的配置页面地址。'));
    }
    const ttl = setTimeout(() => ui.server.close(), 30 * 60 * 1000);
    ttl.unref();
    return;
  }
  const manager = new TaskManager();
  const server = new Server(
    {name: 'api-subagents', version: '0.1.0'},
    {
      capabilities: {tools: {}},
      instructions:
        'Save parent context: offload bounded routine work when it replaces substantial parent reading or editing. Avoid duplicate investigations and needless delegation of trivial tasks. Honor model restrictions. Prefer compact results and long waits. Review affected changes and run focused checks once; apply reviewed proposals via the returned local command. Workers never apply changes.',
    },
  );
  server.setRequestHandler(ListToolsRequestSchema, async () => ({tools}));
  server.setRequestHandler(CallToolRequestSchema, async (request) => {
    try {
      const args = request.params.arguments || {};
      let result;
      switch (request.params.name) {
        case 'list_models':
          result = {models: await manager.listModels()};
          break;
        case 'delegate_task':
          if (
            !Number.isInteger(args.wait_ms ?? 25000) ||
            (args.wait_ms ?? 25000) < 0 ||
            (args.wait_ms ?? 25000) > 25000
          )
            throw new Error('wait_ms 必须为 0–25000。');
          if (!['compact', 'full'].includes(args.detail ?? 'compact'))
            throw new Error('detail 必须为 compact 或 full。');
          result = await manager.submit(args);
          result = await manager.wait(result.task_id, args.wait_ms ?? 25000);
          result = manager.present(result, args.detail);
          break;
        case 'task_status':
          result = manager.present(await manager.get(args.task_id), args.detail);
          break;
        case 'wait_task':
          result = manager.present(
            await manager.wait(args.task_id, args.timeout_ms ?? 25000),
            args.detail,
          );
          break;
        case 'cancel_task':
          result = manager.present(await manager.cancel(args.task_id));
          break;
        case 'configuration_help':
          result = {
            configFile: configPath(),
            instructions:
              '双击插件包中的 Configure.cmd，填写调用名称、接口类型、API 地址和 API Key，点击“拉取模型列表”并选择默认模型，也可手动输入模型 ID。保存后立即对新任务生效。连接测试会发送一条简短 API 请求。',
          };
          break;
        default:
          throw new Error('未知工具。');
      }
      return {content: [{type: 'text', text: JSON.stringify(result)}]};
    } catch (e) {
      return {isError: true, content: [{type: 'text', text: e.message || '操作失败。'}]};
    }
  });
  /** 退出前等待后台任务收尾，再关闭 MCP 传输，避免丢失已产生的结果。 */
  const stop = async () => {
    await manager.close();
    await server.close();
  };
  process.once('SIGINT', () => void stop());
  process.once('SIGTERM', () => void stop());
  process.stdin.once('end', () => void stop());
  await server.connect(new StdioServerTransport());
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url))
  main().catch((e) => {
    console.error(e.message);
    process.exitCode = 1;
  });
