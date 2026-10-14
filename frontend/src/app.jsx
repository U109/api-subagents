import React, {useState, useRef, useEffect, useId} from 'react';
import {createPortal} from 'react-dom';
import {DndContext, DragOverlay, PointerSensor, KeyboardSensor, useSensor, useSensors, pointerWithin, closestCenter} from '@dnd-kit/core';
import {SortableContext, useSortable, sortableKeyboardCoordinates, verticalListSortingStrategy} from '@dnd-kit/sortable';
import {CSS} from '@dnd-kit/utilities';
import {createRoot} from 'react-dom/client';
import * as Dialog from '@radix-ui/react-dialog';
import * as AlertDialog from '@radix-ui/react-alert-dialog';
import * as DropdownMenu from '@radix-ui/react-dropdown-menu';
import * as Tabs from '@radix-ui/react-tabs';
import * as Switch from '@radix-ui/react-switch';
import * as Checkbox from '@radix-ui/react-checkbox';
import * as Tooltip from '@radix-ui/react-tooltip';
import {clsx} from 'clsx';
import {twMerge} from 'tailwind-merge';
import {Layers as LayersIcon, Plug as PlugIcon, Link as LinkIcon, SlidersHorizontal as SlidersHorizontalIcon, GitBranch as GitBranchIcon, Brain as BrainIcon, Plus as PlusIcon, Search as SearchIcon, Check as CheckIcon, X as XIcon, ChevronLeft as ChevronLeftIcon, ChevronRight as ChevronRightIcon, MoreHorizontal as MoreHorizontalIcon, Copy as CopyIcon, Trash2 as Trash2Icon, Star as StarIcon, RefreshCw as RefreshCwIcon, ArrowUp as ArrowUpIcon, ArrowDown as ArrowDownIcon, ArrowRight as ArrowRightIcon, Menu as MenuIcon, Power as PowerIcon, Eye as EyeIcon, EyeOff as EyeOffIcon, Download as DownloadIcon, HelpCircle as HelpCircleIcon, ShieldCheck as ShieldCheckIcon, Settings2 as Settings2Icon, CircleAlert as CircleAlertIcon} from 'lucide-react';
import {PanelsTopLeft as WorkspaceIcon, Puzzle as PuzzleIcon, Cable as CableIcon, Server as ServerIcon, CircleCheck as CircleCheckIcon, PencilLine as PencilLineIcon, Zap as ZapIcon} from 'lucide-react';
import {useWorkbench, updateJob as toUpdateJob} from './controller.jsx';
import {RequestDiagnostics} from './diagnostics.jsx';
import {UsagePage} from './usage-page.jsx';
import {createUsageView} from './usage-domain.mjs';
import {createUsageGate} from './usage-state.mjs';
import {ChartNoAxesCombined as UsageRaw} from 'lucide-react';
import {clone, own, modelKey, makePlan, planModels, choosePlanModel, chooseConnection, validatePlan, isDirty, activatePlan, savePlan, makeSnapshot, deleteConnection, contextSettings, defaultContextSettings, effectiveContext, reorderModels, reorderPlanModels} from './domain.mjs';

/** 图标统一作为装饰，动作名称由按钮文字或 aria-label 提供，避免重复朗读。 */
function decorativeIcon(Icon) {
  /** 保留图标尺寸、填充和样式，仅统一可访问边界。 */
  return function DecorativeIcon(props) {return <Icon strokeWidth={1.75} {...props} aria-hidden="true" focusable="false" />;};
}
const [Layers, Plug, Link, SlidersHorizontal, GitBranch, Brain, Plus, Search, Check, X, ChevronLeft, ChevronRight, MoreHorizontal, Copy, Trash2, Star, RefreshCw, ArrowUp, ArrowDown, ArrowRight, Menu, Power, Eye, EyeOff, Download, HelpCircle, ShieldCheck, Settings2, CircleAlert] = [LayersIcon, PlugIcon, LinkIcon, SlidersHorizontalIcon, GitBranchIcon, BrainIcon, PlusIcon, SearchIcon, CheckIcon, XIcon, ChevronLeftIcon, ChevronRightIcon, MoreHorizontalIcon, CopyIcon, Trash2Icon, StarIcon, RefreshCwIcon, ArrowUpIcon, ArrowDownIcon, ArrowRightIcon, MenuIcon, PowerIcon, EyeIcon, EyeOffIcon, DownloadIcon, HelpCircleIcon, ShieldCheckIcon, Settings2Icon, CircleAlertIcon].map(decorativeIcon);
const [Workspace, Puzzle, Cable, Server, CircleCheck, PencilLine, Zap] = [WorkspaceIcon, PuzzleIcon, CableIcon, ServerIcon, CircleCheckIcon, PencilLineIcon, ZapIcon].map(decorativeIcon);

const UsageIcon = decorativeIcon(UsageRaw);

/** 仅给显式开启 Fast 的模型显示雷电；独立标识提供读屏名称，不根据模型 ID 猜测状态。 */
function ModelFastMark({enabled}) {
  return enabled ? <span className="model-fast-mark" role="img" aria-label="Fast 已开启" title="Fast 已开启"><Zap size={14} fill="currentColor" /></span> : null;
}

/** 不可用模型用红色状态图标代替长错误文字；鼠标悬浮和键盘聚焦都能读取原因，不切换模型勾选。 */
function MissingModelMark() {
  return <Tooltip.Root><Tooltip.Trigger asChild><span className="model-unavailable-mark" role="img" aria-label="连接中已不可用" tabIndex={0} onClick={event => event.preventDefault()}><CircleAlert size={15} /></span></Tooltip.Trigger><Tooltip.Portal><Tooltip.Content className="tooltip" side="top" sideOffset={7}>连接中已不可用，请更新连接的模型选择。<Tooltip.Arrow className="tooltip-arrow" /></Tooltip.Content></Tooltip.Portal></Tooltip.Root>;
}

/** 合并组件变体与 Tailwind 类，避免布局覆盖互相冲突。 */
function cn(...values) { return twMerge(clsx(...values)); }
/** 展示演示容量；未知值保持未知，不冒充规格。 */
function capacity(value) { return value ? value >= 1e6 ? `${value / 1e6}M` : `${value / 1000}K` : '待确认'; }
/** 判断配置保存快照是否等于当前运行快照，不把查看项当作生效项。 */
function isApplied(store, plan) {
  if (!plan || !store.active || store.active.id !== plan.id || store.activeCurrent === false) return false;
  try { return JSON.stringify(makeSnapshot(plan, store.savedConnections, store.connections)) === JSON.stringify(store.active); } catch { return false; }
}
/** 统一按钮的尺寸、反馈、禁用和样式；ref 透传供 Radix 原语管理焦点。 */
function Button({variant = 'secondary', className, children, ...props}) { return <button type="button" className={cn('button', `button-${variant}`, className)} {...props}>{children}</button>; }
/** 为图标操作提供一致命名与键盘可见提示，图形不承担可访问名称。 */
function IconButton({label, children, className, ...props}) {
  return <Tooltip.Root><Tooltip.Trigger asChild><Button variant="ghost" className={cn('icon-button', className)} aria-label={label} {...props}>{children}</Button></Tooltip.Trigger><Tooltip.Portal><Tooltip.Content className="tooltip" sideOffset={6}>{label}<Tooltip.Arrow className="tooltip-arrow" /></Tooltip.Content></Tooltip.Portal></Tooltip.Root>;
}
/** 标签以轻量轮廓图标和文字区分生效、默认与草稿，图标不重复朗读，背景不依赖绿色。 */
function Badge({tone = 'neutral', icon: Icon, children, className}) {
  return <span className={cn('badge', `badge-${tone}`, className)}>{Icon && <Icon size={12} />}{children}</span>;
}
/** 输入的回退字符宽度兼顾中文与拉丁字符；超长名称仍可编辑，不把状态标签推到远端。 */
function nameInputSize(value) {
  return Math.min(32, Math.max(6, Array.from(value).reduce((length, character) => length + (/[^\u0000-\u00ff]/.test(character) ? 2 : 1), 0)));
}
/** 字段关联标签及错误；可选尾部状态紧随输入，仅名称字段启用，不改变其他表单布局。 */
function Field({label, error, multiline, className, suffix, ...props}) {
  const id = useId(), errorID = `${id}-error`, Component = multiline ? 'textarea' : 'input';
  const control = <Component id={id} className={cn('input', multiline && 'textarea')} aria-invalid={Boolean(error)} aria-describedby={error ? errorID : undefined} {...props} />;
  return <div className={cn('field', className)}><label htmlFor={id}>{label}</label>{suffix ? <div className="field-control">{control}{suffix}</div> : control}{error && <small id={errorID} className="field-error">{error}</small>}</div>;
}
/** 搜索控件使用可访问名称，清空操作不重新创建输入框。 */
function SearchField({value, onChange, placeholder, label, className}) {
  return <div className={cn('search-field', className)}><Search size={17} aria-hidden="true" /><input type="search" value={value} onChange={e => onChange(e.target.value)} placeholder={placeholder} aria-label={label || placeholder} />{value && <IconButton label="清空搜索" onClick={() => onChange('')}><X size={15} /></IconButton>}</div>;
}
/** 勾选框采用 Radix 的键盘与半选语义，标签由调用者提供。 */
function Tick({checked, onChange, label, ...props}) {
  return <Checkbox.Root className="checkbox" checked={checked} onCheckedChange={value => onChange(value === true)} aria-label={label} {...props}><Checkbox.Indicator><Check size={14} strokeWidth={2.6} /></Checkbox.Indicator></Checkbox.Root>;
}
/** 开关统一状态与点击边界，不将连接级开关重新引入。 */
function Toggle({checked, onChange, label}) { return <Switch.Root className="switch" checked={checked} onCheckedChange={onChange} aria-label={label}><Switch.Thumb className="switch-thumb" /></Switch.Root>; }
/** 等待环只在可见且离屏之外时旋转；减少动态效果保留静态状态。 */
function Spinner() {
  const ref = useRef(null), onScreen = useRef(true), [visible, setVisible] = useState(!document.hidden);
  useEffect(() => {
    const node = ref.current, observer = new IntersectionObserver(([entry]) => {onScreen.current = entry.isIntersecting; setVisible(entry.isIntersecting && !document.hidden);});
    observer.observe(node);
    /** 切回页面时沿用最后一次离屏状态，避免不可见的等待环继续消耗帧。 */
    const visibility = () => setVisible(!document.hidden && onScreen.current);
    document.addEventListener('visibilitychange', visibility);
    return () => { observer.disconnect(); document.removeEventListener('visibilitychange', visibility); };
  }, []);
  return <span ref={ref} className={cn('spinner', !visible && 'spinner-paused')} aria-hidden="true" />;
}
/** 空状态只给一个下一步，搜索无结果时提供清空入口。 */
function Empty({title, children, action}) { return <div className="empty"><Layers size={25} aria-hidden="true" /><strong>{title}</strong>{children && <p>{children}</p>}{action}</div>; }
/** 页面头与动作共用布局，长标题可换行，操作文字不被挤成竖排。 */
function PageHeading({title, subtitle, children, level = 1}) { const Heading = level === 2 ? 'h2' : 'h1'; return <header className="page-heading"><div><Heading>{title}</Heading>{subtitle && <p>{subtitle}</p>}</div><div className="heading-actions">{children}</div></header>; }
/** 详情面包屑提供返回连接目录入口；窄屏保留返回操作，长连接名不挤压右侧更新按钮。 */
function Breadcrumb({page, connectionName, navigate}) {
  const title = page === 'plans' ? '接入配置' : page === 'plugins' ? '插件管理' : page === 'help' ? '使用帮助' : '我的连接';
  return <div className={cn('breadcrumb', page === 'connection' && 'breadcrumb-detail')}><span>工作区</span><ChevronRight size={14} />{page === 'connection' ? <><button className="breadcrumb-parent" aria-label="返回我的连接" onClick={() => navigate('connections')}><ChevronLeft size={14} />我的连接</button><ChevronRight size={14} /><strong title={connectionName}>{connectionName || '未命名连接'}</strong></> : <strong>{title}</strong>}</div>;
}
/** 面板统一外框与头部；辅助说明只在必须澄清作用域时出现。 */
function Panel({title, icon: Icon, actions, children, className}) { return <section className={cn('panel', className)}>{title && <div className="panel-heading"><div className="panel-title">{Icon && <Icon size={18} aria-hidden="true" />}<h2>{title}</h2></div>{actions}</div>}{children}</section>; }
/** 更多菜单用 Radix 管理定位与键盘；连接目录操作在右端锚点对齐，窄屏由碰撞检测收回视口。 */
function MoreMenu({label, onCopy, onDelete, side = 'bottom', align = 'end'}) {
  return <DropdownMenu.Root><DropdownMenu.Trigger asChild><Button variant="ghost" className="icon-button" aria-label={label}><MoreHorizontal size={19} aria-hidden="true" /></Button></DropdownMenu.Trigger><DropdownMenu.Portal><DropdownMenu.Content className="menu-content" side={side} align={align} sideOffset={8} collisionPadding={12}><DropdownMenu.Item className="menu-item" onSelect={onCopy}><Copy size={16} aria-hidden="true" />复制</DropdownMenu.Item><DropdownMenu.Separator className="menu-separator" /><DropdownMenu.Item className="menu-item destructive" onSelect={onDelete}><Trash2 size={16} aria-hidden="true" />删除</DropdownMenu.Item></DropdownMenu.Content></DropdownMenu.Portal></DropdownMenu.Root>;
}

/** 更新入口共用实际任务状态与等待反馈；打开原有更新弹窗，不在渲染或导航时发起检查。 */
function UpdateTrigger({job, onClick, className}) {
  const loading = job?.phase === 'loading';
  const label = loading ? job.operation === 'checking' ? '正在检查更新' : job.operation === 'downloading' ? '正在下载更新' : '正在准备安装' : job?.phase === 'available' ? '发现更新' : '检查更新';
  return <Button variant="ghost" className={cn('update-trigger', className)} aria-busy={loading} onClick={onClick}>{loading ? <Spinner /> : <RefreshCw size={16} />}<span>{label}</span></Button>;
}

/** 侧栏支持边框拉手收起；各页面保持同一更新与帮助入口位置，图标模式仍保留名称提示。 */
function Sidebar({store, page, navigate, collapsed, toggleCollapsed, version, updateJob, onUpdate}) {
  const navID = useId();
  return <><div className="sidebar-content">
    <div className="sidebar-header"><a className="brand" href="#" aria-label="API Subagents" onClick={e => {e.preventDefault(); navigate('plans');}}><span className="brand-mark"><Layers size={20} /></span><strong>API Subagents</strong></a></div>
    <div className="sidebar-section-label">工作区</div>
    <nav id={navID} className="primary-nav" aria-label="主导航">{[['plans', Workspace, '接入配置'], ['plugins', Puzzle, '插件管理'], ['connections', Cable, '我的连接'], ['usage', UsageIcon, '用量统计']].map(([id, Icon, label]) => {
      const count = id === 'plans' ? store.plans.length : id === 'connections' ? Object.keys(store.connections).length : null;
      return <Tooltip.Root key={id}><Tooltip.Trigger asChild><button type="button" className={cn('nav-button', (page === id || id === 'connections' && page === 'connection') && 'active')} aria-label={count == null ? label : `${label} ${count}`} aria-current={page === id || id === 'connections' && page === 'connection' ? 'page' : undefined} onClick={() => navigate(id)}><Icon size={19} /><span className="nav-caption">{label}</span>{count != null && <span className="nav-count">{count}</span>}</button></Tooltip.Trigger>{collapsed && <Tooltip.Portal><Tooltip.Content className="tooltip" side="right" sideOffset={8}>{label}<Tooltip.Arrow className="tooltip-arrow" /></Tooltip.Content></Tooltip.Portal>}</Tooltip.Root>;
    })}</nav>
    <div className="sidebar-bottom"><UpdateTrigger job={updateJob} onClick={onUpdate} className="sidebar-update" /><Tooltip.Root><Tooltip.Trigger asChild><button type="button" className={cn('nav-button', page === 'help' && 'active')} aria-label="使用帮助" onClick={() => navigate('help')}><HelpCircle size={19} /><span className="nav-caption">使用帮助</span></button></Tooltip.Trigger>{collapsed && <Tooltip.Portal><Tooltip.Content className="tooltip" side="right" sideOffset={8}>使用帮助<Tooltip.Arrow className="tooltip-arrow" /></Tooltip.Content></Tooltip.Portal>}</Tooltip.Root><div className="sidebar-footnote"><span className="status-dot" />本地工作区<span>{version || ''}</span></div></div>
  </div><IconButton label={collapsed ? '展开侧栏' : '收起侧栏'} className="sidebar-collapse-toggle sidebar-edge-toggle" aria-expanded={!collapsed} aria-controls={navID} onClick={toggleCollapsed}>{collapsed ? <ChevronRight size={15} /> : <ChevronLeft size={15} />}</IconButton></>;
}

/** 常驻连接列表与同页编辑器只改变查看对象，独立草稿和当前接入快照保持不变。 */
function ConnectionsWorkspace({store, id, openConnection, addConnection, children}) {
  const all = Object.values(store.connections);
  useEffect(() => {if (!store.connections[id] && all.length) openConnection(all[0].id);}, [id, store.connections, openConnection]);
  if (!all.length) return <><PageHeading title="我的连接" /><Empty title="还没有连接" action={<Button onClick={addConnection}>添加连接</Button>} /></>;
  return <>
    <PageHeading title="我的连接"><Button onClick={addConnection}><Plus size={17} />添加连接</Button></PageHeading>
    <div className="connection-workbench same-page-workbench">
      <aside className="panel connection-workbench-list" aria-label="连接目录">
        <div className="connection-list-heading"><h2>全部连接</h2><span aria-live="polite">{all.length}</span></div>
        <div className="connection-list-items">{all.map(c => <article key={c.id} className="connection-directory-row" data-directory-connection={c.id}>
          <button className="connection-directory-open" aria-label={`管理连接 ${c.name}`} aria-pressed={c.id === id} onClick={() => openConnection(c.id)}>
            <span className="directory-provider-icon" aria-hidden="true"><Server size={19} /></span>
            <span className="directory-provider-copy"><strong>{c.name || '未命名连接'}</strong><span>{c.models.filter(m => m.selected).length} 个模型</span>{isDirty(c, store.savedConnections[c.id]) && <span className="connection-list-draft">未保存</span>}</span>
          </button>
        </article>)}</div>
        {!all.length && <Empty title="还没有连接" action={<Button onClick={addConnection}>添加连接</Button>} />}
      </aside>
      <section className="panel connection-workbench-editor" aria-label="连接编辑器">{store.connections[id] ? children : <Empty title="选择或添加一个连接" action={<Button onClick={addConnection}>添加连接</Button>} />}</section>
    </div>
  </>;
}

/** 接入摘要始终来自运行快照；独立文字标明开关状态，关闭或恢复不会把当前查看项当作生效项。 */
function AccessStatus({store, toggle, locate}) {
  const active = store.active;
  return <div className={cn('access-status', store.enabled && 'access-on')}>
    <div className="access-status-icon"><Power size={20} /></div>
    <div className="access-status-copy">
      <strong>主对话接入{active && <button className="inline-link" onClick={locate}>{active.name}<ChevronRight size={13} /></button>}</strong>
      <span>{active ? `${active.connectionIDs.map(id => active.models.find(m => m.connectionID === id)?.connectionName || id).join(' + ')} · ${active.models.length} 个模型` : '选择配置并启用后，模型才会出现在 Codex 中'}</span>
    </div>
    <div className="access-status-control"><span className="access-state-label">{store.enabled ? '已开启' : '已关闭'}</span><Toggle label="主对话接入" checked={store.enabled} onChange={toggle} /></div>
  </div>;
}

/** 配置列表用选中底色区分查看项、文字标记区分启用项；只切换编辑对象，不隐式保存或启用。 */
function PlanList({store, select, create}) {
  return <aside className="plan-list panel" aria-label="接入配置列表">
    <div className="plan-list-heading"><h2>全部配置</h2><span>{store.plans.length}</span></div>
    <ul className="plan-list-items">{store.plans.map(p => <li key={p.id}>
      <button data-plan-id={p.id} className={cn('plan-list-item', store.selected === p.id && 'selected')} onClick={() => select(p.id)} aria-pressed={store.selected === p.id}>
        <div className="plan-list-title"><strong title={p.name}>{p.name}</strong>{store.enabled && store.active?.id === p.id && <Badge tone="active" icon={CircleCheck}>使用中</Badge>}{isDirty(p, store.savedPlans[p.id]) && <span className="draft-dot" title="未保存" aria-label="未保存" />}</div>
        <span className="plan-list-connections" title={p.connectionIDs.map(cid => store.savedConnections[cid]?.name || cid).join(' + ') || '未添加连接'}>{p.connectionIDs.map(cid => store.savedConnections[cid]?.name || cid).join(' + ') || '未添加连接'}</span>
        <span className="plan-list-meta">{planModels(p, store.savedConnections).length} 个模型</span>
      </button>
    </li>)}</ul>
    {!store.plans.length && <Empty title="还没有接入配置" action={<Button onClick={create}>新建配置</Button>} />}
  </aside>;
}

/** 已加入连接只展示当前配置成员；添加菜单列出其余连接，移除仅清理本配置引用，不删除原始连接。 */
function PlanConnections({plan, store, edit, openConnection}) {
  const candidates = Object.values(store.connections).filter(connection => !plan.connectionIDs.includes(connection.id));
  return <section className="connection-members">
    <div className="section-line"><h2>包含连接</h2><div className="member-section-actions">
      <span className="member-count" aria-live="polite" aria-atomic="true">{plan.connectionIDs.length} 个连接</span>
      <DropdownMenu.Root><DropdownMenu.Trigger asChild><Button className="add-plan-connection"><Plus size={14} />添加连接</Button></DropdownMenu.Trigger>
        <DropdownMenu.Portal><DropdownMenu.Content className="menu-content connection-picker-menu" align="end" sideOffset={6} collisionPadding={12}>
          <DropdownMenu.Label className="connection-picker-label">添加已有连接</DropdownMenu.Label>
          {candidates.map(connection => {
            const saved = own(store.savedConnections, connection.id), unavailable = !saved || isDirty(connection, saved);
            return <DropdownMenu.Item key={connection.id} className="menu-item connection-picker-item" disabled={unavailable} onSelect={() => edit(next => chooseConnection(next, connection.id, true, store.savedConnections))}>
              <Server size={16} /><span><strong>{connection.name}</strong><small>{unavailable ? '请先保存此连接' : `${saved.models.filter(model => model.selected).length} 个可用模型`}</small></span><Plus size={14} />
            </DropdownMenu.Item>;
          })}
          {!candidates.length && <p className="connection-picker-empty">{Object.keys(store.connections).length ? '所有连接都已加入此配置' : '请先在「我的连接」中保存连接'}</p>}
        </DropdownMenu.Content></DropdownMenu.Portal>
      </DropdownMenu.Root>
    </div></div>
    <div className="member-grid">{plan.connectionIDs.map(id => {
      const connection = own(store.connections, id), saved = own(store.savedConnections, id), name = connection?.name || saved?.name || id, unsaved = connection && isDirty(connection, saved);
      return <div key={id} className="member-item member-selected" data-plan-connection={id}>
        <span className="member-symbol"><Server size={17} /></span>
        <div className="member-copy"><strong>{name}</strong>{!connection ? <small className="field-error">连接已不可用</small> : unsaved ? <small className="member-warning">未保存修改</small> : <span className="member-availability">{saved?.models.filter(model => model.selected).length || 0} 个可用模型</span>}</div>
        <div className="member-actions"><IconButton label={`管理连接 ${name}`} disabled={!connection} onClick={() => openConnection(id)}><Settings2 size={15} /></IconButton><IconButton label={`移除连接 ${name}`} onClick={() => edit(next => chooseConnection(next, id, false, store.savedConnections))}><X size={14} /></IconButton></div>
      </div>;
    })}</div>
    {!plan.connectionIDs.length && <p className="member-empty">点击「添加连接」，选择此配置要使用的连接。</p>}
  </section>;
}

/** 可见已选模型提供菜单上移与下移，作为拖动的单指针替代；过滤排序不会移动隐藏模型。 */
function ScopeOrderMenu({name, models, move}) {
  return <DropdownMenu.Root><DropdownMenu.Trigger asChild><Button variant="ghost" className="icon-button scope-order-menu-trigger" aria-label={`调整 ${name} 的模型顺序`} disabled={models.length < 2} aria-description={models.length < 2 ? '至少需要两个符合筛选条件的已选模型。' : undefined}><MoreHorizontal size={15} /></Button></DropdownMenu.Trigger>
    <DropdownMenu.Portal><DropdownMenu.Content className="menu-content scope-order-menu" align="end" sideOffset={6} collisionPadding={12}>
      <DropdownMenu.Label className="connection-picker-label">调整已选模型顺序</DropdownMenu.Label>
      {models.map((model, index) => <DropdownMenu.Group key={model.id} className="scope-order-menu-row"><DropdownMenu.Label className="order-model-name">{model.name || model.id}</DropdownMenu.Label>
        <DropdownMenu.Item className="menu-item scope-order-action" aria-label={`上移 ${name} / ${model.id}`} title={`上移 ${model.name || model.id}`} disabled={!index} onSelect={() => move(model.id, models[index - 1].id)}><ArrowUp size={15} /></DropdownMenu.Item>
        <DropdownMenu.Item className="menu-item scope-order-action" aria-label={`下移 ${name} / ${model.id}`} title={`下移 ${model.name || model.id}`} disabled={index === models.length - 1} onSelect={() => move(model.id, models[index + 1].id)}><ArrowDown size={15} /></DropdownMenu.Item>
      </DropdownMenu.Group>)}
    </DropdownMenu.Content></DropdownMenu.Portal>
  </DropdownMenu.Root>;
}

/** 紧凑模型行只从左侧手柄排序已选可用模型；勾选、默认星标与排序互不触发，缺失模型仍可取消。 */
function ScopeModelRow({plan, cid, name, model, edit}) {
  const key = modelKey(cid, model.id), checked = (plan.modelIDs[cid] ?? []).includes(model.id), isDefault = plan.defaultKey === key, checkboxID = `scope-${encodeURIComponent(plan.id)}-${encodeURIComponent(key)}`;
  const {attributes, listeners, setNodeRef, setActivatorNodeRef, transform, isDragging, isOver} = useSortable({id: model.id, transition: null, disabled: !checked || Boolean(model.missing)});
  return <div ref={setNodeRef} role="listitem" data-scoped-model={key} style={{transform: CSS.Transform.toString(transform)}} className={cn('scope-model-row', 'scope-sort-row', isDefault && 'is-default', model.missing && 'missing-model', isDragging && 'is-dragging', isOver && !isDragging && 'is-drop-target')}>
    <Button ref={setActivatorNodeRef} variant="ghost" className="icon-button scope-drag-handle" {...attributes} {...listeners} disabled={!checked || model.missing} aria-label={`拖动排序 ${name} / ${model.id}`} aria-description="仅调整此配置的已选模型顺序。空格抓取，方向键移动，空格放下，Escape 取消。"><Menu size={13} /></Button>
    <Tick id={checkboxID} checked={checked} label={`配置中选择 ${name} / ${model.id}`} onChange={value => edit(next => choosePlanModel(next, cid, model.id, value))} disabled={Boolean(model.missing && !checked)} />
    <label htmlFor={checkboxID} className="scope-model-name"><span className="model-id" title={model.id}>{model.name || model.id}</span>{model.missing ? <MissingModelMark /> : isDefault && <small className="default-caption">配置默认</small>}<ModelFastMark enabled={!model.missing && model.fast} /></label>
    <span className={cn('capacity', !effectiveContext(model) && 'is-unconfirmed')}>{capacity(effectiveContext(model))}</span>
    <IconButton className={cn('star-button', isDefault && 'is-starred')} label={`${isDefault ? '此配置默认模型' : '设为此配置默认'} ${name} / ${model.id}`} aria-pressed={isDefault} disabled={!checked || model.missing} onClick={() => edit(next => ({...next, defaultKey: key}))}><Star size={17} fill={isDefault ? 'currentColor' : 'none'} /></IconButton>
  </div>;
}

/** 每个连接独立折叠和排序；搜索展开匹配内容，取消拖动不写草稿，隐藏或未选模型不参与重排。 */
function ScopeModelGroup({plan, cid, store, edit, query, selectedOnly}) {
  const [collapsed, setCollapsed] = useState(false), [dragging, setDragging] = useState(null), contentID = useId();
  const connection = own(store.savedConnections, cid), name = connection?.name || cid, ids = plan.modelIDs[cid] ?? [], available = connection?.models.filter(model => model.selected) ?? [];
  const selectedModels = ids.map(id => available.find(model => model.id === id)).filter(Boolean);
  let slot = 0;
  const ordered = available.map(model => ids.includes(model.id) ? selectedModels[slot++] : model), missing = ids.filter(id => !available.some(model => model.id === id)).map(id => ({id, context: null, missing: true}));
  const visible = [...ordered, ...missing].filter(model => (!selectedOnly || ids.includes(model.id)) && `${model.id} ${model.name || ''}`.toLowerCase().includes(query.trim().toLowerCase()));
  const sortableModels = visible.filter(model => ids.includes(model.id) && !model.missing), sortableIDs = sortableModels.map(model => model.id), draggedModel = visible.find(model => model.id === dragging?.id);
  const sensors = useSensors(useSensor(PointerSensor, {activationConstraint: {distance: 6}}), useSensor(KeyboardSensor, {coordinateGetter: sortableKeyboardCoordinates, scrollBehavior: 'auto'}));
  useEffect(() => {if (query.trim()) setCollapsed(false);}, [query]);
  /** 所有排序入口只更新本配置可见已选项；非法或消失的落点由纯函数拒绝。 */
  function move(activeID, overID) {edit(next => reorderPlanModels(next, cid, activeID, overID, sortableIDs));}
  /** 清除拖动状态后再提交有效落点；列表外放下和取消均保留原顺序。 */
  function finishDrag({active, over}) {setDragging(null); if (over) move(active.id, over.id);}
  /** 收起分组同时结束局部拖动，不遗留浮动预览或不可见的键盘抓取状态。 */
  function toggleGroup() {setDragging(null); setCollapsed(value => !value);}
  return <section className="scope-group" aria-label={`${name} 的配置模型`}>
    <div className="scope-group-head"><Button variant="ghost" className="scope-group-toggle" aria-label={`${collapsed ? '展开' : '收起'} ${name} 的配置模型`} aria-expanded={!collapsed} aria-controls={contentID} onClick={toggleGroup}><ChevronRight size={13} /><strong>{name}</strong><span className="scope-group-count">{ids.length} / {available.length}</span></Button>
      <div className="scope-bulk"><Button variant="ghost" disabled={!visible.some(model => !model.missing)} onClick={() => edit(next => visible.filter(model => !model.missing).reduce((result, model) => choosePlanModel(result, cid, model.id, true), next))}>{query ? '选中结果' : '全选'}</Button><Button variant="ghost" disabled={!visible.some(model => ids.includes(model.id))} onClick={() => edit(next => visible.reduce((result, model) => choosePlanModel(result, cid, model.id, false), next))}>{query ? '清空结果' : '清空'}</Button><ScopeOrderMenu name={name} models={sortableModels} move={move} /></div>
    </div>
    <div id={contentID} className="scope-group-content" hidden={collapsed}>{!collapsed && <DndContext sensors={sensors} collisionDetection={modelCollision} modifiers={[verticalDrag]} accessibility={{announcements: modelDragAnnouncements, screenReaderInstructions: {draggable: '只拖动已选模型。按空格抓取，方向键调整顺序，空格放下，Escape 取消。也可使用分组右侧的顺序菜单。'}}} onDragStart={({active}) => setDragging({id: active.id, width: active.rect.current.initial?.width})} onDragEnd={finishDrag} onDragCancel={() => setDragging(null)}>
      <SortableContext items={sortableIDs} strategy={verticalListSortingStrategy}><div className="scope-model-grid" role="list" aria-label={`${name} 的配置模型列表`}>{visible.map(model => <ScopeModelRow key={model.id} plan={plan} cid={cid} name={name} model={model} edit={edit} />)}</div></SortableContext>
      {!visible.length && <div className="group-empty">{selectedOnly ? '此连接没有符合条件的已选模型' : '没有匹配的模型'}</div>}
      {createPortal(<DragOverlay transition="none" dropAnimation={null} zIndex={20}>{draggedModel && <div className="model-drag-preview scope-drag-preview" style={{width: dragging.width}} aria-hidden="true"><Menu size={13} /><span className="model-id">{draggedModel.name || draggedModel.id}</span></div>}</DragOverlay>, document.body)}
    </DndContext>}</div>
  </section>;
}

/** 模型预览保留搜索和已选筛选；紧凑连接分组的勾选、折叠、排序与默认星标不修改连接目录。 */
function ScopedModels({plan, store, edit}) {
  const [query, setQuery] = useState(''), [selectedOnly, setSelectedOnly] = useState(false);
  const selected = planModels(plan, store.savedConnections), total = plan.connectionIDs.reduce((n, cid) => n + (own(store.savedConnections, cid)?.models.filter(m => m.selected).length || 0), 0);
  return <section className="scoped-models" aria-label="Codex 模型预览"><div className="section-line"><h2>Codex 模型预览</h2><span className="model-count" aria-live="polite">已选 {selected.length} / {total}</span></div><div className="scoped-toolbar"><SearchField value={query} onChange={setQuery} placeholder="搜索模型名称或 ID" /><div className="segmented" role="group" aria-label="配置模型筛选"><button className={!selectedOnly ? 'selected' : ''} aria-pressed={!selectedOnly} onClick={() => setSelectedOnly(false)}>全部</button><button className={selectedOnly ? 'selected' : ''} aria-pressed={selectedOnly} onClick={() => setSelectedOnly(true)}>已选</button></div></div>{!plan.connectionIDs.length ? <Empty title="先添加一个连接" /> : plan.connectionIDs.map(cid => <ScopeModelGroup key={cid} plan={plan} cid={cid} store={store} edit={edit} query={query} selectedOnly={selectedOnly} />)}</section>;
}

/** 配置底栏只展示保存状态；校验失败在用户尝试保存或启用时弹窗说明，不在编辑过程中反复打断。 */
function PlanEditor({store, edit, save, apply, restore, copy, remove, back, openConnection, create, busy = false, pending}) {
  const p = store.plans.find(p => p.id === store.selected);
  if (!p) return <section className="plan-editor panel"><Button variant="ghost" className="back-to-plans" onClick={back}><ChevronLeft size={17} />全部配置</Button><Empty title="还没有接入配置" action={<Button onClick={create}><Plus size={16} />新建配置</Button>}>{store.actionError && <span className="field-error" role="status">{store.actionError.message}</span>}</Empty></section>;
  const dirty = isDirty(p, store.savedPlans[p.id]), applied = store.enabled && isApplied(store, p), validation = validatePlan(p, store.savedConnections, store.connections), error = store.actionError?.target === p.id ? store.actionError.message : '';
  const feedback = error === '请星标一个已选模型作为此配置的默认模型。' ? '' : error;
  const saving = pending?.target === p.id && pending.kind === 'plan-save', enabling = pending?.target === p.id && pending.kind === 'plan-enable';
  const saveStatus = dirty ? '有未保存的修改' : applied ? '已与当前接入同步' : store.enabled && store.active?.id === p.id ? '已保存 · 等待更新接入' : '配置已保存';
  return <section className="plan-editor panel" aria-label="配置编辑器"><div className="editor-title-line"><div><Button variant="ghost" className="back-to-plans" onClick={back}><ChevronLeft size={17} />全部配置</Button><h2 className="sr-only">编辑接入配置</h2><Field className="editor-name-field" label="配置名称" title="点击编辑配置名称" size={nameInputSize(p.name)} maxLength={60} value={p.name} onChange={e => edit(next => ({...next, name: e.target.value}))} error={!p.name.trim() ? '请填写配置名称。' : ''} suffix={applied ? <Badge tone="active" icon={CircleCheck}>当前使用</Badge> : dirty ? <Badge tone="warning" icon={PencilLine}>未保存</Badge> : <Badge>未启用</Badge>} /></div><div className="editor-item-actions"><IconButton label={`复制配置 ${p.name}`} onClick={copy}><Copy size={17} /></IconButton><IconButton label={`删除配置 ${p.name}`} className="icon-delete" disabled={busy} onClick={remove}><Trash2 size={17} /></IconButton></div></div><div className="editor-content"><PlanConnections plan={p} store={store} edit={edit} openConnection={openConnection} /><ScopedModels key={p.id} plan={p} store={store} edit={edit} /></div><footer className="editor-footer">{feedback ? <div className="save-message field-error" role="alert">{feedback}</div> : <div className={cn('editor-save-status', dirty && 'is-dirty')} role="status">{dirty ? <PencilLine size={14} /> : <CircleCheck size={14} />}<span>{saveStatus}</span></div>}<div className="save-actions"><Button variant="ghost" disabled={busy || !dirty} onClick={restore}>还原</Button><Button disabled={busy || !dirty} aria-busy={saving} aria-description={validation || undefined} onClick={save}>{saving && <Spinner />}{saving ? '正在保存' : '仅保存'}</Button><Button variant="primary" className={cn(applied && 'button-applied')} disabled={busy || applied} aria-busy={enabling} aria-description={validation || undefined} onClick={apply}>{enabling ? <Spinner /> : applied ? <Check size={16} /> : <Power size={16} />}{enabling ? '正在启用' : applied ? '正在使用' : dirty ? store.enabled && store.active?.id === p.id ? '保存并更新接入' : '保存并启用' : '启用此配置'}</Button></div></footer></section>;
}

/** 配置页采用连贯的主从工作区；头部只新建配置，窄屏让目录与详情纵向排布，业务操作沿用原控制器。 */
function PlanWorkspace(props) {
  const [mobileEditing, setMobileEditing] = useState(false);
  /** 选择只切换右侧对象，独立草稿与当前接入快照不变。 */
  function select(id) {props.select(id); setMobileEditing(true);}
  /** 新建空配置由原有表单处理，不自动保存或启用。 */
  function create() {props.create(); setMobileEditing(true);}
  /** 定位当前生效配置，并让常驻列表中的对应条目保持可见。 */
  function locate() {if (props.store.active) {select(props.store.active.id); requestAnimationFrame(() => Array.from(document.querySelectorAll('[data-plan-id]')).find(node => node.dataset.planId === props.store.active.id)?.scrollIntoView({block: 'nearest'}));}}
  return <><div className="plan-page-intro"><PageHeading title="接入配置"><Button onClick={create}><Plus size={17} />新建配置</Button></PageHeading><p>管理 Codex 主对话使用的连接与模型。</p></div><AccessStatus store={props.store} toggle={props.toggle} locate={locate} /><div className={cn('plan-workspace same-page-workbench mac-plan-workspace', mobileEditing ? 'mobile-editing' : 'mobile-list')}><PlanList store={props.store} select={select} create={create} /><PlanEditor {...props} create={create} copy={props.copy} back={() => setMobileEditing(false)} /></div></>;
}

/** 模型行复用接入配置的三横线手柄启动鼠标或键盘排序；勾选、设置和默认操作互不触发，不再展示更多操作菜单。 */
function ConnectionModelRow({c, model: m, change, editModel}) {
  const isDefault = m.id === c.defaultID;
  const {attributes, listeners, setNodeRef, setActivatorNodeRef, transform, isDragging, isOver} = useSortable({id: m.id, transition: null});
  return <div ref={setNodeRef} role="listitem" style={{transform: CSS.Transform.toString(transform)}} className={cn('connection-model-row', isDefault && 'is-default', isDragging && 'is-dragging', isOver && !isDragging && 'is-drop-target')} data-connection-model={m.id}>
    <Button variant="ghost" ref={setActivatorNodeRef} className="icon-button drag-handle" {...attributes} {...listeners} aria-label={`拖动排序 ${m.id}`}><Menu size={13} /></Button>
    <Tick checked={m.selected} label={`连接中选择模型 ${m.id}`} onChange={selected => change(next => {const model = next.models.find(item => item.id === m.id); model.selected = selected; if (!selected && next.defaultID === m.id) next.defaultID = next.models.find(item => item.selected)?.id || ''; return next;})} />
    <div className="connection-model-name"><span className="model-id">{m.name || m.id}</span>{m.name && <small>{m.id}</small>}{isDefault && <small className="default-caption">默认</small>}<ModelFastMark enabled={m.fast} /></div>
    <div className="model-row-actions">
      <IconButton label={`模型设置 ${m.id}`} onClick={() => editModel(m.id)}><SlidersHorizontal size={16} /></IconButton>
      <IconButton label={`设为连接默认 ${m.id}`} disabled={!m.selected} aria-pressed={isDefault} className={cn('star-button', isDefault && 'is-starred')} onClick={() => change(next => ({...next, defaultID: m.id}))}><Star size={17} fill={isDefault ? 'currentColor' : 'none'} /></IconButton>
    </div>
  </div>;
}

/** 鼠标必须落在可见模型行内才接受排序，键盘则采用库提供的最近落点算法。 */
function modelCollision(args) {return args.pointerCoordinates ? pointerWithin(args) : closestCenter(args);}
/** 拖动仅在垂直方向跟随指针，不引入位置或布局属性的补间动画。 */
function verticalDrag({transform}) {return {...transform, x: 0};}

/** 为读屏提供中文排序反馈；实际状态只在成功放下时修改，取消不保存。 */
const modelDragAnnouncements = {
  /** 开始时朗读模型身份，避免只输出不可识别的拖动状态。 */
  onDragStart({active}) {return `已抓取模型 ${active.id}。`;},
  /** 落点变化时朗读参照模型，离开列表则明确不接受放下。 */
  onDragOver({active, over}) {return over ? `${active.id} 已移到 ${over.id} 的位置。` : '已离开模型列表，放下将保持原顺序。';},
  /** 结束时区分有效排序与列表外取消，不宣称未发生的修改。 */
  onDragEnd({active, over}) {return over ? `已放下模型 ${active.id}。` : '未改变模型顺序。';},
  /** Escape 取消不会提交任何模型修改。 */
  onDragCancel() {return '已取消排序，原顺序不变。';}
};

/** 模型目录仅用手柄排序可见结果，隐藏项留在原槽位；刷新只在按钮展示进度，并保留失败反馈。 */
function ConnectionModels({c, change, openModel, move, refresh, job}) {
  const [query, setQuery] = useState(''), [onlySelected, setOnlySelected] = useState(false), [dragging, setDragging] = useState(null);
  const visible = c.models.filter(m => (!onlySelected || m.selected) && `${m.id} ${m.name || ''}`.toLowerCase().includes(query.trim().toLowerCase())), visibleIDs = visible.map(m => m.id);
  const busy = job?.phase === 'loading', draggedModel = c.models.find(m => m.id === dragging?.id);
  const sensors = useSensors(useSensor(PointerSensor, {activationConstraint: {distance: 6}}), useSensor(KeyboardSensor, {coordinateGetter: sortableKeyboardCoordinates, scrollBehavior: 'auto'}));
  /** 只有有效的可见落点才提交新顺序，取消或列表外放下不产生草稿。 */
  function finishDrag({active, over}) {setDragging(null); if (over) move(active.id, over.id, visibleIDs);}
  return <Panel title="模型目录" icon={Brain} actions={<Badge>{c.models.filter(m => m.selected).length} 已选</Badge>}>
    <div className="connection-model-toolbar"><SearchField value={query} onChange={setQuery} placeholder="搜索模型名称或 ID" /><div className="segmented" role="group" aria-label="连接模型筛选"><button className={!onlySelected ? 'selected' : ''} aria-pressed={!onlySelected} onClick={() => setOnlySelected(false)}>全部</button><button className={onlySelected ? 'selected' : ''} aria-pressed={onlySelected} onClick={() => setOnlySelected(true)}>已选</button></div><Button disabled={busy} aria-busy={busy} onClick={refresh}>{busy ? <Spinner /> : <RefreshCw size={16} />}{busy ? '正在刷新' : '刷新'}</Button><Button onClick={() => openModel(null)}><Plus size={16} />手动添加</Button></div>
    {job?.phase === 'error' && <div className="job-inline field-error" role="alert">{job.label}</div>}
    <DndContext sensors={sensors} collisionDetection={modelCollision} modifiers={[verticalDrag]} accessibility={{announcements: modelDragAnnouncements, screenReaderInstructions: {draggable: '按空格或回车抓取模型，使用上下方向键调整位置，再按空格或回车放下；Escape 取消。'}}} onDragStart={({active}) => setDragging({id: active.id, width: active.rect.current.initial?.width})} onDragEnd={finishDrag} onDragCancel={() => setDragging(null)}>
      <div className="connection-model-table"><div className="model-table-head" aria-hidden="true"><span>模型名称</span><span>操作</span></div><SortableContext items={visibleIDs} strategy={verticalListSortingStrategy}><div role="list" aria-label="连接模型目录">{visible.map(m => <ConnectionModelRow key={m.id} c={c} model={m} change={change} editModel={openModel} />)}</div></SortableContext>{!visible.length && <Empty title="没有匹配的模型" action={<Button onClick={() => {setQuery(''); setOnlySelected(false);}}>显示全部模型</Button>} />}</div>
      {createPortal(<DragOverlay transition="none" dropAnimation={null} zIndex={20}>{draggedModel && <div className="model-drag-preview" style={{width: dragging.width}} aria-hidden="true"><Menu size={13} /><span className="model-id">{draggedModel.name || draggedModel.id}</span>{draggedModel.id === c.defaultID && <small className="default-caption">默认</small>}</div>}</DragOverlay>, document.body)}
    </DndContext>
    <div className="reasoning-section"><div className="section-line"><h2>思考强度</h2><span className="muted">{c.effort === 'default' ? '服务默认' : c.effort}</span></div><div className="effort-options" role="group" aria-label="思考强度">{[['default', '默认'], ['none', '无'], ['minimal', '极低'], ['low', '低'], ['medium', '中'], ['high', '高'], ['xhigh', '很高'], ['max', '最大']].map(([value, label]) => <button key={value} className={cn(c.effort === value && 'selected')} aria-pressed={c.effort === value} onClick={() => change(next => ({...next, effort: value}))}>{label}</button>)}</div></div>
  </Panel>;
}

/** 服务连接复用原型的全宽字段；脱敏凭据只显示占位符，显式新输入由固定配置 API 保存。 */
function ConnectionInfo({c, change, test, cancel, job, errors}) {
  const [visible, setVisible] = useState(false), busy = job?.phase === 'loading';
  return <Panel title="服务连接" icon={Link} actions={<div className="panel-inline-actions"><Button disabled={busy} aria-busy={busy} onClick={test}>{busy ? <Spinner /> : <RefreshCw size={16} />}{busy ? '正在测试' : '测试连接'}</Button>{busy && <Button variant="ghost" onClick={cancel}>取消</Button>}</div>}><div className="panel-body"><div className="field-grid"><Field label="连接名称" maxLength={60} value={c.name} error={errors.name} onChange={e => change(next => ({...next, name: e.target.value}))} /><Field label="API 地址" value={c.url} className="mono-input" error={errors.url} onChange={e => change(next => ({...next, url: e.target.value}))} /></div><div className="section-divider" /><div className="section-line"><h2>身份验证</h2><Badge icon={ShieldCheck}>本机凭据</Badge></div><div className="key-field"><Field label="API Key" type={visible ? 'text' : 'password'} autoComplete="new-password" value={c.apiKey} placeholder={c.hasKey ? '已保存，留空保持不变' : '输入 API Key'} onChange={e => change(next => ({...next, apiKey: e.target.value}))} /><IconButton label={visible ? '隐藏 Key' : '显示 Key'} onClick={() => setVisible(!visible)}>{visible ? <EyeOff size={17} /> : <Eye size={17} />}</IconButton></div>{c.apiKeyEnv && <Field label="Key 环境变量" value={c.apiKeyEnv} onChange={e => change(next => ({...next, apiKeyEnv: e.target.value}))} />}{job && <div className={cn('job-inline', job.phase === 'error' && 'field-error')} role="status">{job.label}</div>}</div></Panel>;
}

/** 任务分工只在文本框下显示委派参考边界，快捷填写不会发起任务。 */
function ConnectionPurpose({c, change}) {
  return <Panel title="擅长与用途" icon={GitBranch}><div className="panel-body"><Field label="任务说明" multiline rows={6} maxLength={300} value={c.description} onChange={e => change(next => ({...next, description: e.target.value}))} /><div className="purpose-caption"><span>帮助 Codex 在插件委派时选择连接，作为委派参考，不替代明确指令。</span><span>{c.description.length} / 300</span></div><div className="purpose-examples"><span>快捷填入</span>{[['代码审查', '分析后端逻辑与边界条件，排查异常并进行代码审查。'], ['文档整理', '梳理需求与现有代码，编写清晰、准确的技术文档。'], ['批量修改', '处理边界明确的重复修改，并使用本地模拟接口验证变更。']].map(([label, text]) => <Button key={label} className={cn(c.description === text && 'example-applied')} aria-pressed={c.description === text} onClick={() => change(next => ({...next, description: text}))}>{c.description === text ? <Check size={15} /> : <Plus size={15} />}{label}</Button>)}</div></div></Panel>;
}

/** 高级参数使用与连接信息一致的面板，把时间范围写进标签；关闭覆盖不丢输入。 */
function ConnectionAdvanced({c, change, reset, errors}) {
  return <Panel title="运行与响应" icon={SlidersHorizontal} actions={<Button variant="ghost" onClick={reset}>恢复默认</Button>}><div className="panel-body"><div className="override-row"><div><strong>自定义参数覆盖</strong><Badge tone={c.override ? 'active' : 'neutral'} icon={c.override ? CircleCheck : undefined}>{c.override ? '已开启' : '未开启'}</Badge></div><Toggle checked={c.override} label="自定义参数覆盖" onChange={override => change(next => ({...next, override}))} /></div><fieldset disabled={!c.override}><legend className="sr-only">运行参数</legend><div className="section-divider" /><div className="section-line"><h2>输出与响应</h2></div><Field label="插件最大输出长度（128–32,000 tokens）" type="number" min={128} max={32000} value={c.maxTokens} error={errors.maxTokens} onChange={e => change(next => ({...next, maxTokens: e.target.value}))} /><div className="radio-options"><label><input type="radio" name="response" checked={c.response === 'stream'} onChange={() => change(next => ({...next, response: 'stream'}))} />流式响应<Badge>默认</Badge></label><label><input type="radio" name="response" checked={c.response === 'complete'} onChange={() => change(next => ({...next, response: 'complete'}))} />完整响应</label></div><div className="section-divider" /><div className="section-line"><h2>超时保护</h2></div><div className="timeout-grid">{[['firstTimeout', '首个数据等待（10–600 秒）', 10, 600], ['idleTimeout', '响应中断等待（10–600 秒）', 10, 600], ['taskTimeout', '任务总时长（1–60 分钟）', 1, 60]].map(([key, label, min, max]) => <Field key={key} label={label} type="number" min={min} max={max} value={c[key]} error={errors[key]} onChange={e => change(next => ({...next, [key]: e.target.value}))} />)}</div></fieldset></div></Panel>;
}

/** 校验连接与启用中的覆盖值，返回各字段错误，不把失败信息只留在 toast。 */
function connectionErrors(c) {
  const errors = {};
  if (!c.name.trim()) errors.name = '请填写连接名称。';
  try {const url = new URL(c.url); if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) errors.url = '请使用不含凭据的 HTTP(S) 地址。';} catch {errors.url = '请输入有效的 API 基础地址。';}
  if (!c.models.some(m => m.selected)) errors.models = '请至少选择一个模型。';
  if (!c.models.some(m => m.selected && m.id === c.defaultID)) errors.models = '请设置一个已选的连接默认模型。';
  if (c.override) for (const [key, min, max] of [['maxTokens', 128, 32000], ['firstTimeout', 10, 600], ['idleTimeout', 10, 600], ['taskTimeout', 1, 60]]) if (!Number.isInteger(Number(c[key])) || Number(c[key]) < min || Number(c[key]) > max) errors[key] = `请输入 ${min}–${max} 范围内的整数。`;
  return errors;
}

/** 连接四页签共用 Radix Tabs 并保留草稿；标题首字母仅装饰，辅助技术只朗读连接名称。 */
function ConnectionPage({store, id, tab, setTab, change, copy, remove, add, openModel, move, jobs, start, cancel, save, restore, reset, embedded = false, busy = false, pending}) {
  const c = store.connections[id], [attempted, setAttempted] = useState(false);
  if (!c) return <><PageHeading title="我的连接"><Button onClick={add}><Plus size={16} />添加连接</Button></PageHeading><Empty title="还没有连接" action={<Button onClick={add}>添加连接</Button>} /></>;
  const dirty = isDirty(c, store.savedConnections[id]), errors = connectionErrors(c), shownErrors = attempted ? errors : {};
  const saving = pending?.target === id && pending.kind === 'connection-save';
  /** 保存失败时跳到对应页签，让字段错误与触发操作相邻可见。 */
  async function trySave() { setAttempted(true); if (errors.name || errors.url) setTab('info'); else if (Object.keys(errors).some(key => key.endsWith('Timeout') || key === 'maxTokens')) setTab('advanced'); else if (errors.models) setTab('models'); else {if (await save()) setAttempted(false); return;} requestAnimationFrame(() => document.querySelector('#main [aria-invalid="true"]')?.focus()); }
  return <><PageHeading level={embedded ? 2 : 1} title={<span className="connection-page-title"><span className="avatar avatar-large" aria-hidden="true"><Server size={21} /></span>{c.name || '未命名连接'}</span>}><IconButton label={`复制连接 ${c.name}`} onClick={copy}><Copy size={17} /></IconButton><IconButton label={`删除连接 ${c.name}`} className="icon-delete" disabled={busy} onClick={remove}><Trash2 size={17} /></IconButton></PageHeading><Tabs.Root value={tab} onValueChange={setTab} className="connection-tabs"><Tabs.List className="tabs-list" aria-label="连接设置">{[['info', Link, '连接信息'], ['models', Brain, '模型选择'], ['purpose', GitBranch, '任务分工'], ['advanced', SlidersHorizontal, '高级设置']].map(([value, Icon, label]) => <Tabs.Trigger className="tab-trigger" key={value} value={value}><Icon size={17} aria-hidden="true" />{label}{value === 'models' && <span className="tab-number">{c.models.filter(m => m.selected).length}</span>}</Tabs.Trigger>)}</Tabs.List><Tabs.Content value="info"><ConnectionInfo c={c} change={change} errors={shownErrors} job={jobs[`test-${id}`]} cancel={() => cancel(`test-${id}`)} test={() => start(`test-${id}`, '正在测试连接')} /></Tabs.Content><Tabs.Content value="models"><ConnectionModels c={c} change={change} openModel={openModel} move={move} job={jobs[`catalog-${id}`]} refresh={() => start(`catalog-${id}`, '正在刷新模型')} /></Tabs.Content><Tabs.Content value="purpose"><ConnectionPurpose c={c} change={change} /></Tabs.Content><Tabs.Content value="advanced"><ConnectionAdvanced c={c} change={change} reset={reset} errors={shownErrors} /></Tabs.Content></Tabs.Root><footer className="connection-save-bar editor-footer">{(store.actionError?.target === id || attempted && Object.keys(errors).length > 0) && <div className="save-message field-error" role="alert">{store.actionError?.target === id ? store.actionError.message : Object.values(errors)[0]}</div>}<div className="save-actions"><Button variant="ghost" disabled={busy || !dirty} onClick={() => {restore(); setAttempted(false);}}>还原</Button><Button variant="primary" disabled={busy || !dirty} aria-busy={saving} onClick={trySave}>{saving && <Spinner />}{saving ? '正在保存' : '保存连接'}</Button></div></footer></>;
}

/** 插件卡片按绑定状态展示版本；检查等待仅由按钮反馈，保留下载进度、取消及其他结果或错误提示。 */
function PluginsPage({backend, updateAction, navigate}) {
  const p = backend.plugin || {}, update = backend.pluginUpdate || {};
  const job = toUpdateJob(p.phase === 'installing' ? {...update, phase: 'installing', message: p.message} : update);
  const busy = job.phase === 'loading', installed = Boolean(p.installedVersion), available = ['available', 'downloaded'].includes(update.phase);
  const message = p.phase === 'error' || p.phase === 'manual' ? p.message : busy && job.operation === 'checking' ? '' : job.label;
  return <><PageHeading title="插件管理" /><div className="section-line plugin-list-heading"><h2>当前插件</h2><span>1 个插件</span></div><div className="plugin-grid"><Panel className="plugin-panel"><div className="plugin-content"><div className="plugin-title"><span className="plugin-avatar"><Plug size={23} /></span><div><h2>API Subagents</h2><small>Codex 插件</small></div><Badge tone={busy ? 'neutral' : installed && !available ? 'active' : 'warning'} icon={busy ? undefined : installed && !available ? CircleCheck : Download}>{busy ? '处理中' : available ? '可更新' : installed ? '已安装' : '未安装'}</Badge></div><p>把边界明确的代码、文档与图像任务交给其他模型，由主任务审查结果。</p><div className="plugin-capabilities"><Badge>模型委派</Badge><Badge>文档与图像</Badge></div></div><div className="plugin-install"><dl><div><dt>已安装版本</dt><dd>{p.installedVersion || '未安装'}</dd></div><div><dt>App 附带版本</dt><dd>{p.bundledVersion || '不可用'}</dd></div></dl>{message && <div className={cn('job-inline', (job.phase === 'error' || p.phase === 'error') && 'field-error')} role="status">{message}</div>}{busy && update.phase === 'downloading' && <progress className="operation-progress" aria-label="插件下载进度" value={update.progress || 0} max={1} />}<div className="plugin-actions"><Button disabled={busy} aria-busy={busy} onClick={() => updateAction('checkPluginUpdate', true)}>{busy && job.operation === 'checking' ? <Spinner /> : <RefreshCw size={16} />}{busy && job.operation === 'checking' ? '正在检查' : '检查更新'}</Button><Button variant="primary" disabled={busy || !available && (!p.bundledVersion || p.installedVersion === p.bundledVersion)} aria-busy={busy} onClick={() => updateAction(available ? 'updatePlugin' : 'installPlugin', true)}>{busy && job.operation === 'installing' ? <Spinner /> : <Download size={16} />}{busy && job.operation === 'installing' ? '正在安装' : available || installed ? '更新插件' : '安装插件'}</Button>{busy && ['checking', 'downloading'].includes(update.phase) && <Button variant="ghost" onClick={() => updateAction('cancelPluginUpdate', true)}>取消</Button>}</div></div></Panel></div><div className="plugin-help"><HelpCircle size={20} /><div><strong>安装后，在新聊天中加载插件</strong><p>已有聊天继续使用原来的插件进程。</p></div><Button variant="ghost" onClick={() => navigate('help')}>使用帮助<ArrowRight size={16} /></Button></div></>;
}

/** 帮助使用原生 details 说明工作流；排错入口只在详情展开时挂载，不影响已确认的收起布局。 */
function HelpPage({navigate, openDiagnostics}) {
  const [requestVisible, setRequestVisible] = useState(false);
  return <><PageHeading title="使用帮助" /><div className="help-grid"><Panel title="主对话接入" icon={Layers}><div className="panel-body"><ol className="help-steps"><li><strong>准备连接</strong><span>在「我的连接」保存地址与可用模型。</span></li><li><strong>组合配置</strong><span>添加连接，在模型预览中勾选模型与默认项。</span></li><li><strong>明确启用</strong><span>点击「保存并启用」，Codex 只展示该配置的模型。</span></li></ol><Button onClick={() => navigate('plans')}>管理接入配置<ArrowRight size={16} /></Button></div></Panel><Panel title="插件委派" icon={Plug}><div className="panel-body"><ol className="help-steps"><li><strong>检查插件</strong><span>安装或更新后，在新聊天中加载。</span></li><li><strong>填写任务分工</strong><span>说明连接擅长处理的任务。</span></li><li><strong>按需授权</strong><span>写入工作区与联网需要明确授权。</span></li></ol><Button onClick={() => navigate('plugins')}>查看插件<ArrowRight size={16} /></Button></div></Panel></div><Panel title="常见问题" icon={HelpCircle}><div className="faq-list">{[['配置的模型选择会改连接吗？', '不会。连接维护可用目录，接入配置独立保存要展示的模型子集和默认模型。相同连接可在不同配置中选择不同模型。'], ['仅保存后会切换正在使用的配置吗？', '不会。只有明确启用或更新接入时才改变当前范围。顶部始终展示实际接入状态。'], ['连接添加了新模型，为什么配置中没有自动选中？', '配置不会自动扩大范围。先保存连接，再回到配置勾选新增模型。'], ['关闭后重新打开会使用什么配置？', '恢复最后明确启用的保存快照，而不是当前正在查看或编辑的另一份配置。']].map(([q, a]) => <details key={q} onToggle={e => {if (q.startsWith('关闭后')) setRequestVisible(e.currentTarget.open);}}><summary>{q}<ChevronRight size={16} aria-hidden="true" /></summary><p>{a}</p>{q.startsWith('关闭后') && requestVisible && <Button onClick={openDiagnostics}>查看最近请求</Button>}</details>)}</div></Panel></>;
}

/** 通用表单弹窗共用 Radix 焦点陷阱与关闭行为；恢复实际触发按钮。 */
function Modal({open, onClose, title, children, opener, wide}) {
  return <Dialog.Root open={open} onOpenChange={value => {if (!value) onClose();}}><Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className={cn('dialog-content', wide && 'dialog-wide')} onCloseAutoFocus={e => {if (opener.current?.isConnected) {e.preventDefault(); opener.current.focus();}}}><div className="dialog-heading"><Dialog.Title>{title}</Dialog.Title><Dialog.Close asChild><Button variant="ghost" className="icon-button" aria-label="关闭弹窗"><X size={19} /></Button></Dialog.Close></div><Dialog.Description className="sr-only">设置操作，关闭弹窗不会提交未保存的输入。</Dialog.Description>{children}</Dialog.Content></Dialog.Portal></Dialog.Root>;
}
/** 破坏性操作必须明确确认；提示类弹窗仅需确认阅读，支持 Escape 并恢复触发按钮焦点。 */
function Confirm({data, close, opener}) {
  return <AlertDialog.Root open={Boolean(data)} onOpenChange={value => {if (!value) close();}}><AlertDialog.Portal><AlertDialog.Overlay className="dialog-overlay" /><AlertDialog.Content className="dialog-content confirm-dialog" onEscapeKeyDown={event => {if (data?.kind === 'notice') {event.preventDefault(); close();}}} onCloseAutoFocus={e => {if (opener.current?.isConnected) {e.preventDefault(); opener.current.focus();}}}><AlertDialog.Title>{data?.title}</AlertDialog.Title><AlertDialog.Description>{data?.description}</AlertDialog.Description><div className="dialog-actions">{data?.kind === 'notice' ? <AlertDialog.Cancel asChild><Button variant="primary">知道了</Button></AlertDialog.Cancel> : <><AlertDialog.Cancel asChild><Button>取消</Button></AlertDialog.Cancel><AlertDialog.Action asChild><Button variant="destructive" onClick={() => {data?.run(); close();}}>{data?.action || '确认'}</Button></AlertDialog.Action></>}</div></AlertDialog.Content></AlertDialog.Portal></AlertDialog.Root>;
}

/** 新建实体表单保持输入与错误，提交失败不关闭，原生 submit 支持 Enter。 */
function NewEntityForm({kind, submit, close}) {
  const [name, setName] = useState(''), [url, setURL] = useState('https://api.openai.com/v1'), [protocol, setProtocol] = useState('responses'), [errors, setErrors] = useState({});
  /** 只创建未保存的连接草稿；地址和接口校验不发起网络请求。 */
  function handleSubmit(e) {e.preventDefault(); const next = {}; if (!name.trim()) next.name = '请填写名称。'; if (kind === 'connection') {try {const u = new URL(url); if (!['http:', 'https:'].includes(u.protocol) || u.username || u.password) throw new Error();} catch {next.url = '请填写不含凭据的 HTTP(S) 地址。';}} if (Object.keys(next).length) {setErrors(next); requestAnimationFrame(() => e.target.querySelector('[aria-invalid="true"]')?.focus()); return;} submit(name.trim(), url, protocol);}
  return <form onSubmit={handleSubmit}><div className="dialog-body"><Field autoFocus label={kind === 'plan' ? '配置名称' : '连接名称'} maxLength={60} value={name} onChange={e => {setName(e.target.value); setErrors({...errors, name: ''});}} error={errors.name} />{kind === 'connection' && <><label className="field"><span>接口类型</span><select className="input" value={protocol} onChange={e => {const defaults = {responses: 'https://api.openai.com/v1', compatible: 'https://api.openai.com/v1', anthropic: 'https://api.anthropic.com/v1', gemini: 'https://generativelanguage.googleapis.com/v1beta'}; if (url === defaults[protocol]) setURL(defaults[e.target.value]); setProtocol(e.target.value);}}><option value="responses">Responses</option><option value="compatible">OpenAI Chat Completions</option><option value="anthropic">Anthropic Messages</option><option value="gemini">Gemini</option></select></label><Field label="API 地址" value={url} error={errors.url} onChange={e => {setURL(e.target.value); setErrors({...errors, url: ''});}} /></>}</div><div className="dialog-actions"><Button onClick={close}>取消</Button><Button variant="primary" type="submit">{kind === 'plan' ? '创建配置' : '添加连接'}</Button></div></form>;
}

/** 模型设置独立编辑上下文和模型级 Fast 设置；保存写入连接草稿，取消不修改任何模型。 */
function ModelForm({model, c, submit, close}) {
  const policy = contextSettings(model);
  const [value, setValue] = useState(model ? {...clone(model), fast: model.fast === true, ...policy, contextOverride: policy.contextOverride ?? defaultContextSettings(model.context).contextOverride ?? ''} : {id: '', name: '', context: null, contextEnabled: false, contextOverride: '', selected: true, image: 'auto', output: '', fast: false}), [errors, setErrors] = useState({});
  const contextID = useId(), contextHintID = useId(), fastID = useId(), fastHintID = useId();
  /** 开启时上下文必须是正整数；关闭时提交 null 覆盖值，绝不改写原生容量或模型勾选。 */
  function handleSubmit(e) {
    e.preventDefault(); const next = {};
    if (!value.id.trim()) next.id = '请填写模型 ID。'; else if (Array.from(value.id.trim()).length > 200 || /\p{Cc}/u.test(value.id)) next.id = '模型 ID 最多 200 个字符，不能包含控制字符。'; else if (!model && c.models.some(m => m.id === value.id.trim())) next.id = '此连接已经存在该模型 ID。';
    if (value.contextEnabled && (!Number.isInteger(Number(value.contextOverride)) || Number(value.contextOverride) <= 0 || Number(value.contextOverride) > 2000000)) next.contextOverride = '上下文须为 1–2,000,000 的整数；不设置请关闭开关。';
    if (value.output !== '' && (!Number.isInteger(Number(value.output)) || Number(value.output) < 128 || Number(value.output) > 1000000)) next.output = '输出上限须为 128–1,000,000 的整数，或留空。';
    if (Object.keys(next).length) {setErrors(next); requestAnimationFrame(() => e.target.querySelector('[aria-invalid="true"]')?.focus()); return;}
    submit({...value, id: value.id.trim(), contextOverride: value.contextEnabled ? Number(value.contextOverride) : null});
  }
  return <form className="model-settings-form" noValidate onSubmit={handleSubmit}><div className="dialog-body">
    <Field autoFocus label="模型 ID" readOnly={Boolean(model)} value={value.id} onChange={e => {setValue({...value, id: e.target.value}); setErrors({...errors, id: ''});}} error={errors.id} />
    <Field label="显示名称（可选）" maxLength={200} value={value.name} onChange={e => setValue({...value, name: e.target.value})} />
    <section className="context-setting model-fast-setting" aria-labelledby={fastID}>
      <div className="context-setting-heading"><label id={fastID} htmlFor={fastID + '-switch'}>Fast 模式</label><Switch.Root id={fastID + '-switch'} className="switch" aria-labelledby={fastID} aria-describedby={fastHintID} disabled={c.protocol === 'gemini'} checked={value.fast} onCheckedChange={fast => setValue({...value, fast})}><Switch.Thumb className="switch-thumb" /></Switch.Root></div>
      <p id={fastHintID} className="context-setting-hint">{c.protocol === 'gemini' ? 'Gemini 原生协议暂不支持此开关。' : '开启后请求上游 Fast / Priority 档位，费用可能增加；需上游模型及账号支持。'}</p>
    </section>
    <section className="context-setting" aria-labelledby={contextID}>
      <div className="context-setting-heading"><label id={contextID} htmlFor={contextID + '-switch'}>设置上下文</label><Switch.Root id={contextID + '-switch'} className="switch" aria-labelledby={contextID} aria-describedby={contextHintID} checked={value.contextEnabled} onCheckedChange={enabled => {setValue({...value, contextEnabled: enabled}); setErrors({...errors, contextOverride: ''});}}><Switch.Thumb className="switch-thumb" /></Switch.Root></div>
      <fieldset disabled={!value.contextEnabled}><Field label="上下文大小（tokens）" type="number" min={1} step={1} value={value.contextEnabled ? value.contextOverride : ''} placeholder="使用模型自带上下文" error={errors.contextOverride} onChange={e => {setValue({...value, contextOverride: e.target.value}); setErrors({...errors, contextOverride: ''});}} /></fieldset>
      <p id={contextHintID} className="context-setting-hint">{value.contextEnabled ? '当前采用 ' + capacity(Number(value.contextOverride) > 0 ? Number(value.contextOverride) : null) : value.context ? '使用模型自带上下文 · ' + capacity(value.context) : '使用模型自带上下文'}</p>
    </section>
    <div className="field-grid"><Field label="插件输出上限（可选）" type="number" value={value.output} error={errors.output} onChange={e => {setValue({...value, output: e.target.value}); setErrors({...errors, output: ''});}} /><label className="field"><span>图像输入</span><select className="input" value={value.image} onChange={e => setValue({...value, image: e.target.value})}><option value="auto">自动适配</option><option value="yes">支持</option><option value="no">不支持</option></select></label></div>
  </div><div className="dialog-actions"><Button onClick={close}>取消</Button><Button variant="primary" type="submit">保存模型</Button></div></form>;
}

/** 更新弹窗将可滚动正文与偏好、操作区分开；回调及草稿安装保护不变，窄矮窗口仍可关闭或取消。 */
function UpdatePanel({state = {}, version, updateAction, close, dirty}) {
  const phase = state.phase || 'idle', busy = ['checking', 'downloading', 'installing'].includes(phase);
  return <div className="update-panel">
    <div className="update-panel-body">
      <div className="update-state-icon">{busy ? <Spinner /> : phase === 'error' ? <CircleAlert size={30} /> : <RefreshCw size={30} />}</div>
      <h2>{busy ? phase === 'checking' ? '正在检查更新' : phase === 'downloading' ? '正在下载更新' : '正在准备安装' : phase === 'available' ? '发现新版本' : phase === 'downloaded' ? '更新已准备好' : ['latest', 'current'].includes(phase) ? '已是最新版本' : phase === 'error' ? '暂时无法检查更新' : '检查应用更新'}</h2>
      <p role="status">{state.message || '检查新的功能与修复'}</p>
      <dl className="update-versions"><div><dt>当前版本</dt><dd>{state.version || version || '—'}</dd></div><div><dt>可用版本</dt><dd>{state.availableVersion || '—'}</dd></div></dl>
      {phase === 'downloading' && <progress className="operation-progress" aria-label="应用下载进度" value={state.progress || 0} max={1} />}
      {state.releaseNotes && <div className="release-notes">{state.releaseNotes}</div>}
      {dirty && phase === 'downloaded' && <div className="field-error" role="status">请先保存或还原草稿再安装更新。</div>}
    </div>
    <div className="update-panel-footer">
      <label className="update-preference"><Tick checked={state.autoCheck !== false} label="打开更新窗口时自动检查" onChange={enabled => updateAction('setUpdateAutoCheck', false, enabled)} /><span>打开更新窗口时自动检查</span></label>
      <div className="dialog-actions">{busy ? phase !== 'installing' && <Button onClick={() => updateAction('cancelUpdate')}>取消操作</Button> : phase === 'available' ? <><Button variant="ghost" onClick={() => updateAction('skipUpdate')}>跳过此版本</Button><Button onClick={close}>稍后</Button><Button variant="primary" onClick={() => updateAction('downloadUpdate')}><Download size={16} />下载更新</Button></> : phase === 'downloaded' ? <Button variant="primary" disabled={dirty} onClick={() => updateAction('installUpdate')}>重启并安装</Button> : phase === 'manual' ? <Button variant="primary" onClick={() => updateAction('openReleases')}>查看发布页<ArrowRight size={16} /></Button> : <Button variant="primary" onClick={() => updateAction('checkUpdate')}><RefreshCw size={16} />{phase === 'error' ? '重试' : '检查更新'}</Button>}</div>
    </div>
  </div>;
}

/** 各工作区页面共用已确认的接入配置主题；页面标识只限定样式，业务状态仍来自固定 Go/API 适配层。 */
function App() {
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false), [usageView, setUsageView] = useState(createUsageView);
  const usageGate = useRef(createUsageGate());
  const c = useWorkbench({extraDirty: Boolean(usageView.priceDraft?.dirty)}), {store, backend, page, connectionID, modal, confirm, jobs, opener} = c;
  const currentConnection = store.connections[connectionID], updateJob = jobs.update;
  const sidebarProps = {store, page, navigate: c.navigate, version: backend.version || '', collapsed: sidebarCollapsed, toggleCollapsed: () => setSidebarCollapsed(value => !value), updateJob, onUpdate: () => c.openModal({type: 'update'})};
  useEffect(() => {
    /** 保存快捷键遵循当前页面和模态边界，输入文字不会触发全局导航。 */
    function saveShortcut(event) {if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {event.preventDefault(); if (!modal && !c.busy) {if (page === 'plans') void c.commitPlan(false); else if (page === 'connection' || page === 'connections') void c.commitConnection();}}}
    document.addEventListener('keydown', saveShortcut); return () => document.removeEventListener('keydown', saveShortcut);
  }, [page, store.selected, connectionID, modal, c.busy]);
  const reported = useRef(false);
  useEffect(() => {
    if (!c.loading && !c.loadError && !reported.current) {
      reported.current = true;
      window.go?.desktop?.App?.FrontendReady?.({ok: true, configLoaded: true, accessConfigCount: store.plans.length, updateBindings: ['CancelUpdate', 'SkipUpdate', 'SetUpdateAutoCheck', 'CancelPluginUpdate', 'EnableAccess'].every(name => typeof window.go?.desktop?.App?.[name] === 'function')});
    }
  }, [c.loading, c.loadError]);
  return <Tooltip.Provider delayDuration={350}><a className="skip-link" href="#main">跳到主内容</a><div className={cn("app-shell", "macos-settings", "macos-workspace", sidebarCollapsed && "sidebar-collapsed")} data-page={page}><aside className="sidebar"><Sidebar {...sidebarProps} /></aside><div className="app-main"><div className="topbar"><Dialog.Root open={c.mobileNav} onOpenChange={c.setMobileNav}><Dialog.Trigger asChild><Button variant="ghost" className="icon-button mobile-nav-trigger" aria-label="打开导航"><Menu size={20} /></Button></Dialog.Trigger><Dialog.Portal><Dialog.Overlay className="dialog-overlay" /><Dialog.Content className="mobile-sidebar macos-settings"><Dialog.Title className="sr-only">工作区导航</Dialog.Title><Dialog.Description className="sr-only">选择页面或连接</Dialog.Description><Dialog.Close asChild><Button variant="ghost" className="icon-button mobile-nav-close" aria-label="关闭导航"><X size={19} /></Button></Dialog.Close><Sidebar {...sidebarProps} /></Dialog.Content></Dialog.Portal></Dialog.Root><div className="topbar-actions"><UpdateTrigger job={updateJob} onClick={sidebarProps.onUpdate} /></div></div>{backend.close?.phase && backend.close.phase !== 'confirm' && <div className={cn('job-inline', backend.close.phase === 'blocked' && 'field-error')} role="status">{backend.close.message}</div>}{backend.relay?.message?.startsWith('上次的') && <div className="job-inline field-error" role="alert">{backend.relay.message}<Button onClick={() => c.toggleAccess(false)}>恢复配置</Button></div>}<main id="main" className={cn('page-content', page === 'plans' && 'plans-page', page === 'usage' && 'usage-page')} tabIndex={-1} aria-busy={c.loading || c.busy} inert={c.busy && !['connection-save', 'plan-save'].includes(c.pending?.kind) ? true : undefined}>{c.loading ? <section className="panel loading-skeleton" aria-label="正在读取工作区"><div /><div /><div /></section> : c.loadError ? <Empty title="无法读取配置" action={<Button onClick={c.load}>重试</Button>}>{c.loadError}</Empty> : page === 'usage' ? <UsagePage view={usageView} onChange={setUsageView} connections={store.savedConnections} requestGate={usageGate.current} /> : page === 'plans' ? <PlanWorkspace store={store} busy={c.busy} pending={c.pending} select={c.select} edit={c.editPlan} save={() => c.commitPlan(false)} apply={() => c.commitPlan(true)} restore={c.restorePlan} copy={c.copyPlan} remove={c.removePlan} create={() => c.openModal({type: 'plan'})} toggle={c.toggleAccess} openConnection={c.openConnection} /> : (page === 'connections' || page === 'connection') ? <ConnectionsWorkspace store={store} id={connectionID} openConnection={c.openConnection} addConnection={() => c.openModal({type: 'connection'})}><ConnectionPage embedded key={connectionID} store={store} busy={c.busy} pending={c.pending} id={connectionID} tab={c.tab} setTab={c.setTab} change={c.editConnection} copy={() => c.copyConnection(connectionID)} remove={() => c.removeConnection(connectionID)} add={() => c.openModal({type: 'connection'})} openModel={id => c.openModal({type: 'model', id})} move={c.moveModel} jobs={jobs} start={c.start} cancel={c.cancel} save={c.commitConnection} restore={c.restoreConnection} reset={c.resetAdvanced} /></ConnectionsWorkspace> : page === 'plugins' ? <PluginsPage backend={backend} updateAction={c.updateAction} navigate={c.navigate} /> : <HelpPage navigate={c.navigate} openDiagnostics={() => c.openModal({type: 'diagnostics'})} />}</main></div></div><Modal open={Boolean(modal)} title={modal?.type === 'plan' ? '新建配置' : modal?.type === 'connection' ? '添加连接' : modal?.type === 'model' ? modal.id ? '模型设置' : '手动添加模型' : modal?.type === 'diagnostics' ? '最近请求' : '应用更新'} onClose={() => c.setModal(null)} opener={opener}>{modal?.type === 'plan' || modal?.type === 'connection' ? <NewEntityForm key={modal.type} kind={modal.type} submit={modal.type === 'plan' ? c.createPlan : c.createConnection} close={() => c.setModal(null)} /> : modal?.type === 'model' && currentConnection ? <ModelForm key={modal.id || 'new'} model={currentConnection.models.find(m => m.id === modal.id)} c={currentConnection} close={() => c.setModal(null)} submit={m => {c.editConnection(connection => ({...connection, models: modal.id ? connection.models.map(item => item.id === modal.id ? m : item) : [...connection.models, m], defaultID: connection.defaultID || m.id})); c.setModal(null);}} /> : modal?.type === 'diagnostics' ? <RequestDiagnostics relay={backend.relay} /> : modal?.type === 'update' ? <UpdatePanel state={backend.update} version={backend.version} updateAction={c.updateAction} dirty={c.dirty} close={() => c.setModal(null)} /> : null}</Modal><Confirm data={confirm} close={c.closeConfirm} opener={opener} /><div className="toast-region" role="status" aria-live="polite">{c.toast && <div className="toast"><Check size={17} /><span>{c.toast}</span><IconButton label="关闭提示" onClick={() => c.setToast('')}><X size={16} /></IconButton></div>}</div></Tooltip.Provider>;
}

createRoot(document.getElementById('root')).render(<App />);
