export type ThemePreference = "system" | "light" | "dark"
export type View = "projects" | "config" | "progress" | "results" | "group" | "settings"

export interface AppInfo { version: string; ffmpeg: boolean; ffprobe: boolean; appData: string; deleteMode: string; engine: string; platform: string }
export interface LastSummary { filesSeen: number; groups: number; reclaimableBytes: number; exactGroups: number; similarGroups: number; pendingGroups?: number }
export interface Project { id: string; name: string; paths: string[]; recursive: boolean; threshold: number; includeImages: boolean; includeVideos: boolean; includeTexts: boolean; textThreshold: number; textWorkers: number; frameCount: number; workers: number; videoWorkers: number; lastScanAt?: string; lastSummary: LastSummary; scanning: boolean; ffmpegReady: boolean }
export interface Settings { defaultThreshold: number; defaultIncludeVideos: boolean; defaultIncludeTexts: boolean; defaultTextThreshold: number; defaultTextWorkers: number; defaultFrameCount: number; defaultWorkers: number; defaultVideoWorkers: number; defaultDeleteMode: string; allowPermanentDelete: boolean; ffmpegPath: string; ffprobePath: string; ffmpegAvailable: boolean; ffprobeAvailable: boolean }
export interface PathStatus { path: string; exists: boolean; isDir: boolean; error?: string }
export interface ScanEvent { stage: string; phase: string; message: string; percent: number; currentFile: string; foundExact: number; foundSimilar: number; filesSeen: number; errors: number; projectId: string }
export interface ScanStatus { projectId: string; running: boolean; canceled: boolean; lastError?: string; event?: ScanEvent; percent: number; filesSeen: number; errors: number }
export interface Group { groupId: number; groupType: string; confidence: number; recommendedFileId: number; status: string; memberCount: number; reclaimableBytes: number; coverThumbUrl: string; typeLabel: string; mediaKind: string }
export interface Item { fileId: number; path: string; action: string; similarity: number; hammingDistance?: number; qualityScore: number; sizeBytes: number; thumbUrl: string; width: number; height: number; durationMs: number; mediaType: string; mediaHashHex: string; isRecommended: boolean; exists: boolean; reasons: string[] }
export interface GroupDetail { groupId: number; groupType: string; typeLabel: string; confidence: number; recommendedFileId: number; status: string; reclaimableBytes: number; items: Item[] }
export interface UpdateInfo { hasUpdate: boolean; currentVersion: string; latestVersion: string; releaseName?: string; releaseNotes?: string; releaseUrl?: string; downloadUrl?: string; assetName?: string; assetSize?: number }
export interface DownloadProgress { downloaded: number; total: number; percent: number; speed: number }
export interface DeleteResult { succeeded: string[]; failed: { path: string; message: string }[]; mode: string; markedProcessed: number[] }

export interface AppAPI {
  AppReady(): Promise<AppInfo>; GetSystemAppearance(): Promise<string>; ListProjects(): Promise<Project[]>; GetProject(id: string): Promise<Project>
  CreateProject(input: Record<string, unknown>): Promise<Project>; UpdateProject(id: string, patch: Record<string, unknown>): Promise<Project>; DeleteProject(id: string): Promise<void>
  ListRecentDirs(): Promise<string[]>; SelectDirectory(): Promise<string>; CheckPaths(paths: string[]): Promise<PathStatus[]>
  GetSettings(): Promise<Settings>; SaveSettings(settings: Settings): Promise<void>; StartScan(id: string): Promise<void>; CancelScan(id: string): Promise<void>; GetScanStatus(id: string): Promise<ScanStatus>
  ListGroups(id: string, filter: Record<string, unknown>): Promise<Group[]>; GetGroup(id: string, groupId: number): Promise<GroupDetail>; SetGroupStatus(id: string, groupId: number, status: string): Promise<void>; SetRecommended(id: string, groupId: number, fileId: number): Promise<void>
  GetThumbURL(id: string, fileId: number): Promise<string>; GetFrameURLs(id: string, fileId: number): Promise<string[]>; DeleteFiles(request: Record<string, unknown>): Promise<DeleteResult>; ExportReport(id: string, format: string, scope: string): Promise<string>
  CheckUpdate(): Promise<UpdateInfo>; GetPendingUpdate(): Promise<UpdateInfo | null>; DownloadUpdate(proxy: boolean): Promise<void>; CancelUpdateDownload(): Promise<void>; ApplyUpdateAndRestart(): Promise<void>; OpenURL(url: string): Promise<void>; OpenPath(path: string): Promise<void>; RevealInFolder(path: string): Promise<void>; CopyToClipboard(text: string): Promise<void>
}

type WailsWindow = Window & { go?: { appapi?: { App?: AppAPI } }; runtime?: { EventsOn: (name: string, cb: (payload: any) => void) => () => void } }
export const mockMode = import.meta.env.DEV && import.meta.env.MODE === "mock"
const native = () => (window as WailsWindow).go?.appapi?.App

function mockApi(): AppAPI {
  const projects: Project[] = []
  let next = 1
  const unsupported = (name: string): never => { throw new Error(`预览模式不支持${name}，请在 Wails 应用中执行`) }
  return {
    AppReady: async () => ({ version: "0.0.0-dev", ffmpeg: true, ffprobe: true, appData: "/tmp/media-dedupe", deleteMode: "recycle", engine: "SQLite + SHA-256 + pHash + FFmpeg", platform: "preview" }), GetSystemAppearance: async () => "light",
    ListProjects: async () => structuredClone(projects), GetProject: async (id) => { const p = projects.find((x) => x.id === id); if (!p) throw new Error("项目不存在"); return structuredClone(p) },
    CreateProject: async (input) => { const p = { id: `prj_${next++}`, name: String(input.name || "未命名"), paths: (input.paths as string[]) || [], recursive: true, threshold: 0.8, includeImages: true, includeVideos: true, includeTexts: true, textThreshold: 0.92, textWorkers: 2, frameCount: 8, workers: 1, videoWorkers: 1, lastSummary: { filesSeen: 0, groups: 0, reclaimableBytes: 0, exactGroups: 0, similarGroups: 0 }, scanning: false, ffmpegReady: true }; projects.push(p); return p },
    UpdateProject: async (id, patch) => { const p = await (async () => { const x = projects.find((y) => y.id === id); if (!x) throw new Error("项目不存在"); return x })(); Object.assign(p, patch); return p }, DeleteProject: async (id) => { const i = projects.findIndex((p) => p.id === id); if (i >= 0) projects.splice(i, 1) },
    ListRecentDirs: async () => ["/tmp", "/Users/demo/Pictures"], SelectDirectory: async () => unsupported("目录选择"), CheckPaths: async (paths) => paths.map((path) => ({ path, exists: !path.includes("missing"), isDir: true, error: path.includes("missing") ? "路径不存在" : "" })),
    GetSettings: async () => ({ defaultThreshold: 0.8, defaultIncludeVideos: true, defaultIncludeTexts: true, defaultTextThreshold: 0.92, defaultTextWorkers: 2, defaultFrameCount: 8, defaultWorkers: 1, defaultVideoWorkers: 1, defaultDeleteMode: "recycle", allowPermanentDelete: false, ffmpegPath: "", ffprobePath: "", ffmpegAvailable: true, ffprobeAvailable: true }), SaveSettings: async () => unsupported("设置保存"), StartScan: async () => unsupported("扫描"), CancelScan: async () => unsupported("扫描取消"), GetScanStatus: async (id) => ({ projectId: id, running: false, canceled: false, percent: 0, filesSeen: 0, errors: 0 }),
    ListGroups: async () => [], GetGroup: async () => unsupported("结果详情"), SetGroupStatus: async () => unsupported("分组更新"), SetRecommended: async () => unsupported("推荐保留更新"), GetThumbURL: async () => "", GetFrameURLs: async () => [], DeleteFiles: async () => unsupported("删除"), ExportReport: async () => unsupported("报告导出"),
    CheckUpdate: async () => ({ hasUpdate: false, currentVersion: "v0.0.0-dev", latestVersion: "v0.0.0-dev" }), GetPendingUpdate: async () => null, DownloadUpdate: async () => unsupported("更新下载"), CancelUpdateDownload: async () => undefined, ApplyUpdateAndRestart: async () => unsupported("更新重启"), OpenURL: async () => unsupported("打开外部链接"), OpenPath: async () => unsupported("打开文件"), RevealInFolder: async () => unsupported("定位文件"), CopyToClipboard: async (text) => navigator.clipboard.writeText(text),
  }
}

// Resolve at call time: module imports must not throw before React can show the startup error.
export const api: AppAPI = mockMode ? mockApi() : new Proxy({} as AppAPI, {
  get: (_target, method: keyof AppAPI) => (...args: unknown[]) => {
    const value = native()
    if (!value || typeof value[method] !== "function") return Promise.reject(new Error(`Wails API 未加载：${method}。请使用 wails dev，或运行 npm run dev:mock 预览。`))
    return Reflect.apply(value[method], value, args)
  },
})
export const runtime = () => (window as WailsWindow).runtime
