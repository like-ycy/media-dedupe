/* media-dedupe Wails frontend */
const state = {
  view: 'projects',
  appInfo: null,
  settings: null,
  projects: [],
  currentProjectId: null,
  groups: [],
  groupFilter: { status: 'pending', groupType: '', search: '' },
  currentGroup: null,
  selected: new Set(),
  scanEvent: null,
  scanStatus: null,
};

// Wails bindings with browser fallback for preview.
const hasWails = typeof window.go !== 'undefined' && window.go.appapi?.App;
const mock = createMock();
const api = hasWails ? window.go.appapi.App : mock;

function createMock() {
  const projects = [];
  let nextId = 1;
  return {
    AppReady: async () => ({
      version: '1.0.0-dev', ffmpeg: true, ffprobe: true, appData: '/tmp/media-dedupe',
      deleteMode: 'recycle', engine: 'SQLite + SHA-256 + pHash + TXT 指纹 + FFmpeg', platform: 'preview',
    }),
    ListProjects: async () => projects.slice(),
    GetProject: async (id) => {
      const p = projects.find((x) => x.id === id);
      if (!p) throw new Error('项目不存在');
      return p;
    },
    CreateProject: async (input) => {
      const p = {
        id: 'prj_' + nextId++, name: input.name || '未命名', createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(), paths: input.paths || [], recursive: true,
        threshold: input.threshold || 0.8, includeImages: input.includeImages !== false,
        includeVideos: input.includeVideos !== false, includeTexts: input.includeTexts !== false,
        textThreshold: input.textThreshold || 0.92, textWorkers: input.textWorkers || 8, frameCount: input.frameCount || 8,
        workers: input.workers || 2, videoWorkers: input.videoWorkers || 1,
        enableThumbs: true, lastScanAt: null,
        lastSummary: { filesSeen: 0, groups: 0, reclaimableBytes: 0, exactGroups: 0, similarGroups: 0, pendingGroups: 0 },
        scanning: false, ffmpegReady: true,
      };
      projects.push(p);
      return p;
    },
    UpdateProject: async (id, patch) => {
      const p = projects.find((x) => x.id === id);
      if (!p) throw new Error('项目不存在');
      Object.assign(p, patch);
      if (patch.paths) p.paths = patch.paths;
      return p;
    },
    DeleteProject: async (id) => {
      const i = projects.findIndex((x) => x.id === id);
      if (i >= 0) projects.splice(i, 1);
    },
    CheckPaths: async (paths) => (paths || []).map((path) => ({ path, exists: path && !path.includes('missing'), isDir: true, error: path && path.includes('missing') ? '路径不存在' : '' })),
    SelectDirectory: async () => '',
    ListRecentDirs: async () => ['/tmp', '/Users/demo/Pictures'],
    GetSettings: async () => ({
      defaultThreshold: 0.8, defaultIncludeVideos: true, defaultFrameCount: 8, defaultWorkers: 2,
      defaultIncludeTexts: true, defaultTextThreshold: 0.92, defaultTextWorkers: 8,
      defaultVideoWorkers: 1, defaultDeleteMode: 'recycle', allowPermanentDelete: false,
      ffmpegPath: '', ffprobePath: '', ffmpegAvailable: true, ffprobeAvailable: true,
    }),
    SaveSettings: async () => {},
    StartScan: async () => {},
    CancelScan: async () => {},
    GetScanStatus: async (id) => ({ projectId: id, running: false, canceled: false, percent: 0 }),
    ListGroups: async () => [],
    GetGroup: async () => { throw new Error('预览模式无扫描结果'); },
    SetGroupStatus: async () => {},
    SetRecommended: async () => {},
    DeleteFiles: async (req) => ({ succeeded: [], failed: [], mode: req.mode || 'recycle', markedProcessed: [] }),
    ExportReport: async () => { throw new Error('预览模式不支持导出'); },
    OpenPath: async () => { throw new Error('预览模式不支持打开系统程序'); },
    RevealInFolder: async () => { throw new Error('预览模式不支持定位'); },
    CopyToClipboard: async (t) => { await navigator.clipboard.writeText(t); },
    GetThumbURL: async () => '',
    GetFrameURLs: async () => [],
  };
}

function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function fmtBytes(n) {
  if (!n || n < 0) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0; let v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${u[i]}`;
}
function fmtDateTime(s) {
  if (!s) return '—';
  try {
    const d = new Date(s);
    if (Number.isNaN(d.getTime())) return s;
    return d.toLocaleString('zh-CN', { hour12: false });
  } catch { return s; }
}
function typeLabel(t) {
  return ({ exact: '精确重复', similar_image: '视觉近似图片', similar_video: '视觉近似视频', similar_text: '文本近似重复' })[t] || t;
}
function toast(msg, kind = '') {
  const root = document.getElementById('toastRoot');
  const el = document.createElement('div');
  el.className = 'toast ' + kind;
  el.textContent = msg;
  root.appendChild(el);
  setTimeout(() => el.remove(), 3200);
}
function showConfirm({ title, body, danger, confirmText = '确认', checkbox }) {
  return new Promise((resolve) => {
    const root = document.getElementById('modalRoot');
    root.hidden = false;
    root.innerHTML = `
      <div class="modal">
        <h3>${esc(title)}</h3>
        <p>${body}</p>
        ${checkbox ? `<label class="check-row"><input type="checkbox" class="checkbox" id="confirmChk" /> <span>${esc(checkbox)}</span></label>` : ''}
        <div class="actions">
          <button class="btn" id="confirmCancel">取消</button>
          <button class="btn ${danger ? 'btn-danger' : 'btn-primary'}" id="confirmOk" ${checkbox ? 'disabled' : ''}>${esc(confirmText)}</button>
        </div>
      </div>`;
    const close = (val) => { root.hidden = true; root.innerHTML = ''; resolve(val); };
    root.querySelector('#confirmCancel').onclick = () => close(false);
    const ok = root.querySelector('#confirmOk');
    if (checkbox) {
      const chk = root.querySelector('#confirmChk');
      chk.onchange = () => { ok.disabled = !chk.checked; };
    }
    ok.onclick = () => close(true);
  });
}
function currentProject() {
  return state.projects.find((p) => p.id === state.currentProjectId) || null;
}
function setPendingBadge() {
  const n = currentProject()?.lastSummary?.pendingGroups || 0;
  const el = document.getElementById('pendingBadge');
  if (n > 0) { el.hidden = false; el.textContent = String(n); } else { el.hidden = true; }
}
async function refreshProjects() {
  state.projects = await api.ListProjects() || [];
  if (!state.currentProjectId && state.projects.length) {
    state.currentProjectId = state.projects[0].id;
  }
  if (state.currentProjectId && !state.projects.some((p) => p.id === state.currentProjectId)) {
    state.currentProjectId = state.projects[0]?.id || null;
  }
  setPendingBadge();
}

async function navigate(view) {
  state.view = view;
  document.querySelectorAll('.nav-item').forEach((b) => b.classList.toggle('active', b.dataset.view === view));
  const main = document.getElementById('main');
  if (view === 'projects') main.innerHTML = await renderProjects();
  else if (view === 'config') main.innerHTML = await renderConfig();
  else if (view === 'progress') main.innerHTML = await renderProgress();
  else if (view === 'results') main.innerHTML = await renderResults();
  else if (view === 'group') main.innerHTML = await renderGroupDetail();
  else if (view === 'settings') main.innerHTML = await renderSettings();
  bindMain();
}

async function renderProjects() {
  await refreshProjects();
  const p = currentProject();
  const head = `
    <div class="toolbar">
      <div>
        <h1>项目列表</h1>
        <div class="sub">管理本机媒体去重项目，纯离线运行，安全审阅后移入系统回收站</div>
      </div>
      <div class="toolbar-actions">
        <button class="btn btn-primary" id="btnNewProject">+ 新建项目</button>
      </div>
    </div>
    <div class="safety">安全守则：本工具从不自动删除任何文件。所有重复项均需人工审阅确认，默认删除操作为「移入系统回收站」。不覆盖原文件。 · <b>SHA-256</b> 精确匹配 · <b>pHash</b> 感知视觉对比 · <b>SQLite size+mtime</b> 增量加速</div>`;

  if (!state.projects.length) {
    return head + `
      <div class="card empty">
        <div class="empty-icon">+</div>
        <h2>尚未创建任何去重项目</h2>
        <div>选择本地媒体目录，基于 SHA-256 精确校验与感知哈希算法发现重复文件，人工审阅后安全移入回收站。</div>
        <div class="steps">
          <div class="step"><div class="n">1</div><h4>选择目录</h4><p>添加一个或多个本地文件夹，支持跨盘符路径与增量扫描。</p></div>
          <div class="step"><div class="n">2</div><h4>增量扫描</h4><p>首次建立特征，后续秒级增量匹配。纯本机离线计算。</p></div>
          <div class="step"><div class="n">3</div><h4>审阅与清理</h4><p>逐组比对关键帧与元数据，勾选后安全移入系统回收站。</p></div>
        </div>
        <div style="margin-top:18px"><button class="btn btn-primary" id="btnNewProjectEmpty">+ 立即新建第一个项目</button></div>
      </div>`;
  }

  const cards = state.projects.map((proj) => {
    const sum = proj.lastSummary || {};
    const pending = sum.pendingGroups || 0;
    let badge = '';
    if (proj.scanning) badge = `<span class="badge badge-info">扫描中…</span>`;
    else if (pending > 0) badge = `<span class="badge badge-pending">待处理 ${pending} 组重复</span>`;
    else if (proj.lastScanAt) badge = `<span class="badge badge-ok">已全部处理完成</span>`;
    else badge = `<span class="badge badge-warn">尚未扫描</span>`;
    const reclaim = sum.reclaimableBytes || 0;
    const algo = proj.includeVideos && state.appInfo?.ffmpeg
      ? (proj.includeImages ? 'SHA-256 + 图片 pHash + FFmpeg 抽帧多帧 pHash' : 'FFmpeg 抽帧多帧 pHash')
      : (proj.includeImages ? 'SHA-256 + 图片 pHash' : 'SHA-256');
    return `
      <div class="card project-card" data-id="${esc(proj.id)}">
        <div style="display:flex;justify-content:space-between;gap:8px;align-items:start">
          <div>
            <h3>${esc(proj.name)}</h3>
            <div class="meta">ID: ${esc(proj.id)} · 阈值 ${proj.threshold} · 抽帧 ${proj.frameCount}</div>
          </div>
          ${badge}
        </div>
        <div class="meta">扫描目录（${proj.paths.length}）</div>
        <div class="path-chips">${proj.paths.map((x) => `<span class="chip">${esc(x)}</span>`).join('')}</div>
        <div class="stats">
          <div>上次扫描<br><b>${fmtDateTime(proj.lastScanAt)}</b></div>
          <div>匹配算法<br><b>${algo}</b></div>
          <div>${proj.lastScanAt ? '预计可释放' : '待释放空间'}<br><b class="mono">${fmtBytes(reclaim)}</b></div>
          <div>重复组<br><b>${sum.groups || 0}</b>（精确 ${sum.exactGroups || 0} · 近似 ${sum.similarGroups || 0}）</div>
        </div>
        <div class="card-actions">
          <button class="btn btn-primary" data-act="open" data-id="${esc(proj.id)}">打开项目</button>
          <button class="btn" data-act="rescan" data-id="${esc(proj.id)}" ${proj.scanning ? 'disabled' : ''}>重新扫描</button>
          <button class="btn" data-act="edit" data-id="${esc(proj.id)}">配置</button>
          <button class="btn" data-act="delete-project" data-id="${esc(proj.id)}">删除项目</button>
        </div>
      </div>`;
  }).join('');

  return head + `<div class="grid grid-2">${cards}</div>`;
}

async function renderConfig() {
  await refreshProjects();
  const p = currentProject();
  if (!p) {
    return `<div class="toolbar"><div><h1>扫描配置</h1><div class="sub">请先创建项目</div></div></div>
      <div class="card empty">尚未选择项目。请先在「项目列表」中新建或打开一个项目。</div>`;
  }
  const status = await api.CheckPaths(p.paths) || [];
  const pathRows = status.map((s, i) => `
    <div style="display:flex;gap:8px;align-items:center;margin-bottom:6px">
      <input type="text" data-path-index="${i}" value="${esc(s.path)}" />
      <span class="badge ${s.exists && s.isDir ? 'badge-ok' : 'badge-pending'}">${s.exists && s.isDir ? '有效' : esc(s.error || '无效')}</span>
    </div>`).join('');
  return `
    <div class="toolbar">
      <div>
        <h1>扫描配置</h1>
        <div class="sub">${esc(p.name)} · 配置扫描目录与算法参数</div>
      </div>
      <div class="toolbar-actions">
        <button class="btn" id="btnAddPath">+ 添加目录</button>
        <button class="btn btn-primary" id="btnSaveConfig">保存配置</button>
        <button class="btn btn-primary" id="btnStartScan">开始扫描</button>
      </div>
    </div>
    <div class="card form">
      <div class="form-row">
        <label>项目名称</label>
        <input type="text" id="cfgName" value="${esc(p.name)}" />
      </div>
      <div class="form-row">
        <label>扫描目录</label>
        <div class="hint">每行一个绝对路径；失效路径会标红提示</div>
        <div id="pathList">${pathRows}</div>
      </div>
      <div class="form-row">
        <label>相似度阈值（pHash / 视频帧相似）</label>
        <input type="number" id="cfgThreshold" min="0.5" max="0.99" step="0.01" value="${p.threshold}" />
        <div class="hint">默认 0.80。精确重复始终使用 SHA-256 全量哈希。</div>
      </div>
      <div class="check-row"><input type="checkbox" class="checkbox" id="cfgImages" ${p.includeImages ? 'checked' : ''}/> 启用图片 pHash（HEIC/HEIF 仅精确）</div>
      <div class="check-row"><input type="checkbox" class="checkbox" id="cfgVideos" ${p.includeVideos ? 'checked' : ''}/> 启用视频（FFmpeg 抽帧多帧 pHash）</div>
      <div class="check-row"><input type="checkbox" class="checkbox" id="cfgTexts" ${p.includeTexts ? 'checked' : ''}/> 启用 TXT 小说正文去重</div>
      <div class="form-row"><label>TXT 正文相似度</label><input type="number" id="cfgTextThreshold" min="0.8" max="0.99" step="0.01" value="${p.textThreshold || 0.92}" /></div>
      <div class="form-row">
        <label>视频抽帧数</label>
        <input type="number" id="cfgFrames" min="1" max="32" value="${p.frameCount}" />
        <div class="hint">默认 8 帧；需系统安装 FFmpeg。</div>
      </div>
      <div class="form-row"><label>图片/哈希并发</label><input type="number" id="cfgWorkers" min="1" max="16" value="${p.workers}" /></div>
      <div class="form-row"><label>视频抽帧并发</label><input type="number" id="cfgVWorkers" min="1" max="8" value="${p.videoWorkers}" /></div>
      <div class="form-row"><label>TXT 并发</label><input type="number" id="cfgTextWorkers" min="1" max="16" value="${p.textWorkers || 8}" /></div>
      <div class="check-row"><input type="checkbox" class="checkbox" id="cfgRecursive" ${p.recursive ? 'checked' : ''}/> 递归子目录</div>
      <div class="check-row"><input type="checkbox" class="checkbox" id="cfgThumbs" ${p.enableThumbs ? 'checked' : ''}/> 生成缩略图</div>
      ${!state.appInfo?.ffmpeg ? `<div class="warn-banner">未检测到 FFmpeg / ffprobe。视频阶段将跳过；图片 SHA-256 + pHash 仍可用。可在「全局设置」配置路径。</div>` : ''}
    </div>`;
}

const STAGES = [
  { key: 'discover', title: '1. 发现文件' },
  { key: 'exact', title: '2. 精确哈希 (SHA-256)' },
  { key: 'image', title: '3. 图片感知哈希 (pHash)' },
  { key: 'video', title: '4. 视频抽帧 (FFmpeg)' },
  { key: 'text', title: '5. TXT 正文相似度' },
  { key: 'match', title: '6. 分组匹配' },
];

async function renderProgress() {
  await refreshProjects();
  const p = currentProject();
  if (!p) {
    return `<div class="toolbar"><div><h1>扫描进度</h1></div></div><div class="card empty">尚未选择项目</div>`;
  }
  const st = await api.GetScanStatus(p.id);
  state.scanStatus = st;
  const ev = st.event || state.scanEvent;
  const percent = st.percent ?? ev?.percent ?? 0;
  const stage = ev?.stage || '';
  const phase = ev?.phase || '';
  const stageCards = STAGES.map((s) => {
    let cls = 'stage';
    let desc = '等待中';
    if (stage === s.key && phase === 'running') { cls += ' running'; desc = ev?.message || '进行中…'; }
    else if (stage === s.key && (phase === 'done' || phase === 'skipped')) { cls += ' done'; desc = phase === 'skipped' ? (ev?.message || '已跳过') : (ev?.message || '已完成'); if (phase === 'skipped') cls = 'stage skipped'; }
    else if (STAGES.findIndex((x) => x.key === stage) > STAGES.findIndex((x) => x.key === s.key) && stage !== 'canceled') {
      cls += ' done'; desc = '已完成';
    } else if (stage === 'video' && !state.appInfo?.ffmpeg && ['match', 'done', 'thumb'].includes(stage)) {
      desc = '已跳过（无 FFmpeg）';
    }
    return `<div class="${cls}"><div class="t">${s.title}</div><div class="d">${esc(desc)}</div></div>`;
  }).join('');

  const cancelled = st.canceled || stage === 'canceled';
  const done = stage === 'done' && phase === 'done';
  const errors = ev?.errors ?? st.errors ?? 0;

  return `
    <div class="toolbar">
      <div>
        <h1>${st.running ? `扫描执行中（${percent.toFixed(0)}%）` : done ? '扫描完成' : cancelled ? '扫描已取消' : '扫描进度'}</h1>
        <div class="sub">${esc(p.name)} · ${ev?.message || st.lastError || '尚未开始扫描'}</div>
      </div>
      <div class="toolbar-actions">
        ${st.running ? `<button class="btn btn-danger" id="btnCancelScan">取消扫描</button>` : `<button class="btn btn-primary" id="btnStartScanFromProgress">开始扫描</button>`}
        ${done || cancelled ? `<button class="btn" id="btnGoResults">查看结果</button>` : ''}
      </div>
    </div>
    ${cancelled ? `<div class="warn-banner">扫描已取消，已计算的特征已写入 SQLite 缓存，下次扫描将自动复用。</div>` : ''}
    ${done ? `<div class="success-banner">扫描完成：发现 ${ev?.foundExact || 0} 个精确组 + ${ev?.foundSimilar || 0} 个近似组。系统不会自动删除任何文件。</div>` : ''}
    ${errors ? `<div class="warn-banner">本次扫描有 ${errors} 个文件处理错误，详情可在导出报告中查看。</div>` : ''}
    <div class="stages">${stageCards}</div>
    <div class="card">
      <div class="kpis">
        <div class="kpi"><div class="label">总体进度</div><div class="value">${percent.toFixed(1)}%</div>
          <div class="progress-bar"><i style="width:${Math.max(0, Math.min(100, percent))}%"></i></div>
        </div>
        <div class="kpi"><div class="label">已发现文件</div><div class="value">${ev?.filesSeen ?? st.filesSeen ?? 0}</div><div class="muted">个对象</div></div>
        <div class="kpi"><div class="label">精确重复 (SHA-256)</div><div class="value">${ev?.foundExact ?? 0}</div><div class="muted">组</div></div>
      </div>
      <div class="kpis">
        <div class="kpi"><div class="label">视觉近似 (pHash)</div><div class="value">${ev?.foundSimilar ?? 0}</div><div class="muted">组（图片/视频）</div></div>
        <div class="kpi"><div class="label">错误</div><div class="value">${errors}</div><div class="muted">文件失败</div></div>
        <div class="kpi"><div class="label">当前文件</div><div class="value" style="font-size:13px;font-weight:600;word-break:break-all">${esc(ev?.currentFile || '—')}</div></div>
      </div>
      <div class="muted">免误删保证：本工具从不自动删除任何文件。扫描完毕后将完整列出分组，由您逐一审阅确认后移入系统回收站。</div>
    </div>`;
}

async function renderResults() {
  await refreshProjects();
  const p = currentProject();
  if (!p) {
    return `<div class="toolbar"><div><h1>重复结果</h1></div></div><div class="card empty">尚未选择项目</div>`;
  }
  const filter = { ...state.groupFilter, status: state.groupFilter.status || 'pending' };
  state.groups = (await api.ListGroups(p.id, filter)) || [];
  const sum = p.lastSummary || {};

  const sortBy = state.groupFilter.sort || 'id';
  const sorted = state.groups.slice().sort((a, b) => {
    if (sortBy === 'size') return (b.reclaimableBytes || 0) - (a.reclaimableBytes || 0);
    if (sortBy === 'members') return (b.memberCount || 0) - (a.memberCount || 0);
    if (sortBy === 'type') return String(a.groupType).localeCompare(String(b.groupType));
    return a.groupId - b.groupId;
  });
  state.groups = sorted;

  const rows = state.groups.map((g) => `
    <tr class="clickable" data-group="${g.groupId}">
      <td><input type="checkbox" class="checkbox row-check" data-group="${g.groupId}" /></td>
      <td>#${g.groupId}</td>
      <td>
        <div style="display:flex;gap:10px;align-items:center">
          ${g.coverThumbUrl ? `<img class="thumb" src="${esc(g.coverThumbUrl)}" alt="" />` : `<div class="thumb"></div>`}
          <div>
            <b>${esc(g.typeLabel || typeLabel(g.groupType))}</b>
            <div class="muted mono">${g.memberCount} 个成员 · 置信度 ${g.confidence.toFixed(2)}</div>
          </div>
        </div>
      </td>
      <td>${esc(g.status || 'pending')}</td>
      <td class="mono">${fmtBytes(g.reclaimableBytes)}</td>
      <td><button class="btn" data-open-group="${g.groupId}">打开</button></td>
    </tr>`).join('');

  const empty = !state.groups.length
    ? `<div class="card empty"><h2>${filter.status === 'pending' ? '没有待处理的重复组' : '没有匹配的分组'}</h2><div>可切换筛选，或先执行扫描。所有删除均需人工确认。</div></div>`
    : '';

  return `
    <div class="toolbar">
      <div>
        <h1>重复结果</h1>
        <div class="sub">${esc(p.name)} · ${sum.groups || 0} 组 · 预计可释放 ${fmtBytes(sum.reclaimableBytes || 0)}</div>
      </div>
      <div class="toolbar-actions">
        <button class="btn" id="btnIgnoreChecked">忽略勾选组</button>
        <button class="btn" id="btnRescanResults">重新扫描</button>
        <button class="btn" id="btnExport">导出报告</button>
      </div>
    </div>
    <div class="info-banner">算法说明：精确重复 = SHA-256；视觉近似 = pHash 汉明距离 / 视频 FFmpeg 多帧 pHash。推荐保留基于分辨率/大小/编码启发式质量分，仍需人工确认。</div>
    <div class="filters">
      <input type="search" id="resultSearch" placeholder="搜索路径…" value="${esc(filter.search)}" />
      <select id="statusFilter">
        <option value="pending" ${filter.status === 'pending' ? 'selected' : ''}>待处理</option>
        <option value="ignored" ${filter.status === 'ignored' ? 'selected' : ''}>已忽略</option>
        <option value="processed" ${filter.status === 'processed' ? 'selected' : ''}>已处理</option>
        <option value="all" ${filter.status === 'all' ? 'selected' : ''}>全部</option>
      </select>
      <select id="typeFilter">
        <option value="">全部类型</option>
        <option value="exact" ${filter.groupType === 'exact' ? 'selected' : ''}>精确重复</option>
        <option value="similar_image" ${filter.groupType === 'similar_image' ? 'selected' : ''}>近似图片</option>
        <option value="similar_video" ${filter.groupType === 'similar_video' ? 'selected' : ''}>近似视频</option>
        <option value="similar_text" ${filter.groupType === 'similar_text' ? 'selected' : ''}>近似 TXT</option>
      </select>
      <select id="sortFilter">
        <option value="id" ${sortBy === 'id' ? 'selected' : ''}>按组号</option>
        <option value="size" ${sortBy === 'size' ? 'selected' : ''}>按可释放空间</option>
        <option value="members" ${sortBy === 'members' ? 'selected' : ''}>按成员数</option>
        <option value="type" ${sortBy === 'type' ? 'selected' : ''}>按类型</option>
      </select>
      <button class="btn" id="btnApplyFilter">应用筛选</button>
    </div>
    ${empty}
    ${state.groups.length ? `
      <table class="data">
        <thead><tr><th></th><th>组</th><th>内容</th><th>状态</th><th>可释放</th><th></th></tr></thead>
        <tbody>${rows}</tbody>
      </table>` : ''}
    <div class="safety">删除默认操作：<b>移入系统回收站</b>。永久删除需在设置中开启并在确认弹窗勾选。</div>`;
}

async function renderGroupDetail() {
  const g = state.currentGroup;
  const p = currentProject();
  if (!g || !p) {
    return `<div class="card empty">组详情加载失败</div>`;
  }
  // enrich frame urls for videos
  const items = [];
  for (const it of g.items) {
    let frames = [];
    if (it.mediaType === 'video') {
      frames = (await api.GetFrameURLs(p.id, it.fileId)) || [];
    }
    items.push({ ...it, frames });
  }
  const members = items.map((it) => {
    const hd = it.hammingDistance != null ? ` · 汉明距离 ${it.hammingDistance}` : '';
    return `
      <div class="member ${it.isRecommended ? 'recommended' : ''}">
        <div>
          ${it.thumbUrl ? `<img class="thumb" src="${esc(it.thumbUrl)}" alt="" />` : `<div class="thumb"></div>`}
          <label class="check-row" style="margin-top:8px">
            <input type="checkbox" class="checkbox member-check" data-file="${it.fileId}" data-path="${esc(it.path)}"
              ${it.isRecommended ? 'disabled' : ''} ${state.selected.has(String(it.fileId)) ? 'checked' : ''}/>
            <span>${it.isRecommended ? '保留' : '删除'}</span>
          </label>
        </div>
        <div>
          ${it.isRecommended ? `<span class="badge badge-ok">推荐保留</span>` : `<span class="badge badge-warn">副本候选</span>`}
          ${!it.exists ? `<span class="badge badge-pending">文件缺失</span>` : ''}
          <div class="path" style="margin-top:6px">${esc(it.path)}</div>
          <div class="muted" style="margin-top:4px">
            ${fmtBytes(it.sizeBytes)} · ${it.width ? `${it.width}×${it.height}` : ''}
            ${it.durationMs ? ` · ${(it.durationMs / 1000).toFixed(1)}s` : ''}
            ${hd}
            ${it.mediaHashHex ? ` · pHash <span class="mono">${esc(it.mediaHashHex.slice(0, 16))}…</span>` : ''}
          </div>
          ${it.reasons?.length ? `<div class="muted" style="margin-top:4px">${esc(it.reasons.join(' · '))}</div>` : ''}
          ${it.frames?.length ? `<div class="frames">${it.frames.map((f) => `<img src="${esc(f)}" alt="frame" />`).join('')}</div>` : ''}
        </div>
        <div class="side row-actions">
          <button class="btn" data-open-file="${esc(it.path)}">打开</button>
          <button class="btn" data-reveal="${esc(it.path)}">定位</button>
          <button class="btn" data-copy="${esc(it.path)}">复制</button>
          ${!it.isRecommended ? `<button class="btn" data-set-rec="${it.fileId}">设为推荐保留</button>` : ''}
        </div>
      </div>`;
  }).join('');

  const statusBadge = g.status === 'processed' ? 'badge-ok' : g.status === 'ignored' ? 'badge-warn' : 'badge-info';
  return `
    <div class="toolbar">
      <div>
        <h1>组 #${g.groupId} · ${esc(g.typeLabel || typeLabel(g.groupType))}</h1>
        <div class="sub">${esc(p.name)} · 置信度 ${g.confidence.toFixed(2)} · <span class="badge ${statusBadge}">${esc(g.status)}</span></div>
      </div>
      <div class="toolbar-actions">
        <button class="btn" id="btnBackResults">← 返回结果</button>
        <button class="btn" id="btnIgnoreGroup">${g.status === 'ignored' ? '取消忽略' : '忽略此组'}</button>
        <button class="btn" id="btnMarkProcessed">标记已处理</button>
      </div>
    </div>
    <div class="info-banner">推荐保留基于质量启发式（分辨率/体积/编码等）。近似成员展示相对推荐保留的 <b>pHash 汉明距离</b>。视频帧由 <b>FFmpeg 抽帧</b>。</div>
    <div class="safety">预计可释放 <b>${fmtBytes(g.reclaimableBytes || 0)}</b>。默认删除 = 移入系统回收站，可还原。</div>
    ${members}
    <div class="footer-bar">
      <div class="muted">已选删除 <b id="selCount">${state.selected.size}</b> 个文件 · 本机离线运行</div>
      <div class="row-actions">
        <button class="btn btn-danger" id="btnDeleteSelected">移入系统回收站</button>
      </div>
    </div>`;
}

async function renderSettings() {
  const s = state.settings || (await api.GetSettings());
  state.settings = s;
  return `
    <div class="toolbar">
      <div>
        <h1>全局设置</h1>
        <div class="sub">默认扫描参数与删除策略。永久删除默认关闭。</div>
      </div>
      <div class="toolbar-actions"><button class="btn btn-primary" id="btnSaveSettings">保存设置</button></div>
    </div>
    ${!s.ffmpegAvailable ? `<div class="warn-banner">未检测到 FFmpeg / ffprobe。视频近似与帧条预览不可用；图片检测正常。</div>` : `<div class="success-banner">FFmpeg / ffprobe 已就绪。</div>`}
    <div class="card form">
      <div class="form-row"><label>默认相似度阈值</label><input type="number" id="setThreshold" step="0.01" min="0.5" max="0.99" value="${s.defaultThreshold}" /></div>
      <div class="form-row"><label>默认视频抽帧数</label><input type="number" id="setFrames" min="1" max="32" value="${s.defaultFrameCount}" /></div>
      <div class="form-row"><label>默认图片并发</label><input type="number" id="setWorkers" min="1" max="16" value="${s.defaultWorkers}" /></div>
      <div class="form-row"><label>默认视频并发</label><input type="number" id="setVWorkers" min="1" max="8" value="${s.defaultVideoWorkers}" /></div>
      <div class="form-row"><label>默认 TXT 并发</label><input type="number" id="setTextWorkers" min="1" max="16" value="${s.defaultTextWorkers || 8}" /></div>
      <div class="check-row"><input type="checkbox" class="checkbox" id="setVideos" ${s.defaultIncludeVideos ? 'checked' : ''}/> 默认启用视频</div>
      <div class="check-row"><input type="checkbox" class="checkbox" id="setTexts" ${s.defaultIncludeTexts !== false ? 'checked' : ''}/> 默认启用 TXT</div>
      <div class="form-row"><label>默认 TXT 相似度</label><input type="number" id="setTextThreshold" min="0.8" max="0.99" step="0.01" value="${s.defaultTextThreshold || 0.92}" /></div>
      <div class="form-row">
        <label>默认删除模式</label>
        <select id="setDeleteMode">
          <option value="recycle" ${s.defaultDeleteMode === 'recycle' ? 'selected' : ''}>移入系统回收站（推荐）</option>
          <option value="permanent" ${s.defaultDeleteMode === 'permanent' ? 'selected' : ''}>永久删除</option>
        </select>
      </div>
      <div class="check-row">
        <input type="checkbox" class="checkbox" id="setAllowPerm" ${s.allowPermanentDelete ? 'checked' : ''}/>
        <span>允许永久删除（需额外确认，文件无法从回收站还原）</span>
      </div>
      <div class="form-row"><label>FFmpeg 路径（可选，空则用 PATH）</label><input type="text" id="setFFmpeg" value="${esc(s.ffmpegPath || '')}" placeholder="例如 /usr/local/bin/ffmpeg" /></div>
      <div class="form-row"><label>ffprobe 路径（可选）</label><input type="text" id="setFFprobe" value="${esc(s.ffprobePath || '')}" /></div>
      <div class="muted">应用数据目录：${esc(state.appInfo?.appData || '')}</div>
    </div>`;
}

function bindMain() {
  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

  // Project list
  $('#btnNewProject')?.addEventListener('click', openCreateProjectModal);
  $('#btnNewProjectEmpty')?.addEventListener('click', openCreateProjectModal);
  $$('[data-act="open"]').forEach((b) => b.addEventListener('click', async () => {
    state.currentProjectId = b.dataset.id;
    state.selected.clear();
    await navigate('results');
  }));
  $$('[data-act="rescan"]').forEach((b) => b.addEventListener('click', async () => {
    state.currentProjectId = b.dataset.id;
    try { await api.StartScan(b.dataset.id); toast('扫描已启动', 'ok'); await navigate('progress'); startPoll(); } catch (e) { toast(String(e.message || e), 'error'); }
  }));
  $$('[data-act="edit"]').forEach((b) => b.addEventListener('click', async () => {
    state.currentProjectId = b.dataset.id; await navigate('config');
  }));
  $$('[data-act="delete-project"]').forEach((b) => b.addEventListener('click', async () => {
    const ok = await showConfirm({
      title: '删除项目？',
      body: '将删除该项目的 SQLite 缓存与缩略图数据目录，<b>不会删除</b>任何媒体文件。此操作不可撤销。',
      confirmText: '删除项目',
      danger: true,
    });
    if (!ok) return;
    try { await api.DeleteProject(b.dataset.id); toast('项目已删除', 'ok'); await navigate('projects'); } catch (e) { toast(String(e.message || e), 'error'); }
  }));

  // Config
  $('#btnAddPath')?.addEventListener('click', async () => {
    const list = $('#pathList');
    const i = list.querySelectorAll('input').length;
    const div = document.createElement('div');
    div.style.cssText = 'display:flex;gap:8px;align-items:center;margin-bottom:6px';
    div.innerHTML = `<input type="text" data-path-index="${i}" value="" placeholder="绝对路径" /><span class="badge badge-warn">待校验</span>`;
    list.appendChild(div);
  });
  $('#btnSaveConfig')?.addEventListener('click', saveConfig);
  $('#btnStartScan')?.addEventListener('click', async () => {
    await saveConfig();
    try {
      await api.StartScan(state.currentProjectId);
      toast('扫描已启动', 'ok');
      await navigate('progress');
      startPoll();
    } catch (e) { toast(String(e.message || e), 'error'); }
  });

  // Progress
  $('#btnCancelScan')?.addEventListener('click', async () => {
    try { await api.CancelScan(state.currentProjectId); toast('正在取消…'); } catch (e) { toast(String(e.message || e), 'error'); }
  });
  $('#btnStartScanFromProgress')?.addEventListener('click', async () => {
    try { await api.StartScan(state.currentProjectId); toast('扫描已启动', 'ok'); startPoll(); await navigate('progress'); } catch (e) { toast(String(e.message || e), 'error'); }
  });
  $('#btnGoResults')?.addEventListener('click', () => navigate('results'));

  // Results
  $('#btnApplyFilter')?.addEventListener('click', () => {
    state.groupFilter.search = $('#resultSearch')?.value || '';
    state.groupFilter.status = $('#statusFilter')?.value || 'pending';
    state.groupFilter.groupType = $('#typeFilter')?.value || '';
    state.groupFilter.sort = $('#sortFilter')?.value || 'id';
    navigate('results');
  });
  $('#btnIgnoreChecked')?.addEventListener('click', async () => {
    const ids = $$('.row-check').filter((c) => c.checked).map((c) => Number(c.dataset.group));
    if (!ids.length) { toast('请先勾选组', 'error'); return; }
    try {
      for (const id of ids) await api.SetGroupStatus(state.currentProjectId, id, 'ignored');
      toast(`已忽略 ${ids.length} 组`, 'ok');
      await navigate('results');
    } catch (e) { toast(String(e.message || e), 'error'); }
  });
  $$('[data-open-group]').forEach((b) => b.addEventListener('click', (e) => {
    e.stopPropagation(); openGroup(Number(b.dataset.openGroup));
  }));
  $$('tr[data-group]').forEach((tr) => tr.addEventListener('click', (e) => {
    if (e.target.closest('input,button')) return;
    openGroup(Number(tr.dataset.group));
  }));
  $('#btnRescanResults')?.addEventListener('click', async () => {
    try { await api.StartScan(state.currentProjectId); toast('扫描已启动', 'ok'); await navigate('progress'); startPoll(); } catch (e) { toast(String(e.message || e), 'error'); }
  });
  $('#btnExport')?.addEventListener('click', openExportModal);

  // Group detail
  $('#btnBackResults')?.addEventListener('click', () => navigate('results'));
  $('#btnIgnoreGroup')?.addEventListener('click', async () => {
    const g = state.currentGroup;
    const next = g.status === 'ignored' ? 'pending' : 'ignored';
    try {
      await api.SetGroupStatus(state.currentProjectId, g.groupId, next);
      g.status = next;
      toast(next === 'ignored' ? '已忽略该组' : '已取消忽略', 'ok');
      await navigate('group');
    } catch (e) { toast(String(e.message || e), 'error'); }
  });
  $('#btnMarkProcessed')?.addEventListener('click', async () => {
    const g = state.currentGroup;
    try {
      await api.SetGroupStatus(state.currentProjectId, g.groupId, 'processed');
      g.status = 'processed';
      toast('已标记为已处理', 'ok');
      await navigate('group');
    } catch (e) { toast(String(e.message || e), 'error'); }
  });
  $$('.member-check').forEach((chk) => chk.addEventListener('change', () => {
    const id = String(chk.dataset.file);
    if (chk.checked) state.selected.add(id); else state.selected.delete(id);
    const el = $('#selCount'); if (el) el.textContent = String(state.selected.size);
  }));
  $$('[data-open-file]').forEach((b) => b.addEventListener('click', async () => {
    try { await api.OpenPath(b.dataset.openFile); } catch (e) { toast(String(e.message || e), 'error'); }
  }));
  $$('[data-reveal]').forEach((b) => b.addEventListener('click', async () => {
    try { await api.RevealInFolder(b.dataset.reveal); } catch (e) { toast(String(e.message || e), 'error'); }
  }));
  $$('[data-copy]').forEach((b) => b.addEventListener('click', async () => {
    try { await api.CopyToClipboard(b.dataset.copy); toast('已复制路径', 'ok'); } catch (e) { toast(String(e.message || e), 'error'); }
  }));
  $$('[data-set-rec]').forEach((b) => b.addEventListener('click', async () => {
    try {
      await api.SetRecommended(state.currentProjectId, state.currentGroup.groupId, Number(b.dataset.setRec));
      await openGroup(state.currentGroup.groupId);
      toast('已更新推荐保留', 'ok');
    } catch (e) { toast(String(e.message || e), 'error'); }
  }));
  $('#btnDeleteSelected')?.addEventListener('click', confirmDeleteSelected);

  // Settings
  $('#btnSaveSettings')?.addEventListener('click', async () => {
    const dto = {
      defaultThreshold: Number($('#setThreshold').value) || 0.8,
      defaultIncludeVideos: $('#setVideos').checked,
      defaultIncludeTexts: $('#setTexts').checked,
      defaultTextThreshold: Number($('#setTextThreshold').value) || 0.92,
      defaultTextWorkers: Number($('#setTextWorkers').value) || 8,
      defaultFrameCount: Number($('#setFrames').value) || 8,
      defaultWorkers: Number($('#setWorkers').value) || 2,
      defaultVideoWorkers: Number($('#setVWorkers').value) || 1,
      defaultDeleteMode: $('#setDeleteMode').value,
      allowPermanentDelete: $('#setAllowPerm').checked,
      ffmpegPath: $('#setFFmpeg').value.trim(),
      ffprobePath: $('#setFFprobe').value.trim(),
    };
    if (dto.defaultDeleteMode === 'permanent' && !dto.allowPermanentDelete) {
      const ok = await showConfirm({
        title: '将永久删除设为默认？',
        body: '永久删除的文件无法从回收站还原。建议保持「移入系统回收站」。',
        confirmText: '仍然设为永久删除',
        danger: true,
      });
      if (!ok) return;
    }
    try { await api.SaveSettings(dto); state.settings = dto; toast('设置已保存', 'ok'); await navigate('settings'); } catch (e) { toast(String(e.message || e), 'error'); }
  });
}

async function saveConfig() {
  const p = currentProject();
  if (!p) return;
  const paths = $$('#pathList input[type="text"]').map((i) => i.value.trim()).filter(Boolean);
  const patch = {
    name: $('#cfgName').value.trim() || p.name,
    paths,
    threshold: Number($('#cfgThreshold').value) || 0.8,
    includeImages: $('#cfgImages').checked,
    includeVideos: $('#cfgVideos').checked,
    includeTexts: $('#cfgTexts').checked,
    textThreshold: Number($('#cfgTextThreshold').value) || 0.92,
    textWorkers: Number($('#cfgTextWorkers')?.value) || 8,
    frameCount: Number($('#cfgFrames').value) || 8,
    workers: Number($('#cfgWorkers').value) || 2,
    videoWorkers: Number($('#cfgVWorkers').value) || 1,
    recursive: $('#cfgRecursive').checked,
    enableThumbs: $('#cfgThumbs').checked,
  };
  try {
    const updated = await api.UpdateProject(p.id, patch);
    Object.assign(p, updated);
    toast('配置已保存', 'ok');
  } catch (e) {
    toast(String(e.message || e), 'error');
    throw e;
  }
}

async function openCreateProjectModal() {
  const root = document.getElementById('modalRoot');
  let recent = [];
  try { recent = (await api.ListRecentDirs()) || []; } catch { recent = []; }
  const recentHtml = recent.length
    ? `<div class="form-row"><label>最近目录</label><div class="path-chips">${recent.map((d) => `<button type="button" class="chip" data-recent="${esc(d)}">${esc(d)}</button>`).join('')}</div></div>`
    : '';
  root.hidden = false;
  root.innerHTML = `
    <div class="modal">
      <h3>新建项目</h3>
      <p>项目是本机离线任务容器：一组扫描目录 + 去重参数 + 独立 SQLite 缓存。</p>
      <div class="form">
        <div class="form-row"><label>项目名称</label><input type="text" id="npName" placeholder="例如：2024 家庭照片" /></div>
        ${recentHtml}
        <div class="form-row"><label>扫描目录（每行一个绝对路径）</label><textarea id="npPaths" placeholder="/Users/you/Pictures&#10;/Users/you/Movies"></textarea>
          <button type="button" class="btn" id="npSelectDirectory">选择目录</button>
          <div class="hint">可先填目录；无效路径会在配置页标红。</div>
        </div>
        <div class="check-row"><input type="checkbox" class="checkbox" id="npImages" checked /> 启用图片 pHash</div>
        <div class="check-row"><input type="checkbox" class="checkbox" id="npVideos" ${state.settings?.defaultIncludeVideos !== false ? 'checked' : ''}/> 启用视频 FFmpeg 抽帧</div>
      </div>
      <div class="actions">
        <button class="btn" id="npCancel">取消</button>
        <button class="btn btn-primary" id="npOk">创建</button>
      </div>
    </div>`;
  root.querySelectorAll('[data-recent]').forEach((btn) => {
    btn.onclick = () => {
      const ta = root.querySelector('#npPaths');
      const lines = ta.value.split(/\r?\n/).map((s) => s.trim()).filter(Boolean);
      if (!lines.includes(btn.dataset.recent)) lines.push(btn.dataset.recent);
      ta.value = lines.join('\n');
    };
  });
  root.querySelector('#npSelectDirectory').onclick = async () => {
    try {
      const path = await api.SelectDirectory();
      if (!path) return;
      const ta = root.querySelector('#npPaths');
      const lines = ta.value.split(/\r?\n/).map((s) => s.trim()).filter(Boolean);
      if (!lines.includes(path)) lines.push(path);
      ta.value = lines.join('\n');
    } catch (e) {
      toast(String(e.message || e), 'error');
    }
  };
  const close = () => { root.hidden = true; root.innerHTML = ''; };
  root.querySelector('#npCancel').onclick = close;
  root.querySelector('#npOk').onclick = async () => {
    const name = root.querySelector('#npName').value.trim();
    const paths = root.querySelector('#npPaths').value.split(/\r?\n/).map((s) => s.trim()).filter(Boolean);
    if (!name) { toast('请填写项目名称', 'error'); return; }
    if (!paths.length) { toast('请至少填写一个目录', 'error'); return; }
    try {
      const p = await api.CreateProject({
        name, paths, recursive: true, threshold: state.settings?.defaultThreshold || 0.8,
        includeImages: root.querySelector('#npImages').checked,
        includeVideos: root.querySelector('#npVideos').checked,
        includeTexts: state.settings?.defaultIncludeTexts !== false,
        textThreshold: state.settings?.defaultTextThreshold || 0.92,
        textWorkers: state.settings?.defaultTextWorkers || 8,
        frameCount: state.settings?.defaultFrameCount || 8,
        workers: state.settings?.defaultWorkers || 2,
        videoWorkers: state.settings?.defaultVideoWorkers || 1,
        enableThumbs: true,
      });
      // enableThumbs default true if false zero — force true for new projects when user didn't care
      state.currentProjectId = p.id;
      close();
      toast('项目已创建', 'ok');
      await navigate('config');
    } catch (e) {
      toast(String(e.message || e), 'error');
    }
  };
}

async function openGroup(groupId) {
  try {
    const g = await api.GetGroup(state.currentProjectId, groupId);
    state.currentGroup = g;
    state.selected = new Set(
      (g.items || []).filter((it) => !it.isRecommended && it.action === 'cleanup_candidate').map((it) => String(it.fileId)),
    );
    // default: don't auto-select; leave empty for safety unless action says cleanup
    state.selected = new Set();
    await navigate('group');
  } catch (e) {
    toast(String(e.message || e), 'error');
  }
}

async function confirmDeleteSelected() {
  const g = state.currentGroup;
  const p = currentProject();
  if (!g || !p) return;
  const items = g.items.filter((it) => state.selected.has(String(it.fileId)));
  if (!items.length) { toast('请先勾选要删除的文件', 'error'); return; }
  if (items.some((it) => it.isRecommended)) { toast('不能删除推荐保留文件', 'error'); return; }

  const st = state.settings || (await api.GetSettings());
  const mode = st.defaultDeleteMode || 'recycle';
  const permanent = mode === 'permanent';
  if (permanent && !st.allowPermanentDelete) {
    toast('永久删除未在设置中开启', 'error');
    return;
  }
  const list = items.map((it) => `<div class="path">• ${esc(it.path)}</div>`).join('');
  const ok = await showConfirm({
    title: permanent ? '确认永久删除选中的重复媒体文件？' : '确认将选中的重复媒体文件移入系统回收站？',
    body: `项目「${esc(p.name)}」组 #${g.groupId}，共 ${items.length} 个文件。<br/><br/>${list}<br/><br/>${permanent
      ? '<b style="color:#b91c1c">永久删除后无法从回收站还原。</b>'
      : '将移入系统回收站，可还原。不会删除推荐保留文件。'}`,
    confirmText: permanent ? '永久删除' : '移入系统回收站',
    danger: true,
    checkbox: permanent ? '我已理解无法恢复' : null,
  });
  if (!ok) return;
  try {
    const res = await api.DeleteFiles({
      projectId: p.id,
      mode,
      paths: items.map((it) => it.path),
      groupIds: [g.groupId],
      fileIds: items.map((it) => it.fileId),
    });
    const msg = `成功 ${res.succeeded?.length || 0} · 失败 ${res.failed?.length || 0}`;
    if (res.failed?.length) {
      toast(msg + '：' + res.failed[0].message, 'error');
    } else {
      toast('已' + (permanent ? '永久删除 ' : '移入回收站 ') + msg, 'ok');
    }
    // refresh group
    await openGroup(g.groupId);
    await refreshProjects();
  } catch (e) {
    toast(String(e.message || e), 'error');
  }
}

async function openExportModal() {
  const p = currentProject();
  if (!p) return;
  const root = document.getElementById('modalRoot');
  root.hidden = false;
  root.innerHTML = `
    <div class="modal">
      <h3>导出去重清单</h3>
      <p>生成本地只读报告，不会修改或删除任何媒体文件。</p>
      <div class="form">
        <div class="form-row">
          <label>格式</label>
          <select id="expFormat">
            <option value="json">JSON 结构化数据</option>
            <option value="html">HTML 本地离线审阅报告</option>
            <option value="txt">TXT 纯文本路径清单</option>
          </select>
        </div>
        <div class="form-row">
          <label>范围</label>
          <select id="expScope">
            <option value="pending">仅待处理组</option>
            <option value="all">全部分组（含忽略/已处理）</option>
          </select>
        </div>
      </div>
      <div class="actions">
        <button class="btn" id="expCancel">取消</button>
        <button class="btn btn-primary" id="expOk">导出</button>
      </div>
    </div>`;
  const close = () => { root.hidden = true; root.innerHTML = ''; };
  root.querySelector('#expCancel').onclick = close;
  root.querySelector('#expOk').onclick = async () => {
    const format = root.querySelector('#expFormat').value;
    const scope = root.querySelector('#expScope').value;
    try {
      const path = await api.ExportReport(p.id, format, scope);
      close();
      toast('已导出：' + path, 'ok');
    } catch (e) {
      toast(String(e.message || e), 'error');
    }
  };
}

let pollTimer = null;
function startPoll() {
  stopPoll();
  pollTimer = setInterval(async () => {
    if (!state.currentProjectId) return;
    try {
      const st = await api.GetScanStatus(state.currentProjectId);
      const prevStage = state.scanStatus?.event?.stage;
      state.scanStatus = st;
      if (st.event) state.scanEvent = st.event;
      if (state.view === 'progress') {
        // light refresh
        const main = document.getElementById('main');
        main.innerHTML = await renderProgress();
        bindMain();
      }
      if (!st.running) {
        if (st.event?.stage === 'done' || st.canceled || st.event?.stage === 'canceled') {
          await refreshProjects();
          setPendingBadge();
        }
        stopPoll();
      }
      if (st.event?.stage === 'done' && prevStage !== 'done') {
        toast('扫描完成', 'ok');
      }
    } catch {
      /* ignore */
    }
  }, 800);
}
function stopPoll() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
}

async function boot() {
  // nav
  document.getElementById('nav').addEventListener('click', (e) => {
    const btn = e.target.closest('.nav-item');
    if (!btn) return;
    navigate(btn.dataset.view);
  });

  // Wails events
  if (hasWails && window.runtime) {
    window.runtime.EventsOn('scan:progress', (ev) => {
      state.scanEvent = ev;
      if (state.view === 'progress') {
        // handled by poll mostly; light update possible
      }
    });
    window.runtime.EventsOn('scan:finished', async (payload) => {
      toast(`扫描完成：${payload.groups} 组 / ${payload.filesSeen} 文件`, 'ok');
      await refreshProjects();
      setPendingBadge();
      if (state.view === 'progress') { document.getElementById('main').innerHTML = await renderProgress(); bindMain(); }
    });
    window.runtime.EventsOn('scan:canceled', async () => {
      toast('扫描已取消，缓存已保留');
      if (state.view === 'progress') { document.getElementById('main').innerHTML = await renderProgress(); bindMain(); }
    });
    window.runtime.EventsOn('scan:error', async (payload) => {
      toast('扫描错误：' + payload.message, 'error');
      if (state.view === 'progress') { document.getElementById('main').innerHTML = await renderProgress(); bindMain(); }
    });
    window.runtime.EventsOn('files:deleted', () => refreshProjects());
  }

  state.appInfo = await api.AppReady();
  state.settings = await api.GetSettings();
  document.getElementById('ffmpegStatus').textContent = state.appInfo.ffmpeg ? '已就绪' : '未安装';
  document.getElementById('deleteModeFoot').textContent = state.settings.defaultDeleteMode === 'permanent' ? '永久删除' : '回收站';
  document.getElementById('appDataPath').textContent = state.appInfo.appData || '';
  await refreshProjects();
  await navigate('projects');
}

boot().catch((e) => {
  console.error(e);
  toast(String(e.message || e), 'error');
});
