import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';

const excluded = new Set([
  '.git',
  '.codex',
  '.ssh',
  '.aws',
  '.azure',
  '.pal',
  'node_modules',
  '.venv',
  'venv',
  'dist',
  'build',
  'coverage',
]);
const maxBytes = 200000;
/** 按路径各级名称排除依赖、生成物和常见凭据文件，避免被文件工具读出。 */
function blocked(rel) {
  return rel
    .split(/[\\/]/)
    .some(
      (p) =>
        excluded.has(p.toLowerCase()) ||
        /^\.env($|\.)|\.(pem|key|p12|pfx)$/i.test(p) ||
        /^(credentials|auth|models)\.json$/i.test(p),
    );
}
/** 用相对路径判断目标是否仍在项目内，不依赖容易误匹配的字符串前缀。 */
function inside(root, candidate) {
  const r = path.relative(root, candidate);
  return r === '' || (!r.startsWith('..' + path.sep) && r !== '..' && !path.isAbsolute(r));
}
/** 为读取的 UTF-8 文本生成版本指纹，应用建议前据此检测并发修改。 */
const hash = (text) => crypto.createHash('sha256').update(text).digest('hex');
export class Workspace {
  /** 保存真实项目根目录和内存建议表；建议不会直接影响磁盘内容。 */
  constructor(root) {
    this.root = root;
    this.proposals = new Map();
  }
  /** 要求绝对目录并解析真实路径，确保后续边界检查使用一致的项目根。 */
  static async open(root) {
    if (!path.isAbsolute(root)) throw new Error('workspace 必须是项目的绝对路径。');
    const actual = await fs.realpath(root);
    if (!(await fs.stat(actual)).isDirectory()) throw new Error('workspace 必须是目录。');
    return new Workspace(actual);
  }
  /** 校验相对路径、凭据规则与符号链接；新文件沿父目录检查，仍不得越过项目边界。 */
  async resolve(relative, allowNew = false) {
    if (
      typeof relative !== 'string' ||
      relative.includes('\0') ||
      relative.includes(':') ||
      path.isAbsolute(relative) ||
      relative.includes('\\')
    )
      throw new Error('请使用项目内相对路径，以 / 分隔。');
    const candidate = path.resolve(this.root, relative || '.');
    const normalized = path.relative(this.root, candidate);
    if (!inside(this.root, candidate) || blocked(normalized))
      throw new Error('路径超出项目或属于排除目录/凭据文件。');
    let probe = candidate;
    while (true) {
      try {
        const actual = await fs.realpath(probe);
        const actualRelative = path.relative(this.root, actual);
        if (!inside(this.root, actual) || blocked(actualRelative))
          throw new Error('符号链接指向项目外或受限文件。');
        break;
      } catch (e) {
        if (e.code !== 'ENOENT' || !allowNew || probe === this.root) throw e;
        probe = path.dirname(probe);
      }
    }
    return candidate;
  }
  /** 最多读取 200 KB 普通文本，同时返回 SHA；读取中再次限流，阻止 stat 后文件增长。 */
  async read(relative) {
    const file = await this.resolve(relative);
    const handle = await fs.open(file, 'r');
    try {
      const info = await handle.stat();
      if (!info.isFile() || info.size > maxBytes)
        throw new Error('只允许读取不超过 200 KB 的普通文本文件。');
      // stat 后文件仍可能增长；实际读取也必须有硬上限。
      const buffer = Buffer.alloc(maxBytes + 1);
      let size = 0;
      while (size < buffer.length) {
        const {bytesRead} = await handle.read(buffer, size, buffer.length - size, null);
        if (!bytesRead) break;
        size += bytesRead;
      }
      if (size > maxBytes) throw new Error('只允许读取不超过 200 KB 的普通文本文件。');
      const bytes = buffer.subarray(0, size);
      if (bytes.includes(0)) throw new Error('不支持二进制文件。');
      const text = bytes.toString('utf8');
      return {path: relative, sha256: hash(text), content: text};
    } finally {
      await handle.close();
    }
  }
  /** 有界遍历目录，最多返回 500 项并跳过符号链接；截断会在结果中明确标记。 */
  async list(relative = '.', recursive = false) {
    const start = await this.resolve(relative);
    const results = [];
    const queue = [start];
    let scanned = 0;
    while (queue.length && results.length < 500 && scanned < 1500) {
      const next = queue.shift();
      const relative = path.relative(this.root, next).split(path.sep).join('/');
      const dir = await this.resolve(relative);
      // 流式遍历，避免一次把大型目录的所有条目读入内存。
      for await (const item of await fs.opendir(dir)) {
        scanned++;
        const rel = path.relative(this.root, path.join(dir, item.name)).split(path.sep).join('/');
        if (!blocked(rel) && !item.isSymbolicLink()) {
          results.push({path: rel, type: item.isDirectory() ? 'directory' : 'file'});
          if (recursive && item.isDirectory()) queue.push(path.join(dir, item.name));
        }
        if (results.length >= 500 || scanned >= 1500) break;
      }
      if (!recursive) break;
    }
    return {entries: results, truncated: results.length >= 500 || scanned >= 1500};
  }
  /** 在允许读取的文件中做忽略大小写的字面搜索，最多返回 80 处匹配及行号。 */
  async search(query, relative = '.') {
    if (typeof query !== 'string' || !query || query.length > 300)
      throw new Error('搜索词须为 1–300 字符的普通文本。');
    const files = await this.list(relative, true);
    const matches = [];
    let count = 0;
    for (const entry of files.entries) {
      if (entry.type !== 'file') continue;
      try {
        const {content} = await this.read(entry.path);
        count++;
        const lines = content.split(/\r?\n/);
        for (let i = 0; i < lines.length; i++)
          if (lines[i].toLowerCase().includes(query.toLowerCase())) {
            matches.push({path: entry.path, line: i + 1, text: lines[i].slice(0, 400)});
            if (matches.length >= 80) return {matches, truncated: true, scannedFiles: count};
          }
      } catch {
        /* unreadable, binary and oversized files are omitted */
      }
    }
    return {matches, truncated: files.truncated, scannedFiles: count};
  }
  /** 校验版本后保存完整文件建议；新文件要求 SHA 为 null，同路径的新建议替换旧建议。 */
  async propose(relative, content, expectedSha256) {
    if (typeof content !== 'string' || Buffer.byteLength(content) > maxBytes)
      throw new Error('修改建议必须是最多 200 KB 的文本。');
    const target = await this.resolve(relative, true);
    if (target === this.root) throw new Error('不能修改项目根目录。');
    let current = null;
    try {
      current = await this.read(relative);
    } catch (e) {
      if (e.code !== 'ENOENT') throw e;
    }
    if ((current?.sha256 ?? null) !== expectedSha256)
      throw new Error('文件版本不匹配，请重新读取。新文件的 expectedSha256 应为 null。');
    const key = path.relative(this.root, target).split(path.sep).join('/');
    if (!this.proposals.has(key) && this.proposals.size >= 20)
      throw new Error('单个任务最多提出 20 个文件修改。');
    const proposal = {path: key, originalSha256: current?.sha256 ?? null, content};
    this.proposals.set(key, proposal);
    return {
      staged: true,
      path: key,
      applied: false,
      message: '建议已保存；项目文件未修改，主 Agent 负责检查和应用。',
    };
  }
  /**
   * 暂存小范围替换，避免模型为一行修改重写整份文件。
   * 每个 oldText 必须在当前工作副本中唯一出现；最终内容仍按原文件 SHA 校验，不直接写盘。
   */
  async proposeEdit(relative, edits, expectedSha256) {
    if (!Array.isArray(edits) || !edits.length || edits.length > 40)
      throw new Error('edits 必须包含 1–40 个精确替换片段。');
    const current = await this.read(relative);
    if (current.sha256 !== expectedSha256) throw new Error('文件版本不匹配，请重新读取。');
    let content = current.content;
    for (const edit of edits) {
      if (!edit || typeof edit.oldText !== 'string' || !edit.oldText || typeof edit.newText !== 'string')
        throw new Error('每个替换需要非空 oldText 和字符串 newText。');
      const index = content.indexOf(edit.oldText);
      if (index < 0 || content.indexOf(edit.oldText, index + 1) !== -1)
        throw new Error('oldText 必须精确且唯一匹配，请补充上下文后重试。');
      content = content.slice(0, index) + edit.newText + content.slice(index + edit.oldText.length);
    }
    const result = await this.propose(relative, content, expectedSha256);
    this.proposals.get(result.path).edits = structuredClone(edits);
    return result;
  }
  /** 调度模型可用的受限文件工具；拒绝未知工具和非对象参数，不提供命令执行能力。 */
  async execute(name, args) {
    if (!args || typeof args !== 'object' || Array.isArray(args))
      throw new Error('工具参数必须为对象。');
    if (name === 'list_files') return this.list(args.path ?? '.', Boolean(args.recursive));
    if (name === 'read_file') return this.read(args.path);
    if (name === 'search_text') return this.search(args.query, args.path ?? '.');
    if (name === 'propose_file') return this.propose(args.path, args.content, args.expectedSha256);
    if (name === 'propose_edit') return this.proposeEdit(args.path, args.edits, args.expectedSha256);
    throw new Error(`未知工具：${String(name).slice(0, 60)}`);
  }
}
/** 构造严格的工具参数对象定义，所有列出的属性均必填且不接受额外字段。 */
const schema = (properties) => ({
  type: 'object',
  properties,
  required: Object.keys(properties),
  additionalProperties: false,
});
export const workspaceTools = [
  {
    name: 'list_files',
    description:
      'List files inside the assigned project. Common dependency, credential and generated directories are excluded.',
    schema: schema({path: {type: 'string'}, recursive: {type: 'boolean'}}),
  },
  {
    name: 'read_file',
    description: 'Read a project text file and its SHA-256. Use a relative path with forward slashes.',
    schema: schema({path: {type: 'string'}}),
  },
  {
    name: 'search_text',
    description:
      'Search literal text, case-insensitively, in project files. Results are bounded; narrow the path when truncated.',
    schema: schema({path: {type: 'string'}, query: {type: 'string'}}),
  },
  {
    name: 'propose_edit',
    description:
      'Prefer for small edits: stage unique oldText/newText replacements against the original SHA. Submit all edits for a file together; a later proposal replaces the earlier one. Never writes files.',
    schema: schema({
      path: {type: 'string'},
      expectedSha256: {type: 'string'},
      edits: {
        type: 'array',
        minItems: 1,
        maxItems: 40,
        items: schema({oldText: {type: 'string'}, newText: {type: 'string'}}),
      },
    }),
  },
  {
    name: 'propose_file',
    description:
      'Stage complete proposed file content for the parent to inspect and apply. Does not modify any project file. Use SHA-256 from read_file, or null for a new file.',
    schema: schema({
      path: {type: 'string'},
      content: {type: 'string'},
      expectedSha256: {type: ['string', 'null']},
    }),
  },
];
