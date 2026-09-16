import fs from 'node:fs/promises';
import {createReadStream} from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';

const execute = promisify(execFile);

/** 流式计算运行文件的内容标识，避免重复复制同一 Node 或覆盖仍在运行的旧版本。 */
async function fileHash(file) {
  const hash = crypto.createHash('sha256');
  for await (const chunk of createReadStream(file)) hash.update(chunk);
  return hash.digest('hex');
}

/** 从桌面包安装插件；运行环境独立保存，关闭或更新桌面 App 不会停止 Codex 子任务。 */
export class PluginManager {
  /** 目录和执行器由主进程提供，页面只能触发安装，不能指定命令或文件路径。 */
  constructor({
    resources,
    profileRoot,
    dataRoot,
    codexHome,
    version,
    nodeVersion,
    onChange = () => {},
    execImpl = execute,
  }) {
    Object.assign(this, {
      resources,
      profileRoot,
      dataRoot,
      codexHome,
      version,
      nodeVersion,
      onChange,
      execImpl,
    });
    this.state = {
      phase: 'idle',
      installedVersion: '',
      message: '将插件安装到 Codex，即可使用配置的模型',
    };
  }

  /** 更新界面状态；不包含模型配置或任何 API Key。 */
  change(patch) {
    Object.assign(this.state, patch);
    this.onChange({...this.state});
  }

  /** 检查插件源与 Codex 缓存是否一致，区分已有文件和实际安装完成。 */
  async refresh() {
    if (this.state.phase === 'installing') return;
    try {
      const pluginRoot = path.join(this.profileRoot, 'plugins', 'api-subagents');
      const manifest = JSON.parse(
        await fs.readFile(path.join(pluginRoot, '.codex-plugin', 'plugin.json'), 'utf8'),
      );
      const market = JSON.parse(
        (
          await fs.readFile(
            path.join(this.profileRoot, '.agents', 'plugins', 'marketplace.json'),
            'utf8',
          )
        ).replace(/^\uFEFF/, ''),
      );
      if (!/^[\w.-]+$/.test(market.name) || !/^[\w.+-]+$/.test(manifest.version)) throw new Error();
      const cached = JSON.parse(
        await fs.readFile(
          path.join(
            this.codexHome,
            'plugins',
            'cache',
            market.name,
            'api-subagents',
            manifest.version,
            '.codex-plugin',
            'plugin.json',
          ),
          'utf8',
        ),
      );
      if (cached.version !== manifest.version) throw new Error();
      const installedVersion = manifest.version.split('+')[0];
      this.change({
        phase: 'installed',
        installedVersion,
        message:
          installedVersion === this.version
            ? '已安装，配置模型后在 Codex 新建对话即可使用'
            : `已安装 ${installedVersion}，可以更新插件到 ${this.version}`,
      });
    } catch {
      this.change({phase: 'idle', installedVersion: '', message: '点击安装，将插件添加到 Codex'});
    }
  }

  /** 把随包提供的 Node 复制到独立缓存；内容寻址文件名确保旧任务不被运行环境更新打断。 */
  async prepareRuntime() {
    const source = path.join(this.resources, 'runtime', 'node.exe');
    const hash = await fileHash(source);
    const root = path.join(this.dataRoot, 'runtime');
    await fs.mkdir(root, {recursive: true});
    await fs.copyFile(
      path.join(this.resources, 'runtime', 'LICENSE'),
      path.join(root, `LICENSE-node-${this.nodeVersion}.txt`),
    );
    const target = path.join(root, `node-${this.nodeVersion}-${hash.slice(0, 12)}.exe`);
    try {
      if ((await fileHash(target)) === hash) return target;
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
    }
    const temp = path.join(root, `${crypto.randomUUID()}.tmp`);
    try {
      await fs.copyFile(source, temp);
      await fs.rename(temp, target);
    } finally {
      await fs.rm(temp, {force: true});
    }
    return target;
  }

  /** 串行执行已有安装脚本，保留模型数据；缺少 Codex 时提示用户完成本机安装。 */
  async install() {
    if (this.state.phase === 'installing') return;
    this.change({phase: 'installing', message: '正在准备运行环境并安装插件…'});
    try {
      const node = await this.prepareRuntime();
      const script = path.join(this.resources, 'plugin', 'scripts', 'install.ps1');
      const {stdout} = await this.execImpl(
        'powershell.exe',
        [
          '-NoProfile',
          '-NonInteractive',
          '-ExecutionPolicy',
          'Bypass',
          '-File',
          script,
          '-ProfileRoot',
          this.profileRoot,
          '-NodePath',
          node,
          '-Json',
        ],
        {windowsHide: true, timeout: 120000, maxBuffer: 1024 * 1024},
      );
      const result = JSON.parse(stdout.trim().replace(/^\uFEFF/, ''));
      if (!result.installed) {
        this.change({
          phase: 'manual',
          message:
            '插件文件已准备好。请先安装并启动 Codex，再点击安装；也可在 Codex 的 Plugins → Personal 中安装。',
        });
        return;
      }
      this.change({
        phase: 'installed',
        installedVersion: result.version.split('+')[0],
        message: '安装成功！保存模型配置后，在 Codex 新建对话即可使用。',
      });
    } catch {
      this.change({phase: 'error', message: '插件安装未完成。请确认 Codex 已安装且可启动，然后重试。'});
    }
  }
}
