import * as React from "react"
import { Check, ChevronLeft, FolderOpen, Loader2, Moon, Monitor, Plus, RefreshCw, Search, Settings, Sun, Trash2, Upload, X, Zap } from "lucide-react"
import { api, runtime, type AppInfo, type DeleteResult, type DownloadProgress, type Group, type GroupDetail, type Item, type Project, type ScanEvent, type ScanError, type ScanStatus, type Settings as AppSettings, type ThemePreference, type UpdateInfo, type View } from "./api"
import { Badge, Button, Card, Checkbox, Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, Input, Label, Progress, Switch } from "xwang-ui"

const stages = [{ key: "discover", label: "发现文件" }, { key: "exact", label: "精确哈希 (SHA-256)" }, { key: "image", label: "图片感知哈希 (pHash)" }, { key: "video", label: "视频抽帧 (FFmpeg)" }, { key: "text", label: "TXT 文本与书名分析" }, { key: "match", label: "分组匹配" }]
const typeLabel = (value: string) => ({ exact: "精确重复", similar_image: "视觉近似图片", similar_video: "视觉近似视频", similar_text: "TXT 正文近似（待核对）", text_equal: "TXT 文本一致", text_contained: "TXT 内容覆盖（待核对）", text_name: "TXT 书名关联（待核对）" }[value] || value)
const statusLabel = (value: string) => ({ pending: "待处理", ignored: "已忽略", processed: "已处理" }[value] || value)
const bytes = (n = 0) => { if (!n) return "0 B"; const units = ["B", "KB", "MB", "GB", "TB"]; const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1); return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${units[i]}` }
const candidateFileIds = (items: Pick<Item, "fileId" | "isRecommended" | "action" | "exists">[]) => new Set(items.filter((x) => !x.isRecommended && x.exists && x.action === "cleanup_candidate").map((x) => x.fileId))
const isTextAnalysis = (type: string) => ["similar_text", "text_contained", "text_name"].includes(type)
// Must stay within the server's max (500); the list is paged, never truncated
// silently, so "no more rows" and "one page loaded" look different to the user.
const GROUP_PAGE_SIZE = 100
const GROUP_PAGE_MAX = 500
const message = (error: unknown) => error instanceof Error ? error.message : String(error)
// Row-level selection must not hijack clicks aimed at a nested control.
const isRowTarget = (target: EventTarget | null) => !(target instanceof Element && Boolean(target.closest("button,a,input,label")))
// A negative similarity means the two files were never compared directly; show
// a dash instead of a fabricated percentage.
const similarityText = (value: number) => (value < 0 ? "—" : `${Math.round(value * 100)}%`)

function useTheme() {
  const [preference, setPreference] = React.useState<ThemePreference>(() => (localStorage.getItem("media-dedupe.theme") as ThemePreference) || "system")
  const [resolved, setResolved] = React.useState("light")
  React.useEffect(() => { let cancelled = false; const sync = async () => { const next = preference === "system" ? ((await api.GetSystemAppearance().catch(() => "")) || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light")) : preference; if (!cancelled) { setResolved(next); document.documentElement.classList.toggle("dark", next === "dark"); document.documentElement.style.colorScheme = next } }; void sync(); localStorage.setItem("media-dedupe.theme", preference); return () => { cancelled = true } }, [preference])
  return { preference, resolved, setPreference }
}

function useAppEvents(projectId: string | null, onScan: (event: ScanEvent) => void, onRefresh: () => void, onUpdate: (event: string, payload: any) => void) {
  React.useEffect(() => { const unsub: (() => void)[] = []; const rt = runtime(); if (!rt?.EventsOn) return; const scoped = (name: string, cb: (payload: any) => void) => unsub.push(rt.EventsOn!(name, cb)); scoped("scan:progress", (payload) => { if (!projectId || payload?.projectId === projectId) onScan(payload) }); scoped("scan:finished", (payload) => { if (!projectId || payload?.projectId === projectId) onRefresh() }); scoped("scan:canceled", (payload) => { if (!projectId || payload?.projectId === projectId) onRefresh() }); scoped("scan:error", (payload) => { if (!projectId || payload?.projectId === projectId) onRefresh() }); scoped("files:deleted", onRefresh); ["update:available", "update:progress", "update:done", "update:failed"].forEach((name) => scoped(name, (payload) => onUpdate(name, payload))); return () => unsub.forEach((fn) => fn()) }, [projectId, onScan, onRefresh, onUpdate])
}

function ConfirmDialog({ open, title, body, danger = false, checkbox, confirmLabel = "确认", retryLabel, onRetry, onConfirm, onClose }: { open: boolean; title: string; body: React.ReactNode; danger?: boolean; checkbox?: string; confirmLabel?: string; retryLabel?: string; onRetry?: () => void; onConfirm: () => void; onClose: () => void }) {
  const [checked, setChecked] = React.useState(false)
  React.useEffect(() => { if (!open) setChecked(false) }, [open])
  return <Dialog open={open} onOpenChange={(value) => !value && onClose()}><DialogContent><DialogHeader><DialogTitle>{title}</DialogTitle><DialogDescription asChild><div className="text-sm text-muted">{body}</div></DialogDescription></DialogHeader>{checkbox && <label className="flex items-center gap-2 text-sm"><Checkbox checked={checked} onCheckedChange={(v) => setChecked(v === true)} />{checkbox}</label>}<DialogFooter>{onRetry && retryLabel ? <Button variant="secondary" onClick={onRetry}>{retryLabel}</Button> : <Button variant="secondary" onClick={onClose}>取消</Button>}<Button variant={danger ? "danger" : "default"} disabled={Boolean(checkbox) && !checked} onClick={onConfirm}>{confirmLabel}</Button></DialogFooter></DialogContent></Dialog>
}

// DeleteOutcome reports what a delete actually did. Files that were not removed are
// still on disk and their groups stay pending, so this is a state the user must be
// able to read and retry, not a success message.
function DeleteOutcome({ result }: { result: DeleteResult }) {
  return <div className="space-y-2">
    {result.failed.length > 0 && <>
      <p>{result.succeeded.length ? `已删除 ${result.succeeded.length} 个；` : "没有文件被删除："}{result.failed.length} 个未通过校验或删除失败，仍在原位置：</p>
      <ul className="max-h-48 space-y-1 overflow-auto">{result.failed.map((failure) => <li key={failure.path} className="break-all font-mono text-xs">{failure.path}<div className="text-muted">{failure.message}</div></li>)}</ul>
      <p>对应的组仍留在「待处理」里，重试只会再提交这些失败项。</p>
    </>}
    {result.metadataErrors?.length ? <>
      <p>文件已经删除，但结果更新失败，需要重新扫描才能让列表与磁盘一致：</p>
      <ul className="max-h-48 space-y-1 overflow-auto">{result.metadataErrors.map((item, index) => <li key={index} className="break-all text-xs">{item}</li>)}</ul>
    </> : null}
  </div>
}

function Shell({ view, setView, projects, current, setCurrent, info, settings, theme, children }: { view: View; setView: (v: View) => void; projects: Project[]; current: string | null; setCurrent: (id: string) => void; info: AppInfo | null; settings: AppSettings | null; theme: ReturnType<typeof useTheme>; children: React.ReactNode }) {
  const nav: [View, string, React.ReactNode][] = [["projects", "项目列表", <FolderOpen key="p" />], ["config", "扫描配置", <Settings key="c" />], ["progress", "扫描进度", <Zap key="g" />], ["results", "重复结果", <Search key="r" />], ["settings", "全局设置", <Settings key="s" />]]
  const themeLabel = theme.preference === "system" ? "跟随系统" : theme.preference === "dark" ? "暗色模式" : "亮色模式"
  const ThemeIcon = theme.preference === "light" ? Sun : theme.preference === "dark" ? Moon : Monitor
  return <div className="flex h-screen min-w-0 bg-background text-foreground"><aside className="hidden w-60 shrink-0 flex-col border-r border-border bg-surface md:flex"><div className="border-b border-border p-4"><div className="text-base font-semibold">文件去重助手</div><Badge variant="neutral" className="mt-2">本地离线引擎</Badge></div><div className="p-3"><label className="mb-1 block text-xs text-faint">当前项目</label><select className="control-h w-full rounded-control border border-border-strong bg-surface px-2 text-sm" value={current || ""} onChange={(e) => setCurrent(e.target.value)}><option value="">未选择项目</option>{projects.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></div><nav className="flex-1 space-y-1 p-3">{nav.map(([id, label, icon]) => <button key={id} onClick={() => setView(id)} className={`flex h-9 w-full items-center gap-2 rounded-control px-3 text-left text-sm ${view === id ? "bg-primary-soft font-medium text-primary" : "text-muted hover:bg-surface-2 hover:text-foreground"}`}>{icon}{label}{id === "results" && current && <span className="ml-auto text-xs">{projects.find((p) => p.id === current)?.lastSummary?.pendingGroups || ""}</span>}</button>)}</nav><div className="space-y-1 border-t border-border p-3 text-xs text-muted"><div className="flex justify-between"><span>当前版本</span><span>{info?.version || "加载中…"}</span></div><div className="flex justify-between"><span>FFmpeg</span><span>{info ? info.ffmpeg ? "已就绪" : "未安装" : "—"}</span></div><div className="flex justify-between"><span>删除策略</span><span>{settings?.defaultDeleteMode === "permanent" ? "永久删除" : "回收站"}</span></div></div></aside><main className="min-w-0 flex-1 overflow-auto"><header className="sticky top-0 z-10 flex h-14 items-center justify-between border-b border-border bg-surface/95 px-5 backdrop-blur"><div className="flex items-center gap-2 text-sm font-medium md:hidden">文件去重助手</div><div className="ml-auto flex items-center gap-2"><Button variant="secondary" size="icon" aria-label={themeLabel} title={`${themeLabel}（点击切换）`} onClick={() => theme.setPreference(theme.preference === "light" ? "dark" : theme.preference === "dark" ? "system" : "light")} className="flex items-center justify-center"><ThemeIcon className="w-4 h-4" /></Button><span className="hidden text-xs text-muted sm:inline">{theme.preference === "system" ? "跟随系统" : theme.preference === "dark" ? "深色" : "浅色"}</span></div></header><div className="mx-auto max-w-[1280px] p-5 lg:p-8">{children}</div></main></div>
}

function PageHeader({ title, sub, actions }: { title: string; sub?: string; actions?: React.ReactNode }) { return <div className="mb-5 flex flex-wrap items-start justify-between gap-4"><div><h1 className="text-2xl font-semibold tracking-tight">{title}</h1>{sub && <p className="mt-1 text-sm text-muted">{sub}</p>}</div><div className="flex flex-wrap gap-2">{actions}</div></div> }
function Empty({ title, body, action }: { title: string; body: string; action?: React.ReactNode }) { return <Card className="flex min-h-64 flex-col items-center justify-center p-8 text-center"><div className="mb-3 flex size-12 items-center justify-center rounded-card bg-surface-2 text-primary"><FolderOpen /></div><h2 className="text-lg font-semibold">{title}</h2><p className="mt-1 max-w-md text-sm text-muted">{body}</p>{action && <div className="mt-5">{action}</div>}</Card> }

function Projects({ projects, onNew, onDelete, onOpen }: { projects: Project[]; onNew: () => void; onDelete: (p: Project) => void; onOpen: (p: Project) => void }) { return <><PageHeader title="项目列表" sub="管理本机文件去重项目，所有删除均需人工确认。" actions={<Button onClick={onNew}><Plus />新建项目</Button>} />{!projects.length ? <Empty title="还没有项目" body="创建项目后添加扫描目录，开始使用 SHA-256、pHash 和 FFmpeg 抽帧检测重复媒体。" action={<Button size="lg" onClick={onNew}><Plus />创建第一个项目</Button>} /> : <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">{projects.map((p) => <Card key={p.id} className="flex flex-col gap-4"><div><div className="flex items-start justify-between gap-2"><h2 className="text-base font-semibold">{p.name}</h2><Badge variant={p.scanning ? "info" : "neutral"}>{p.scanning ? "扫描中" : "就绪"}</Badge></div><p className="mt-1 text-xs text-muted">{p.paths.length} 个扫描目录 · {p.lastSummary?.groups || 0} 组重复</p></div><div className="rounded-card bg-surface-2 p-3 text-xs text-muted">{p.paths.length ? p.paths.slice(0, 3).map((path) => <div key={path} className="truncate font-mono">{path}</div>) : "尚未配置目录"}</div><div className="mt-auto flex gap-2"><Button className="flex-1" onClick={() => onOpen(p)}>打开项目</Button><Button variant="danger" size="icon" aria-label="删除项目" onClick={() => onDelete(p)}><Trash2 /></Button></div></Card>)}</div>}</> }

function Config({ project, appInfo, onSave, onStart, onSelect }: { project: Project | null; appInfo: AppInfo | null; onSave: (patch: Record<string, unknown>) => Promise<void>; onStart: (form: Record<string, unknown>) => Promise<void>; onSelect: () => Promise<void> }) { const [form, setForm] = React.useState<Record<string, any>>({}); React.useEffect(() => { if (project) setForm({ ...project }) }, [project]); if (!project) return <><PageHeader title="扫描配置" sub="请先选择项目" /><Empty title="尚未选择项目" body="从项目列表创建或打开一个项目。" /></>; const set = (key: string, value: any) => setForm((x) => ({ ...x, [key]: value })); return <><PageHeader title="扫描配置" sub={`${project.name} · 配置扫描目录与算法参数`} actions={<><Button variant="secondary" onClick={() => void onSave(form)}>保存配置</Button><Button onClick={() => void onStart(form)}><Zap />开始扫描</Button></>} /><Card className="max-w-3xl space-y-5"><div><Label>项目名称</Label><Input className="mt-1" value={form.name || ""} onChange={(e) => set("name", e.target.value)} /></div><div><Label>扫描目录</Label><div className="mt-1 space-y-2">{(form.paths || []).map((path: string, i: number) => <div key={`${path}-${i}`} className="flex gap-2"><Input value={path} onChange={(e) => set("paths", form.paths.map((x: string, j: number) => j === i ? e.target.value : x))} /><Button variant="danger" size="icon-sm" aria-label="移除目录" onClick={() => set("paths", form.paths.filter((_: string, j: number) => j !== i))}><X /></Button></div>)}</div><Button className="mt-2" variant="secondary" size="sm" onClick={onSelect}><FolderOpen />选择目录</Button></div><Field label="相似度阈值（pHash / 视频帧）" value={form.threshold} onChange={(v) => set("threshold", Number(v))} step="0.01" min="0.5" max="0.99" /><Toggle label="启用图片 pHash（HEIC/HEIF 仅精确）" checked={form.includeImages} onChange={(v) => set("includeImages", v)} /><Toggle label="启用视频去重" checked={form.includeVideos} onChange={(v) => set("includeVideos", v)} /><div><Label>视频比对方式</Label><select className="control-h mt-1 w-full rounded-control border border-border-strong bg-surface px-2 text-sm" value={form.videoCompareMode || "simple"} onChange={(e) => set("videoCompareMode", e.target.value)}><option value="precise">精细：抽帧 pHash，能识别重编码与裁剪版本</option><option value="simple">简易：仅内容指纹，跳过抽帧，速度最快</option></select><p className="mt-1 text-xs text-muted">{form.videoCompareMode === "simple" ? "按文件内容采样判重，跳过抽帧。采样范围外的差异可能漏检，可在全局设置中开启完整文件校验。" : "每个视频抽取多帧比对，每个视频需多次随机读取，大库在机械盘上较慢。"}</p></div><Toggle label="启用 TXT 正文相似度" checked={form.includeTexts} onChange={(v) => set("includeTexts", v)} /><div className="grid gap-4 sm:grid-cols-2"><Field label="视频抽帧数" value={form.frameCount} onChange={(v) => set("frameCount", Number(v))} min="3" max="32" disabled={form.videoCompareMode === "simple"} /><Field label="图片/哈希并发" value={form.workers} onChange={(v) => set("workers", Number(v))} min="1" max="16" /><Field label="视频并发" value={form.videoWorkers} onChange={(v) => set("videoWorkers", Number(v))} min="1" max="8" /><Field label="TXT 并发" value={form.textWorkers} onChange={(v) => set("textWorkers", Number(v))} min="1" max="16" /></div><Toggle label="递归子目录" checked={form.recursive} onChange={(v) => set("recursive", v)} />{form.videoCompareMode !== "simple" && !appInfo?.ffmpeg && <p className="rounded-card bg-warning-soft p-3 text-sm text-warning">未检测到 FFmpeg / ffprobe，视频阶段将跳过。</p>}</Card></> }
function Field({ label, value, onChange, ...props }: { label: string; value: any; onChange: (v: string) => void; [key: string]: any }) { return <div><Label>{label}</Label><Input className="mt-1" type="number" value={value ?? ""} onChange={(e) => onChange(e.target.value)} {...props} /></div> }
function Toggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) { return <label className="flex items-center gap-3 text-sm"><Switch checked={Boolean(checked)} onCheckedChange={onChange} /><span>{label}</span></label> }

function ProgressView({ project, status, event, errors, onStart, onCancel, onResults }: { project: Project | null; status: ScanStatus | null; event: ScanEvent | null; errors: ScanError[]; onStart: () => Promise<void>; onCancel: () => Promise<void>; onResults: () => void }) { if (!project) return <><PageHeader title="扫描进度" /><Empty title="尚未选择项目" body="先选择一个项目。" /></>; const percent = status?.percent ?? event?.percent ?? 0; const stage = event?.stage || ""; const skipped = errors.filter((item) => !["aux", "text_decode"].includes(item.kind || "skipped")); const textWarnings = errors.filter((item) => item.kind === "text_decode"); const auxWarnings = errors.filter((item) => item.kind === "aux"); return <><PageHeader title={status?.running ? `扫描执行中（${percent.toFixed(0)}%）` : stage === "done" ? "扫描完成" : status?.canceled ? "扫描已取消" : "扫描进度"} sub={`${project.name} · ${event?.message || status?.lastError || "尚未开始扫描"}`} actions={status?.running ? <Button variant="danger" onClick={onCancel}>取消扫描</Button> : <><Button onClick={onStart}><Zap />开始扫描</Button>{stage === "done" && <Button variant="secondary" onClick={onResults}>查看结果</Button>}</>} />{status?.canceled && <Notice tone="warning">扫描已取消，已计算特征保留在 SQLite 缓存中。</Notice>}{stage === "done" && <Notice tone="success">扫描完成。系统不会自动删除任何文件。</Notice>}{stage === "done" && skipped.length > 0 && <Notice tone="warning">有 {skipped.length} 个文件无法读取，已跳过且未参与比对，详见下方列表。</Notice>}{stage === "done" && textWarnings.length > 0 && <Notice tone="warning">有 {textWarnings.length} 个 TXT 的正文无法解码，已保留书名关联线索，详见下方列表。</Notice>}{stage === "done" && auxWarnings.length > 0 && <Notice tone="info">有 {auxWarnings.length} 条预览辅助警告，源文件已正常参与比对。</Notice>}<Card className="mb-4"><div className="grid gap-2 md:grid-cols-3">{stages.map((item, index) => { const current = stages.findIndex((x) => x.key === stage); const done = current > index || ["done"].includes(stage); return <div key={item.key} className={`rounded-card border p-3 ${stage === item.key ? "border-primary bg-primary-soft" : done ? "border-success bg-success-soft" : "border-border bg-surface"}`}><div className="flex items-center gap-2 text-sm font-medium">{done ? <Check className="size-4 text-success" /> : <span className="text-faint">{index + 1}</span>}{item.key === "video" && project.videoCompareMode === "simple" ? "视频内容指纹" : item.label}</div><div className="mt-1 text-xs text-muted">{stage === item.key ? event?.message || "进行中…" : done ? "已完成" : "等待中"}</div></div>})}</div></Card><div className="grid gap-4 md:grid-cols-3"><Metric label="总体进度" value={`${percent.toFixed(1)}%`}><Progress value={percent} /></Metric><Metric label="已发现文件" value={String(event?.filesSeen ?? status?.filesSeen ?? 0)} /><Metric label="警告 / 错误" value={String(event?.errors ?? status?.errors ?? 0)} /></div><Card className="mt-4"><div className="text-xs text-muted">当前文件</div><div className="mt-1 break-all font-mono text-sm">{event?.currentFile || "—"}</div></Card>
  <ScanWarnings title="已跳过文件" description="这些文件无法读取，已跳过且未参与比对。" badge="已跳过" items={skipped} />
  <ScanWarnings title="TXT 解码警告" description="这些文件保留书名关联和已确认的精确重复，正文尚未参与比较。文件变化或算法升级后会重新解析。" badge="正文不可用" items={textWarnings} />
  <ScanWarnings title="预览辅助警告" description="源文件已正常参与比对，仅缩略图或抽帧预览未能生成。" badge="辅助" items={auxWarnings} />
</> }

function ScanWarnings({ title, description, badge, items }: { title: string; description: string; badge: string; items: ScanError[] }) {
  if (!items.length) return null
  return <Card className="mt-4">
    <div className="flex flex-wrap items-start justify-between gap-2">
      <div><h2 className="font-semibold">{title}（{items.length}）</h2><p className="mt-1 text-xs text-muted">{description}</p></div>
      <Badge variant="warning">{badge}</Badge>
    </div>
    <div className="mt-3 max-h-80 divide-y divide-border overflow-auto">
      {items.map((item, index) => <div key={`${item.path}-${item.stage}-${index}`} className="py-2">
        <div className="break-all font-mono text-xs">{item.path}</div>
        <div className="mt-1 text-xs text-muted">[{item.stage}] {item.message}</div>
      </div>)}
    </div>
  </Card>
}

function Metric({ label, value, children }: { label: string; value: string; children?: React.ReactNode }) { return <Card><div className="text-xs text-muted">{label}</div><div className="mt-2 text-2xl font-semibold tabular-nums">{value}</div>{children && <div className="mt-3">{children}</div>}</Card> }
function Notice({ tone, children }: { tone: "success" | "warning" | "danger" | "info"; children: React.ReactNode }) { return <div className={`mb-4 rounded-card border p-3 text-sm ${tone === "success" ? "border-success bg-success-soft text-success" : tone === "warning" ? "border-warning bg-warning-soft text-warning" : tone === "danger" ? "border-danger bg-danger-soft text-danger" : "border-primary bg-primary-soft text-primary"}`}>{children}</div> }

function Results({ project, groups, total, loadingMore, filter, setFilter, onOpen, onIgnore, onBulkDelete, onExport, onRescan, onLoadMore }: { project: Project | null; groups: Group[]; total: number; loadingMore: boolean; filter: { status: string; groupType: string; search: string; sort: string }; setFilter: React.Dispatch<React.SetStateAction<{ status: string; groupType: string; search: string; sort: string }>>; onOpen: (id: number) => void; onIgnore: (ids: number[]) => Promise<void>; onBulkDelete: (ids: number[]) => Promise<void>; onExport: () => void; onRescan: () => void; onLoadMore: () => Promise<void> }) {
  const [selected, setSelected] = React.useState<number[]>([])
  const anchorIndex = React.useRef<number | null>(null)
  const modifiers = React.useRef({ shiftKey: false, ctrlKey: false, metaKey: false })
  React.useEffect(() => { const alive = new Set(groups.map((g) => g.groupId)); setSelected((prev) => prev.filter((id) => alive.has(id))) }, [groups])
  if (!project) return <><PageHeader title="重复结果" /><Empty title="尚未选择项目" body="先选择一个项目。" /></>
  // Ordering is decided by the server, which sees every matching group rather
  // than the page that happens to be loaded. Shift-click ranges follow this order.
  const rows = groups
  const allIds = rows.map((g) => g.groupId)
  const allChecked = rows.length > 0 && selected.length === rows.length
  // Shift+click extends the selection from the last anchor (add only, so a
  // range never wipes out earlier picks). Ctrl/Cmd+click and a plain click
  // toggle a single row, which keeps scattered picks possible. The anchor is
  // only moved by non-range interactions so repeated Shift+click keeps working.
  const toggleRow = (index: number, groupId: number, checked: boolean, event?: { shiftKey?: boolean; ctrlKey?: boolean; metaKey?: boolean }) => {
    const anchor = anchorIndex.current
    if (event?.shiftKey && anchor != null) {
      const start = Math.min(anchor, index)
      const end = Math.max(anchor, index)
      setSelected((prev) => [...new Set([...prev, ...allIds.slice(start, end + 1)])])
      return
    }
    anchorIndex.current = index
    setSelected((prev) => {
      const next = new Set(prev)
      checked ? next.add(groupId) : next.delete(groupId)
      return [...next]
    })
  }
  // macOS turns Ctrl+click into a contextmenu event instead of a click, so both
  // paths feed the same action and a short window keeps them from double firing.
  const ctrlGuard = React.useRef({ groupId: 0, at: 0 })
  const pickRow = (event: React.MouseEvent, index: number, groupId: number, checked: boolean) => {
    if (!isRowTarget(event.target)) return
    if (ctrlGuard.current.groupId === groupId && Date.now() - ctrlGuard.current.at < 400) return
    toggleRow(index, groupId, checked, event)
  }
  const pickRowFromContextMenu = (event: React.MouseEvent, index: number, groupId: number, checked: boolean) => {
    if (!event.ctrlKey && !event.metaKey) return
    if (!isRowTarget(event.target)) return
    event.preventDefault()
    ctrlGuard.current = { groupId, at: Date.now() }
    toggleRow(index, groupId, checked, event)
  }
  return <><PageHeader title="重复结果" sub={`${project.name} · ${project.lastSummary?.groups || 0} 组 · 可释放 ${bytes(project.lastSummary?.reclaimableBytes)}`} actions={<><Button variant="secondary" onClick={() => setSelected(allChecked ? [] : allIds)}>{allChecked ? "清空本页选择" : "全选本页"}</Button><Button variant="secondary" onClick={() => void onIgnore(selected)} disabled={!selected.length}>忽略勾选组</Button><Button variant="secondary" onClick={onRescan}><RefreshCw />重新扫描</Button><Button variant="secondary" onClick={onExport}><Upload />导出报告</Button></>} /><Notice tone="info">支持全选本页、按住 Shift 连续范围勾选，或按住 Ctrl/⌘ 逐个加减勾选（也可直接点击整行）；按组删除只处理明确的清理候选；TXT 正文近似、覆盖和书名关联项需要打开原文件核对，不会自动勾选。</Notice><div className="mb-4 grid gap-2 md:grid-cols-4"><Input placeholder="搜索路径…" value={filter.search} onChange={(e) => setFilter((f) => ({ ...f, search: e.target.value }))} /><select className="control-h rounded-control border border-border-strong bg-surface px-2 text-sm" value={filter.status} onChange={(e) => setFilter((f) => ({ ...f, status: e.target.value }))}><option value="pending">待处理</option><option value="ignored">已忽略</option><option value="processed">已处理</option><option value="all">全部</option></select><select className="control-h rounded-control border border-border-strong bg-surface px-2 text-sm" value={filter.groupType} onChange={(e) => setFilter((f) => ({ ...f, groupType: e.target.value }))}><option value="">全部类型</option><option value="exact">精确重复</option><option value="similar_image">近似图片</option><option value="similar_video">近似视频</option><option value="text_equal">TXT 文本一致</option><option value="similar_text">TXT 正文近似</option><option value="text_contained">TXT 内容覆盖</option><option value="text_name">TXT 书名关联</option></select><select className="control-h rounded-control border border-border-strong bg-surface px-2 text-sm" value={filter.sort} onChange={(e) => setFilter((f) => ({ ...f, sort: e.target.value }))}><option value="id">按组号</option><option value="confidence">按置信度</option><option value="size">按可释放空间</option><option value="members">按成员数</option></select></div>{!rows.length ? <Empty title="没有匹配的重复组" body="可以切换筛选，或先执行扫描。" /> : <Card className="overflow-auto p-0"><table className="w-full min-w-[720px] text-left text-sm"><thead className="bg-surface-2 text-xs text-muted"><tr><th className="p-3"><Checkbox checked={allChecked} onCheckedChange={(v) => { setSelected(v === true ? allIds : []); anchorIndex.current = null }} /></th><th className="p-3">组</th><th className="p-3">内容</th><th className="p-3">状态</th><th className="p-3">可释放</th><th className="p-3"></th></tr></thead><tbody>{rows.map((g, index) => { const isChecked = selected.includes(g.groupId); return <tr key={g.groupId} onClick={(event: React.MouseEvent) => pickRow(event, index, g.groupId, !isChecked)} onContextMenu={(event: React.MouseEvent) => pickRowFromContextMenu(event, index, g.groupId, !isChecked)} className={`cursor-pointer select-none border-t border-border hover:bg-surface-2 ${isChecked ? "bg-primary-soft/40" : ""}`}><td className="p-3"><Checkbox checked={isChecked} onClick={(e: React.MouseEvent) => { e.stopPropagation(); modifiers.current = { shiftKey: e.shiftKey, ctrlKey: e.ctrlKey, metaKey: e.metaKey } }} onCheckedChange={(v) => toggleRow(index, g.groupId, v === true, modifiers.current)} /></td><td className="p-3 font-mono">#{g.groupId}</td><td className="p-3"><div className="font-medium">{g.typeLabel || typeLabel(g.groupType)}</div><div className="text-xs text-muted tabular-nums">{g.memberCount} 个成员 · {g.groupType === "text_name" ? "仅书名证据" : isTextAnalysis(g.groupType) && g.confidence === 0 ? "含未确认正文关系，请逐对核对" : `组内最低匹配分 ${g.confidence.toFixed(2)}`}</div></td><td className="p-3"><Badge variant={g.status === "processed" ? "success" : g.status === "ignored" ? "warning" : "info"}>{statusLabel(g.status)}</Badge></td><td className="p-3 font-mono">{bytes(g.reclaimableBytes)}</td><td className="p-3"><Button size="sm" variant="secondary" onClick={() => onOpen(g.groupId)}>打开</Button></td></tr> })}</tbody></table></Card>}{total > 0 && <div className="mt-3 flex flex-wrap items-center justify-between gap-3 rounded-card border border-border bg-surface px-4 py-3"><div className="text-sm text-muted">已显示 <span className="font-semibold text-foreground tabular-nums">{rows.length}</span> / 共 <span className="tabular-nums">{total}</span> 组匹配{rows.length < total ? "，勾选与批量删除只作用于已加载的行" : "（已全部载入）"}</div>{rows.length < total && <Button variant="secondary" onClick={() => void onLoadMore()} disabled={loadingMore}>{loadingMore ? "加载中…" : `加载更多（还有 ${total - rows.length} 组）`}</Button>}</div>}{selected.length > 0 && <div className="sticky bottom-4 z-10 mt-4 flex flex-wrap items-center justify-between gap-3 rounded-card border border-primary/30 bg-surface px-4 py-3 shadow-lg"><div className="text-sm text-muted">已勾选 <span className="font-semibold text-foreground">{selected.length}</span> 组，仅删除明确的清理候选，待核对项不参与批量删除</div><div className="flex flex-wrap gap-2"><Button variant="secondary" onClick={() => setSelected([])}>取消选择</Button><Button variant="danger" onClick={() => void onBulkDelete(selected)} disabled={!rows.some((g) => selected.includes(g.groupId) && g.cleanupCount > 0)}><Trash2 />删除候选</Button></div></div>}</> }

function GroupView({ detail, selected, setSelected, deleteLabel, onBack, onStatus, onDelete, onRecommend, onOpenFile }: { detail: GroupDetail | null; selected: Set<number>; setSelected: React.Dispatch<React.SetStateAction<Set<number>>>; deleteLabel: string; onBack: () => void; onStatus: (status: string) => Promise<void>; onDelete: () => void; onRecommend: (id: number) => Promise<void>; onOpenFile: (action: string, path: string) => Promise<void> }) {
  if (!detail) return <Empty title="组详情加载失败" body="返回结果列表重试。" />
  const analysis = isTextAnalysis(detail.groupType)
  return <>
    <PageHeader title={`组 #${detail.groupId} · ${detail.typeLabel || typeLabel(detail.groupType)}`} sub={`明确清理候选可释放 ${bytes(detail.reclaimableBytes)}`} actions={<><Button variant="secondary" onClick={onBack}><ChevronLeft />返回结果</Button><Button variant="secondary" onClick={() => void onStatus(detail.status === "ignored" ? "pending" : "ignored")}>{detail.status === "ignored" ? "取消忽略" : "忽略此组"}</Button><Button variant="secondary" onClick={() => void onStatus("processed")}>标记已处理</Button></>} />
    <Notice tone="info">{analysis ? "这是逐对核对的关联组。对照文件不保证能替代其他版本；正文近似、覆盖与仅书名关联的区别见每个文件的依据。仅已确认的副本会自动勾选，其余文件请打开核对后再手动选择。" : detail.groupType === "text_equal" ? "这些文件解码并统一换行后的全文一致。编码、BOM 或换行形式可以不同；其他文本没有被删除或改写。" : "明确的清理候选已自动勾选。待核对项不会参与按组批量删除。"}</Notice>
    <div className="space-y-3">{detail.items.map((item) => <Member key={item.fileId} item={item} analysis={analysis} selected={selected.has(item.fileId)} onSelect={(checked) => setSelected((set) => { const next = new Set(set); checked ? next.add(item.fileId) : next.delete(item.fileId); return next })} onRecommend={() => void onRecommend(item.fileId)} onOpenFile={onOpenFile} />)}</div>
    <div className="sticky bottom-0 mt-5 flex items-center justify-between border-t border-border bg-surface/95 py-3 backdrop-blur"><span className="text-sm text-muted">仅明确清理候选自动勾选 · 已选删除 {selected.size} 个文件</span><div className="flex gap-2"><Button variant="secondary" onClick={() => setSelected(candidateFileIds(detail.items))}>重选清理候选</Button><Button variant="danger" onClick={onDelete} disabled={!selected.size}><Trash2 />{deleteLabel}</Button></div></div>
  </>
}

function Member({ item, analysis, selected, onSelect, onRecommend, onOpenFile }: { item: Item; analysis: boolean; selected: boolean; onSelect: (v: boolean) => void; onRecommend: () => void; onOpenFile: (action: string, path: string) => void }) {
  const label = item.isRecommended ? (analysis ? "对照文件" : "推荐保留") : item.action === "review_required" ? "待核对" : "清理候选"
  return <Card className={`grid gap-4 md:grid-cols-[120px_1fr_auto] ${item.isRecommended ? "border-success" : ""}`}>
    <div><div className="flex aspect-square items-center justify-center overflow-hidden rounded-card bg-surface-2">{item.thumbUrl ? <img src={item.thumbUrl} alt="" className="h-full w-full object-cover" /> : <span className="text-xs text-faint">{item.mediaType || "媒体"}</span>}</div><label className="mt-2 flex items-center gap-2 text-xs"><Checkbox disabled={item.isRecommended || !item.exists} checked={selected} onCheckedChange={(v) => onSelect(v === true)} />{item.isRecommended ? label : "选择删除"}</label></div>
    <div className="min-w-0"><div className="flex flex-wrap gap-2"><Badge variant={item.isRecommended ? "success" : "warning"}>{label}</Badge>{!item.exists && <Badge variant="danger">文件缺失</Badge>}</div><div className="mt-2 break-all text-sm">{item.path}</div><div className="mt-1 text-xs text-muted">{bytes(item.sizeBytes)} · {analysis ? "与对照文件的正文匹配" : "相似度"} {similarityText(item.similarity)}{item.width ? ` · ${item.width}×${item.height}` : ""}{item.durationMs ? ` · ${(item.durationMs / 1000).toFixed(1)}s` : ""}{item.hammingDistance != null ? ` · 汉明距离 ${item.hammingDistance}` : ""}</div>{item.reasons?.length ? <ul className="mt-2 space-y-1 break-words text-xs text-muted">{item.reasons.map((reason, i) => <li key={i}>{reason}</li>)}</ul> : null}</div>
    <div className="flex flex-wrap items-start gap-2 md:flex-col"><Button size="sm" variant="secondary" onClick={() => onOpenFile("open", item.path)}>打开</Button><Button size="sm" variant="secondary" onClick={() => onOpenFile("reveal", item.path)}>定位</Button><Button size="sm" variant="secondary" onClick={() => onOpenFile("copy", item.path)}>复制</Button>{!item.isRecommended && item.exists && <Button size="sm" variant="secondary" onClick={onRecommend}>{analysis ? "设为对照文件" : "设为推荐保留"}</Button>}</div>
  </Card>
}

function SettingsView({ settings, info, onSave, onUpdate }: { settings: AppSettings | null; info: AppInfo | null; onSave: (s: AppSettings) => Promise<void>; onUpdate: () => Promise<void> }) { const [form, setForm] = React.useState<AppSettings | null>(settings); React.useEffect(() => setForm(settings), [settings]); if (!form) return null; const set = (key: keyof AppSettings, value: any) => setForm((s) => s ? ({ ...s, [key]: value }) : s); return <><PageHeader title="全局设置" sub="默认扫描参数与删除策略。永久删除默认关闭。" actions={<Button onClick={() => void onSave(form)}>保存设置</Button>} />{!form.ffmpegAvailable && <Notice tone="warning">未检测到 FFmpeg / ffprobe。视频近似与帧条预览不可用。</Notice>}<Card className="max-w-3xl space-y-5"><Field label="默认相似度阈值" value={form.defaultThreshold} onChange={(v) => set("defaultThreshold", Number(v))} step="0.01" min="0.5" max="0.99" /><Field label="默认视频抽帧数" value={form.defaultFrameCount} onChange={(v) => set("defaultFrameCount", Number(v))} min="1" max="32" /><div><Label>默认视频比对方式</Label><select className="control-h mt-1 w-full rounded-control border border-border-strong bg-surface px-2 text-sm" value={form.defaultVideoCompareMode || "simple"} onChange={(e) => set("defaultVideoCompareMode", e.target.value)}><option value="precise">精细（抽帧 pHash）</option><option value="simple">简易（内容指纹，跳过抽帧）</option></select></div><Toggle label="简易模式下用完整文件校验指纹（更准但更慢）" checked={form.defaultVerifySampledHash} onChange={(v) => set("defaultVerifySampledHash", v)} /><div className="grid gap-4 sm:grid-cols-3"><Field label="图片并发" value={form.defaultWorkers} onChange={(v) => set("defaultWorkers", Number(v))} min="1" max="16" /><Field label="视频并发" value={form.defaultVideoWorkers} onChange={(v) => set("defaultVideoWorkers", Number(v))} min="1" max="8" /><Field label="TXT 并发" value={form.defaultTextWorkers} onChange={(v) => set("defaultTextWorkers", Number(v))} min="1" max="16" /></div><Toggle label="默认启用视频" checked={form.defaultIncludeVideos} onChange={(v) => set("defaultIncludeVideos", v)} /><Toggle label="默认启用 TXT" checked={form.defaultIncludeTexts} onChange={(v) => set("defaultIncludeTexts", v)} /><Field label="默认 TXT 相似度" value={form.defaultTextThreshold} onChange={(v) => set("defaultTextThreshold", Number(v))} step="0.01" min="0.8" max="0.99" /><div><Label>默认删除模式</Label><select className="control-h mt-1 w-full rounded-control border border-border-strong bg-surface px-2 text-sm" value={form.defaultDeleteMode} onChange={(e) => set("defaultDeleteMode", e.target.value)}><option value="recycle">移入系统回收站（推荐）</option><option value="permanent">永久删除</option></select></div><Toggle label="允许永久删除（需额外确认）" checked={form.allowPermanentDelete} onChange={(v) => set("allowPermanentDelete", v)} /><div><Label>FFmpeg 路径</Label><Input className="mt-1" value={form.ffmpegPath} onChange={(e) => set("ffmpegPath", e.target.value)} /></div><div><Label>ffprobe 路径</Label><Input className="mt-1" value={form.ffprobePath} onChange={(e) => set("ffprobePath", e.target.value)} /></div></Card><Card className="mt-4 max-w-3xl"><h2 className="font-semibold">关于与更新</h2><p className="mt-2 text-sm text-muted">当前版本 {info?.version || "—"}。启动后自动检查 GitHub Release。</p><Button className="mt-4" variant="secondary" onClick={() => void onUpdate()}>检查更新</Button></Card></> }

export default function App() {
  const theme = useTheme(); const [view, setView] = React.useState<View>("projects"); const [projects, setProjects] = React.useState<Project[]>([]); const [currentId, setCurrentId] = React.useState<string | null>(null); const [info, setInfo] = React.useState<AppInfo | null>(null); const [settings, setSettings] = React.useState<AppSettings | null>(null); const [event, setEvent] = React.useState<ScanEvent | null>(null); const [status, setStatus] = React.useState<ScanStatus | null>(null); const [scanErrors, setScanErrors] = React.useState<ScanError[]>([]); const [groups, setGroups] = React.useState<Group[]>([]); const [groupTotal, setGroupTotal] = React.useState(0); const [groupsLoading, setGroupsLoading] = React.useState(false); const [detail, setDetail] = React.useState<GroupDetail | null>(null); const [selected, setSelected] = React.useState(new Set<number>()); const [filter, setFilter] = React.useState({ status: "pending", groupType: "", search: "", sort: "confidence" }); const [confirm, setConfirm] = React.useState<{ title: string; body: React.ReactNode; danger?: boolean; checkbox?: string; confirmLabel?: string; retryLabel?: string; onRetry?: () => void; action: () => void } | null>(null); const [newOpen, setNewOpen] = React.useState(false); const [newName, setNewName] = React.useState(""); const [newPath, setNewPath] = React.useState(""); const [update, setUpdate] = React.useState<UpdateInfo | null>(null); const [updateState, setUpdateState] = React.useState<"prompt" | "downloading" | "downloaded" | "failed" | null>(null); const [updateError, setUpdateError] = React.useState(""); const [updateProgress, setUpdateProgress] = React.useState<DownloadProgress | null>(null); const current = projects.find((p) => p.id === currentId) || null
  // Project identity and request generations. A response may only be applied
  // while the project it was requested for is still the current one; the counter
  // additionally drops out-of-order answers for the same project (typing in the
  // search box fires one request per keystroke).
  const currentIdRef = React.useRef<string | null>(currentId); currentIdRef.current = currentId
  const groupsSeq = React.useRef(0); const detailSeq = React.useRef(0)
  // Read by loadMore to build the next offset without capturing a stale array.
  const groupsRef = React.useRef(groups); groupsRef.current = groups
  const filterRef = React.useRef(filter); filterRef.current = filter
  // Switching project invalidates every piece of page state that describes the
  // previous one: a detail, a selection or a confirmation built for project A
  // must never be submitted while B is selected.
  const setProject = React.useCallback((id: string | null) => { currentIdRef.current = id; groupsSeq.current++; detailSeq.current++; setCurrentId(id); setGroups([]); setGroupTotal(0); setDetail(null); setSelected(new Set()); setConfirm(null); setStatus(null); setEvent(null); setScanErrors([]) }, [])
  const refresh = React.useCallback(async () => {
    const next = await api.ListProjects()
    setProjects(next)
    const known = next.some((p) => p.id === currentIdRef.current)
    const id = known ? currentIdRef.current : next[0]?.id ?? null
    if (id !== currentIdRef.current) setProject(id)
    if (!id) return
    const scan = await api.GetScanStatus(id)
    if (id === currentIdRef.current) setStatus(scan)
  }, [setProject])
  // keepPlace reloads as many rows as are currently on screen instead of dropping
  // back to page 1, so deleting one group does not move the list under the user.
  // It still stops at the server's page ceiling.
  const refreshGroups = React.useCallback(async (options?: { keepPlace?: boolean }) => {
    const id = currentIdRef.current
    if (!id) return
    const seq = ++groupsSeq.current
    const loaded = options?.keepPlace ? groupsRef.current.length : 0
    const limit = Math.min(Math.max(loaded, GROUP_PAGE_SIZE), GROUP_PAGE_MAX)
    const page = await api.ListGroups(id, { ...filterRef.current, limit, offset: 0 })
    // A slower answer for an earlier filter, or for a project that is no longer
    // selected, must not overwrite what is on screen.
    if (seq !== groupsSeq.current || id !== currentIdRef.current) return
    setGroupTotal(page.total)
    setGroups(page.items)
  }, [])
  const loadMoreGroups = React.useCallback(async () => {
    const id = currentIdRef.current
    if (!id) return
    const seq = groupsSeq.current
    setGroupsLoading(true)
    try {
      const page = await api.ListGroups(id, { ...filterRef.current, limit: GROUP_PAGE_SIZE, offset: groupsRef.current.length })
      if (seq !== groupsSeq.current || id !== currentIdRef.current) return
      setGroupTotal(page.total)
      // Rows can shift between requests; drop anything already on screen rather
      // than rendering the same group twice under one key.
      setGroups((prev) => { const seen = new Set(prev.map((g) => g.groupId)); return [...prev, ...page.items.filter((g) => !seen.has(g.groupId))] })
    } finally { setGroupsLoading(false) }
  }, [])
  React.useEffect(() => { void (async () => { setInfo(await api.AppReady()); setSettings(await api.GetSettings()); await refresh() })() }, [refresh])
  // Typing in the search box used to fire one full list query per keystroke.
  React.useEffect(() => {
    if (view !== "results") return
    const timer = window.setTimeout(() => void refreshGroups(), 200)
    return () => window.clearTimeout(timer)
  }, [view, filter, refreshGroups])
  React.useEffect(() => { if (!currentId || view !== "progress") return; let active = true; const tick = async () => { const next = await api.GetScanStatus(currentId); if (active) { setStatus(next); if (next.event) setEvent(next.event) } }; void tick(); const timer = window.setInterval(() => void tick(), 800); return () => { active = false; window.clearInterval(timer) } }, [currentId, view])
  React.useEffect(() => { if (view !== "progress" || !currentId) return; if (status?.running) { setScanErrors([]); return } let active = true; void api.ListScanErrors(currentId).then((list) => { if (active) setScanErrors(list) }).catch(() => { if (active) setScanErrors([]) }); return () => { active = false } }, [view, currentId, status?.running, status?.startedAt, status?.finishedAt])
  const onUpdate = React.useCallback((name: string, payload: any) => { if (name === "update:available") { setUpdate(payload); setUpdateState("prompt") } else if (name === "update:progress") { setUpdateProgress(payload); setUpdateState("downloading") } else if (name === "update:done") setUpdateState("downloaded"); else if (name === "update:failed") { setUpdateError(typeof payload?.message === "string" ? payload.message : ""); setUpdateState("failed") } }, [])
  // Stable identity: an inline lambda here re-subscribed every event on every
  // render, because it changes identity on each pass.
  const refreshFromEvent = React.useCallback(() => { void refresh().catch((error) => console.error("刷新项目失败", error)) }, [refresh])
  useAppEvents(currentId, setEvent, refreshFromEvent, onUpdate)
  const saveProject = async (patch: Record<string, unknown>) => { if (!currentId) return; await api.UpdateProject(currentId, patch); await refresh() }
  const start = async () => { if (!currentId) return; setScanErrors([]); await api.StartScan(currentId); setView("progress") }
  const saveAndStart = async (form: Record<string, unknown>) => { await saveProject(form); await start() }
  const openGroup = async (id: number, options?: { quiet?: boolean }) => {
    const pid = currentIdRef.current
    if (!pid) return false
    const seq = ++detailSeq.current
    try {
      const next = await api.GetGroup(pid, id)
      if (seq !== detailSeq.current || pid !== currentIdRef.current) return false
      setDetail(next); setSelected(candidateFileIds(next.items)); setView("group")
      return true
    } catch (error) {
      // Rows can outlive a rescan; a click on one is worth explaining, the
      // automatic hop after a delete is not.
      if (!options?.quiet && pid === currentIdRef.current) setConfirm({ title: "打开组失败", body: message(error), action: () => setConfirm(null) })
      return false
    }
  }
  // One path for every delete: submit, then read the per-file outcome. A partial
  // result is a state to show and retry, never a silent success.
  async function runDelete(projectId: string, mode: string, groupIds: number[], fileIds: number[], after: (result: DeleteResult) => Promise<void>) {
    let result: DeleteResult
    try {
      result = await api.DeleteFiles({ projectId, mode, groupIds, fileIds })
    } catch (error) {
      setConfirm({ title: "删除未执行", body: message(error), action: () => setConfirm(null) })
      return
    }
    try { await after(result) } catch (error) { setConfirm({ title: "删除已执行，但刷新结果失败", body: message(error), action: () => setConfirm(null) }); return }
    if (!result.failed.length && !result.metadataErrors?.length) { setConfirm(null); return }
    // Only failures the server could identify can be retried; a path that came
    // from somewhere else carries no fileId and must not be replayed.
    const retryable = result.failed.filter((f) => f.fileId > 0)
    const retryGroups = [...new Set(retryable.map((f) => f.groupId))]
    const retryFiles = retryable.map((f) => f.fileId)
    setConfirm({
      title: result.failed.length ? (result.succeeded.length ? "部分文件未能删除" : "文件未能删除") : "文件已删除，但结果更新失败",
      body: <DeleteOutcome result={result} />,
      confirmLabel: "关闭",
      retryLabel: retryable.length ? `重试失败项（${retryable.length}）` : undefined,
      onRetry: retryable.length ? () => { setConfirm(null); void runDelete(projectId, mode, retryGroups, retryFiles, after) } : undefined,
      action: () => setConfirm(null),
    })
  }
  const deleteMode = settings?.defaultDeleteMode === "permanent" && settings?.allowPermanentDelete ? "permanent" : "recycle"
  const deleteLabel = deleteMode === "permanent" ? "永久删除" : "移入系统回收站"
  const deleteSelected = () => {
    if (!detail || !current) return
    const items = detail.items.filter((x) => selected.has(x.fileId))
    if (!items.length || items.some((x) => x.isRecommended)) return
    // Snapshot the identity now: the dialog must delete what it described, even
    // if the page state moves on while it is open.
    const projectId = current.id, groupId = detail.groupId, fileIds = items.map((x) => x.fileId)
    const permanent = deleteMode === "permanent"
    const reviewNote = items.some((x) => x.action === "review_required") ? "包含手动选择的待核对文件，请确认已检查正文和版本。" : ""
    setConfirm({
      title: permanent ? "确认永久删除选中文件？" : "确认将文件移入系统回收站？",
      body: `项目「${current.name}」组 #${groupId}，共 ${items.length} 个文件。${reviewNote}`,
      danger: true,
      checkbox: permanent ? "我已理解无法恢复" : undefined,
      action: () => {
        void runDelete(projectId, permanent ? "permanent" : "recycle", [groupId], fileIds, async (result) => {
          await refresh()
          const pending = await api.ListGroups(projectId, { ...filterRef.current, status: "pending", limit: Math.min(Math.max(groupsRef.current.length, GROUP_PAGE_SIZE), GROUP_PAGE_MAX), offset: 0 })
          if (projectId !== currentIdRef.current) return
          setGroupTotal(pending.total)
          setGroups(pending.items)
          // A file that stayed behind means this group still needs attention:
          // stay on it and re-read it so the row that was removed disappears.
          if (result.failed.length) { await openGroup(groupId, { quiet: true }); return }
          const next = pending.items.find((g) => g.groupId !== groupId)
          if (next) await openGroup(next.groupId, { quiet: true })
          else setView("results")
        })
      },
    })
  }
  const bulkDeleteCandidates = async (groupIds: number[]) => {
    if (!currentId || !current || !groupIds.length) return
    const projectId = current.id
    const permanent = deleteMode === "permanent"
    const total = groups.filter((g) => groupIds.includes(g.groupId)).reduce((n, g) => n + (g.reclaimableBytes || 0), 0)
    setConfirm({
      title: permanent ? "确认永久删除副本候选？" : "确认将副本候选移入系统回收站？",
      body: `项目「${current.name}」共 ${groupIds.length} 组 · 约 ${bytes(total)}。只删除明确的清理候选，所有待核对项均保留。`,
      danger: true,
      checkbox: permanent ? "我已理解无法恢复" : undefined,
      action: () => { void runDelete(projectId, permanent ? "permanent" : "recycle", groupIds, [], async () => { await refresh(); await refreshGroups({ keepPlace: true }); setView("results") }) },
    })
  }
  const newProject = () => { setNewName(""); setNewPath(""); setNewOpen(true) }
  const createProject = async () => { if (!newName.trim() || !newPath.trim()) return; const p = await api.CreateProject({ name: newName.trim(), paths: [newPath.trim()], recursive: true, includeImages: true, includeVideos: true, includeTexts: false }); setNewOpen(false); setProject(p.id); await refresh(); setView("config") }
  return <Shell view={view} setView={setView} projects={projects} current={currentId} setCurrent={(id) => { setProject(id); setView("config") }} info={info} settings={settings} theme={theme}>{view === "projects" && <Projects projects={projects} onNew={newProject} onDelete={(p) => setConfirm({ title: "删除项目？", body: `项目「${p.name}」的本地配置和缓存将被删除。`, danger: true, action: () => void api.DeleteProject(p.id).then(() => { setConfirm(null); setProject(null); void refresh() }) })} onOpen={(p) => { setProject(p.id); setView("config") }} />}{view === "config" && <Config project={current} appInfo={info} onSave={saveProject} onStart={saveAndStart} onSelect={async () => { const path = await api.SelectDirectory(); if (path && current) await saveProject({ paths: [...current.paths, path] }) }} />}{view === "progress" && <ProgressView project={current} status={status} event={event} errors={scanErrors} onStart={start} onCancel={async () => { if (currentId) await api.CancelScan(currentId) }} onResults={() => setView("results")} />}{view === "results" && <Results project={current} groups={groups} total={groupTotal} loadingMore={groupsLoading} onLoadMore={loadMoreGroups} filter={filter} setFilter={setFilter} onOpen={openGroup} onIgnore={async (ids) => { if (!currentId) return; try { for (const id of ids) await api.SetGroupStatus(currentId, id, "ignored"); await Promise.all([refresh(), refreshGroups({ keepPlace: true })]) } catch (error) { setConfirm({ title: "忽略失败", body: message(error), action: () => setConfirm(null) }) } }} onBulkDelete={bulkDeleteCandidates} onExport={() => currentId && void api.ExportReport(currentId, "json", filter.status).catch((e) => setConfirm({ title: "导出失败", body: message(e), action: () => setConfirm(null) }))} onRescan={start} />}{view === "group" && <GroupView detail={detail} selected={selected} setSelected={setSelected} deleteLabel={deleteLabel} onBack={() => setView("results")} onStatus={async (s) => { if (currentId && detail) { await api.SetGroupStatus(currentId, detail.groupId, s); setDetail({ ...detail, status: s }); void refresh() } }} onDelete={deleteSelected} onRecommend={async (id) => { if (currentId && detail) { await api.SetRecommended(currentId, detail.groupId, id); void refresh(); const d = await api.GetGroup(currentId, detail.groupId); setDetail(d); setSelected(candidateFileIds(d.items)) } }} onOpenFile={(action, path) => api[action === "open" ? "OpenPath" : action === "reveal" ? "RevealInFolder" : "CopyToClipboard"](path).catch((e) => setConfirm({ title: "操作失败", body: message(e), action: () => setConfirm(null) }))} />}{view === "settings" && <SettingsView settings={settings} info={info} onSave={async (s) => { await api.SaveSettings(s); setSettings(s) }} onUpdate={async () => { const next = await api.CheckUpdate(); if (next?.hasUpdate) { setUpdate(next); setUpdateState("prompt") } }} />}<Dialog open={newOpen} onOpenChange={setNewOpen}><DialogContent><DialogHeader><DialogTitle>新建项目</DialogTitle><DialogDescription>项目是一组扫描目录和去重参数的本地任务容器。</DialogDescription></DialogHeader><div className="space-y-4"><div><Label>项目名称</Label><Input className="mt-1" value={newName} onChange={(e) => setNewName(e.target.value)} /></div><div><Label>扫描目录</Label><Input className="mt-1" value={newPath} onChange={(e) => setNewPath(e.target.value)} /></div></div><DialogFooter><Button variant="secondary" onClick={() => setNewOpen(false)}>取消</Button><Button onClick={() => void createProject()} disabled={!newName.trim() || !newPath.trim()}>创建</Button></DialogFooter></DialogContent></Dialog><Dialog open={Boolean(update && updateState)} onOpenChange={(open) => !open && setUpdateState(null)}><DialogContent><DialogHeader><DialogTitle>{updateState === "downloading" ? "正在下载更新" : updateState === "downloaded" ? "更新已准备就绪" : updateState === "failed" ? "更新失败" : "发现新版本"}</DialogTitle><DialogDescription>{update?.currentVersion} → {update?.latestVersion}</DialogDescription></DialogHeader>{update?.releaseNotes && <div className="max-h-48 overflow-auto whitespace-pre-wrap rounded-card bg-surface-2 p-3 text-sm">{update.releaseNotes}</div>}{updateState === "downloading" && <Progress value={updateProgress?.percent || 0} />}{updateState === "failed" && <Notice tone="danger"><div>{updateError || "更新下载失败，请前往发布页手动下载。"}</div><Button className="mt-2" size="sm" variant="secondary" onClick={() => void api.OpenURL(update?.releaseUrl || "")}>打开发布页</Button></Notice>}<DialogFooter>{updateState === "prompt" && <><Button variant="secondary" onClick={() => void api.OpenURL(update?.releaseUrl || update?.downloadUrl || "")}>浏览器下载</Button><Button onClick={() => { setUpdateError(""); setUpdateState("downloading"); void api.DownloadUpdate(true).catch((e) => { setUpdateError(message(e)); setUpdateState("failed") }) }}>立即更新</Button></>}{updateState === "downloading" && <Button variant="secondary" onClick={() => void api.CancelUpdateDownload()}>取消下载</Button>}{updateState === "downloaded" && <Button onClick={() => void api.ApplyUpdateAndRestart()}>立即重启并更新</Button>}<Button variant="secondary" onClick={() => setUpdateState(null)}>关闭</Button></DialogFooter></DialogContent></Dialog><ConfirmDialog open={Boolean(confirm)} title={confirm?.title || ""} body={confirm?.body || ""} danger={confirm?.danger} checkbox={confirm?.checkbox} confirmLabel={confirm?.confirmLabel} retryLabel={confirm?.retryLabel} onRetry={confirm?.onRetry} onClose={() => setConfirm(null)} onConfirm={() => { void Promise.resolve().then(() => confirm?.action()).catch((e) => setConfirm({ title: "操作失败", body: message(e), action: () => setConfirm(null) })) }} /></Shell>
}
