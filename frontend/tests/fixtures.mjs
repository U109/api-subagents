import {clone, defaultContextSettings, makePlan, makeSnapshot} from '../src/domain.mjs';
import {encodeConnection} from '../src/adapter.mjs';

/** 合成无真实凭据的原型对照数据；字段与已批准的模型列表一致，仅供隔离测试。 */
export function fixtureStore(count = 2) {
  const definitions = [
    ['cpa', '日常编码、复杂逻辑分析与多步骤排错。', [['gpt-5-extra', 1000000], ['gpt-5.1-fast-high', 1000000], ['deepseek-v4-flash', 128000], ['claude-4.7-sonnet', 200000], ['gpt-4', 256000], ['gemini-3.1-pro', 64000], ['custom-coder', null, false], ['fast-mini', 128000, false]]],
    ['worker', '边界明确的重复修改、注释和文档整理。', [['gpt-5-mini', 128000], ['fast-worker', null]]],
    ['review', '分析后端逻辑与边界条件，排查异常并进行代码审查。', [['review-model', 200000], ['code-helper', 128000, false]]]
  ];
  const connections = Object.fromEntries(definitions.map(([id, description, models]) => [id, {id, name: id, description, url: 'https://api.example.com/v1', models: models.map(([id, context, selected = true]) => ({id, context, ...defaultContextSettings(context), selected, name: '', image: 'auto', output: ''})), defaultID: models[0][0], effort: 'xhigh', override: false, maxTokens: 4096, firstTimeout: 180, idleTimeout: 120, taskTimeout: 15, response: 'stream'}]));
  const plans = [makePlan('plan-a', '配置 A', ['cpa', 'worker'], connections), makePlan('plan-b', '配置 B', ['cpa', 'review'], connections, {cpa: ['gpt-5-extra', 'claude-4.7-sonnet'], review: ['review-model']})];
  const labels = ['日常开发', '代码审查', '技术文档', '快速问答', '发布检查', '后端排错', '批量修改', '性能分析'];
  for (let i = 2; i < count; i++) plans.push(makePlan(`plan-${i + 1}`, `${labels[(i - 2) % labels.length]} ${String(i + 1).padStart(2, '0')}`, i % 2 ? ['cpa', 'review'] : ['worker'], connections));
  return {connections: clone(connections), savedConnections: connections, plans, savedPlans: Object.fromEntries(plans.map(p => [p.id, clone(p)])), selected: 'plan-a', active: makeSnapshot(plans[0], connections), enabled: true};
}

/** 将合成原型数据编码为真实 API 返回形状，不读取配置文件或机器环境。 */
export function fixtureConfig(count = 2) {
  const store = fixtureStore(count);
  return {version: 1, models: Object.fromEntries(Object.values(store.connections).map(c => [c.name, {...encodeConnection({...c, protocol: 'responses', apiKey: '', hasKey: true}), savedName: c.name}])), accessConfigs: clone(store.plans), lastAccess: clone(store.plans[0])};
}

/** 提供原型等价的合成桌面状态，版本字符串不代表正式发布。 */
export function fixtureState(config = fixtureConfig()) {return {version: '0.0.0-test', relay: {enabled: true, access: clone(config.lastAccess), model: 'cpa'}, plugin: {phase: 'installed', installedVersion: '0.5.2-demo', bundledVersion: '0.5.4-demo'}, pluginUpdate: {phase: 'available', availableVersion: '0.5.4-demo', message: ''}, update: {phase: 'idle', autoCheck: true, version: '0.0.0-test', message: ''}, close: {}};}
