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
  const makeItem = (fileId: number, path: string, isRecommended: boolean, sizeMb: number, extra: Partial<Item> = {}): Item => ({
    fileId, path, action: isRecommended ? "keep_recommended" : "delete", similarity: 1, qualityScore: isRecommended ? 0.92 : 0.61, sizeBytes: Math.round(sizeMb * 1024 * 1024),
    thumbUrl: "", width: isRecommended ? 4000 : 3200, height: isRecommended ? 3000 : 2400, durationMs: 0, mediaType: "image", mediaHashHex: "a".repeat(16),
    isRecommended, exists: true, reasons: isRecommended ? ["分辨率更高"] : [], ...extra,
  })
  const mockGroups: GroupDetail[] = [
    { groupId: 1, groupType: "exact", typeLabel: "精确重复", confidence: 1, recommendedFileId: 11, status: "pending", reclaimableBytes: 2.4 * 1024 * 1024, items: [makeItem(11, "/photos/IMG_0001.jpg", true, 2.4), makeItem(12, "/photos/IMG_0001 (copy).jpg", false, 2.1), makeItem(13, "/backup/IMG_0001.jpg", false, 1.8)] },
    { groupId: 2, groupType: "similar_image", typeLabel: "视觉近似图片", confidence: 0.93, recommendedFileId: 21, status: "pending", reclaimableBytes: 3.1 * 1024 * 1024, items: [makeItem(21, "/photos/trip/DSC_0100.jpg", true, 3.1, { hammingDistance: 0 }), makeItem(22, "/photos/trip/edit/DSC_0100_edit.jpg", false, 2.6, { hammingDistance: 4 })] },
    { groupId: 3, groupType: "exact", typeLabel: "精确重复", confidence: 1, recommendedFileId: 31, status: "pending", reclaimableBytes: 5 * 1024 * 1024, items: [makeItem(31, "/videos/raw/clip.mp4", true, 5, { mediaType: "video", durationMs: 12000 }), makeItem(32, "/videos/export/clip.mp4", false, 4.2, { mediaType: "video", durationMs: 12000 })] },
  ]
  return {
    AppReady: async () => ({ version: "0.0.0-dev", ffmpeg: true, ffprobe: true, appData: "/tmp/media-dedupe", deleteMode: "recycle", engine: "SQLite + SHA-256 + pHash + FFmpeg", platform: "preview" }), GetSystemAppearance: async () => "light",
    ListProjects: async () => structuredClone(projects), GetProject: async (id) => { const p = projects.find((x) => x.id === id); if (!p) throw new Error("项目不存在"); return structuredClone(p) },
    CreateProject: async (input) => { const p = { id: `prj_${next++}`, name: String(input.name || "未命名"), paths: (input.paths as string[]) || [], recursive: true, threshold: 0.8, includeImages: true, includeVideos: true, includeTexts: true, textThreshold: 0.92, textWorkers: 2, frameCount: 8, workers: 1, videoWorkers: 1, lastSummary: { filesSeen: 12, groups: mockGroups.filter((g) => g.status === "pending").length, reclaimableBytes: mockGroups.reduce((n, g) => n + g.reclaimableBytes, 0), exactGroups: 2, similarGroups: 1, pendingGroups: mockGroups.filter((g) => g.status === "pending").length }, scanning: false, ffmpegReady: true }; projects.push(p); return structuredClone(p) },
    UpdateProject: async (id, patch) => { const p = await (async () => { const x = projects.find((y) => y.id === id); if (!x) throw new Error("项目不存在"); return x })(); Object.assign(p, patch); return structuredClone(p) }, DeleteProject: async (id) => { const i = projects.findIndex((p) => p.id === id); if (i >= 0) projects.splice(i, 1) },
    ListRecentDirs: async () => ["/tmp", "/Users/demo/Pictures"], SelectDirectory: async () => unsupported("目录选择"), CheckPaths: async (paths) => paths.map((path) => ({ path, exists: !path.includes("missing"), isDir: true, error: path.includes("missing") ? "路径不存在" : "" })),
    GetSettings: async () => ({ defaultThreshold: 0.8, defaultIncludeVideos: true, defaultIncludeTexts: true, defaultTextThreshold: 0.92, defaultTextWorkers: 2, defaultFrameCount: 8, defaultWorkers: 1, defaultVideoWorkers: 1, defaultDeleteMode: "recycle", allowPermanentDelete: false, ffmpegPath: "", ffprobePath: "", ffmpegAvailable: true, ffprobeAvailable: true }), SaveSettings: async () => undefined, StartScan: async () => unsupported("扫描"), CancelScan: async () => unsupported("扫描取消"), GetScanStatus: async (id) => ({ projectId: id, running: false, canceled: false, percent: 0, filesSeen: 0, errors: 0 }),
    ListGroups: async (_id, filter: Record<string, unknown> = {}) => mockGroups.filter((g) => !filter.status || filter.status === "all" || g.status === filter.status).map((g) => ({ groupId: g.groupId, groupType: g.groupType, confidence: g.confidence, recommendedFileId: g.recommendedFileId, status: g.status, memberCount: g.items.length, reclaimableBytes: g.reclaimableBytes, coverThumbUrl: "", typeLabel: g.typeLabel, mediaKind: g.items[0]?.mediaType || "image" })),
    GetGroup: async (_id, groupId) => { const g = mockGroups.find((x) => x.groupId === groupId); if (!g) throw new Error("组不存在"); return structuredClone(g) },
    SetGroupStatus: async (_id, groupId, status) => { const g = mockGroups.find((x) => x.groupId === groupId); if (g) g.status = status },
    SetRecommended: async (_id, groupId, fileId) => { const g = mockGroups.find((x) => x.groupId === groupId); if (!g) return; g.recommendedFileId = fileId; for (const item of g.items) item.isRecommended = item.fileId === fileId },
    GetThumbURL: async () => "", GetFrameURLs: async () => [],
    DeleteFiles: async (request) => { const paths = (request.paths as string[]) || []; const groupIds = (request.groupIds as number[]) || []; for (const gid of groupIds) { const g = mockGroups.find((x) => x.groupId === gid); if (!g) continue; g.items = g.items.filter((x) => !paths.includes(x.path)); if (g.items.length <= 1) g.status = "processed" } return { succeeded: paths, failed: [], mode: String(request.mode || "recycle"), markedProcessed: groupIds.filter((gid) => mockGroups.find((x) => x.groupId === gid)?.status === "processed") } },
    ExportReport: async () => unsupported("报告导出"),
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
