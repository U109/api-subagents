import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
import {fileURLToPath} from 'node:url';
import {dataDir} from './config.mjs';
import {Workspace} from './workspace.mjs';

/**
 * 由主 Agent 在审查后调用，把任务记录中的完整建议写入指定项目，避免让 GPT 重新抄写整份文件。
 * 先校验所有选中文件的路径、脱敏标记和 SHA，再逐个原子替换；工作副本冲突时拒绝覆盖。
 * paths 为空时应用全部建议；非空时仅处理主 Agent 已审查的文件。
 */
export async function applyProposals(
  taskId,
  workspace,
  paths = [],
  storageDir = path.join(dataDir(), 'tasks'),
) {
  if (typeof taskId !== 'string' || !/^[a-f0-9-]{36}$/.test(taskId)) throw new Error('无效的 task_id。');
  const task = JSON.parse(await fs.readFile(path.join(storageDir, taskId + '.json'), 'utf8'));
  const ws = await Workspace.open(workspace);
  if (
    task.status !== 'completed' ||
    typeof task.workspace !== 'string' ||
    path.relative(ws.root, task.workspace) !== ''
  )
    throw new Error('只能应用已完成且属于指定项目的任务。');
  if (!Array.isArray(task.changes)) throw new Error('任务记录缺少文件建议。');
  const selected = paths.length
    ? task.changes.filter((change) => paths.includes(change.path))
    : task.changes;
  if (paths.some((name) => !selected.some((change) => change.path === name)))
    throw new Error('选择的文件不在任务建议中。');
  const seen = new Set();
  for (const change of selected) {
    if (change.redacted) throw new Error('含脱敏占位符的建议不能直接应用，请结合原文件处理。');
    const staged = await ws.propose(change.path, change.content, change.originalSha256);
    const key = process.platform === 'win32' ? staged.path.toLowerCase() : staged.path;
    if (seen.has(key)) throw new Error('任务记录含重复文件建议。');
    seen.add(key);
    if (
      change.originalSha256 !== null &&
      (await fs.lstat(await ws.resolve(change.path))).isSymbolicLink()
    )
      throw new Error('请针对实际文件提出建议，不能直接替换符号链接。');
  }
  const applied = [];
  try {
    for (const change of selected) {
      const target = await ws.resolve(change.path, true);
      await fs.mkdir(path.dirname(target), {recursive: true});
      await ws.propose(change.path, change.content, change.originalSha256);
      const mode = change.originalSha256 === null ? 0o666 : (await fs.stat(target)).mode;
      const temporary = target + '.proposal-' + crypto.randomUUID() + '.tmp';
      try {
        await fs.writeFile(temporary, change.content, {flag: 'wx', mode});
        // 写临时文件期间用户可能保存了目标，提交前再次核对版本与链接边界。
        await ws.propose(change.path, change.content, change.originalSha256);
        if (change.originalSha256 === null) await fs.link(temporary, target);
        else await fs.rename(temporary, target);
        applied.push(change.path);
      } finally {
        await fs.rm(temporary, {force: true});
      }
    }
    return {task_id: taskId, applied, changesApplied: true};
  } catch (error) {
    // 多文件写入不是文件系统事务；明确返回已落盘的路径，便于主 Agent 处理后续故障。
    error.applied = applied;
    throw error;
  }
}

/** 解析本地脚本参数；此入口不调用模型、不运行项目命令，也不绕过主 Agent 的文件权限。 */
async function main() {
  const [taskId, flag, workspace, ...paths] = process.argv.slice(2);
  if (flag !== '--workspace' || !workspace)
    throw new Error(
      '用法：node apply-proposals.mjs TASK_ID --workspace 绝对项目路径 [相对文件路径 ...]',
    );
  console.log(JSON.stringify(await applyProposals(taskId, workspace, paths)));
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(JSON.stringify({error: error.message, applied: error.applied || []}));
    process.exitCode = 1;
  });
}
