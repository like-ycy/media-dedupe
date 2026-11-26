# 一键构建（Windows 侧）：Python 组件（模板 B）→ Go/Wails -> dist -> 归档。
#
# 与 tools/build.sh 同责、同参数语义：
#   tools\build.ps1                 标准包
#   tools\build.ps1 -Full           整包（模板 B 带 tools\ 组件）
#   tools\build.ps1 -NoPython       复用 python\build\pyi\dist 里已有产物
#   tools\build.ps1 -PackageOnly    只组装与归档，不构建
#   tools\build.ps1 -Version 1.2.3  覆盖版本号
[CmdletBinding()]
param(
    [switch]$Full,
    [switch]$NoPython,
    [switch]$PackageOnly,
    [string]$Version = ''
)

$ErrorActionPreference = 'Stop'

$Root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$Config = @{}
Get-Content (Join-Path $PSScriptRoot 'config.env') | ForEach-Object {
    $line = $_.Trim()
    if ($line -eq '' -or $line.StartsWith('#')) { return }
    $i = $line.IndexOf('=')
    if ($i -lt 1) { return }
    $Config[$line.Substring(0, $i).Trim()] = $line.Substring($i + 1).Trim()
}
$Product = $Config['PRODUCT']
$Exe = $Config['EXE']
$GoModule = $Config['GO_MODULE']
$HasPython = $Config['HAS_PYTHON']
$PyPackage = $Config['PY_PACKAGE']
$PyBuildName = $Config['PY_BUILD_NAME']

function Die([string]$Text) { throw $Text }
function Step([string]$Text) { Write-Host ''; Write-Host "==> $Text" -ForegroundColor Cyan }
function Note([string]$Text) { Write-Host "    $Text" }

if (-not $Version) {
    if ($env:VERSION) {
        $Version = $env:VERSION -replace '^v', ''
    }
    else {
        $tag = (git -C $Root describe --tags --exact-match 2>$null)
        $Version = if ($tag) { $tag -replace '^v', '' } else { '0.0.0-dev' }
    }
}

$buildPython = ($HasPython -eq '1') -and (-not $NoPython) -and (-not $PackageOnly)
$buildApp = -not $PackageOnly

if (-not $PackageOnly) {
    $rel = Join-Path $Root 'dist\release'
    if (Test-Path $rel) { Remove-Item -Recurse -Force $rel }
}

Write-Host "==> 目标：windows/amd64  版本：$Version"

if ($buildPython) {
    Step "1/4 构建 Python 组件（$PyBuildName）"
    if (-not (Get-Command uv -ErrorAction SilentlyContinue)) { Die '未找到 uv，安装见 https://docs.astral.sh/uv/' }
    # 只改 __version__ 那一行，保住文件里的说明（与 build.sh 同责）。
    $verFile = Join-Path $Root "python\src\$PyPackage\_version.py"
    if (Test-Path $verFile) {
        $replaced = $false
        $lines = Get-Content $verFile | ForEach-Object {
            if (-not $replaced -and $_ -match '^__version__ = ".*"$') {
                $replaced = $true
                "__version__ = `"$Version`""
            }
            else { $_ }
        }
        if (-not $replaced) { $lines = @($lines) + "__version__ = `"$Version`"" }
        Set-Content -Path $verFile -Value $lines -Encoding utf8
    }
    else {
        "__version__ = `"$Version`"" | Set-Content -Path $verFile -Encoding utf8
    }
    Push-Location (Join-Path $Root 'python')
    try {
        & uv sync
        if ($LASTEXITCODE -ne 0) { Die 'uv sync 失败' }
        if (Test-Path 'build\pyi') { Remove-Item -Recurse -Force 'build\pyi' }
        & uv run pyinstaller packaging/pyinstaller.spec --noconfirm `
            --distpath build/pyi/dist --workpath build/pyi/work
        if ($LASTEXITCODE -ne 0) { Die 'PyInstaller 失败' }
    }
    finally { Pop-Location }
    $pyOut = Join-Path $Root "python\build\pyi\dist\$PyBuildName"
    if (-not (Test-Path $pyOut)) { Die "PyInstaller 未产出 $pyOut" }
    Note "产物：python\build\pyi\dist\$PyBuildName"
}
else {
    Step '1/4 跳过 Python 组件（复用已有产物）'
}

if (Test-Path (Join-Path $Root 'tools\component.ps1')) {
    Step '2/4 生成组件包'
    & (Join-Path $Root 'tools\component.ps1') -Version $Version
    if ($LASTEXITCODE -ne 0) { Die 'component.ps1 失败' }
}
else {
    Step '2/4 无组件包（模板 A）'
}

if (Test-Path (Join-Path $Root 'tools\prebuild.ps1')) {
    Step '3/4 预构建钩子（tools\prebuild.ps1）'
    & (Join-Path $Root 'tools\prebuild.ps1') -Version $Version
    if ($LASTEXITCODE -ne 0) { Die 'prebuild.ps1 失败' }
}
else {
    Step '3/4 无预构建钩子'
}

if ($buildApp) {
    Step "4/4 构建 Go / Wails 应用（$Product）"
    if (-not (Get-Command wails -ErrorAction SilentlyContinue)) {
        Die '未找到 wails CLI，安装：go install github.com/wailsapp/wails/v2/cmd/wails@latest'
    }
    # productVersion 决定 Windows 文件属性，与 -ldflags 注入的是同一个版本号。
    $wailsJson = Join-Path $Root 'go\wails.json'
    $json = Get-Content $wailsJson -Raw | ConvertFrom-Json
    if (-not $json.info) { $json | Add-Member -NotePropertyName info -NotePropertyValue ([pscustomobject]@{}) }
    $json.info | Add-Member -NotePropertyName productVersion -NotePropertyValue $Version -Force
    $json | ConvertTo-Json -Depth 8 | Set-Content -Path $wailsJson -Encoding utf8

    Push-Location (Join-Path $Root 'go')
    try {
        $wailsArgs = @('build', '-clean', '-platform', 'windows/amd64', '-o', "$Exe.exe")
        # GO_MODULE 为空表示这个仓库没有 internal/version 注入点，不要硬塞 -X。
        if ($GoModule) {
            $wailsArgs += @('-ldflags', "-X $GoModule/internal/version.Version=$Version")
        }
        & wails @wailsArgs
        if ($LASTEXITCODE -ne 0) { Die 'wails build 失败' }
    }
    finally { Pop-Location }
    Note '产物：go\build\bin\'
}
else {
    Step '4/4 跳过 Go / Wails 构建（复用已有产物）'
}

Step '组装与归档'
$pkg = Join-Path $Root 'tools\package.ps1'
$pkgArgs = @('-Version', $Version)
if ($Full) { $pkgArgs += '-Full' }
& $pkg @pkgArgs
