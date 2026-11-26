# 组装分发目录并生成发布归档（Windows 侧）。
#
# 与 tools/package.sh 同责、同产物、同参数语义：
#   tools\package.ps1                标准包
#   tools\package.ps1 -Full          整包（模板 B 带 tools\ 组件）
#   tools\package.ps1 -LegacyFlat    额外产出旧扁平布局，仅供升级回归
#   tools\package.ps1 -Version 1.2.3 覆盖版本号
#
# 产物：
#   dist\<PRODUCT>\                              分发目录（运行时的路径契约）
#   dist\release\<SLUG>_<ver>_<os>_<arch>.zip
#   dist\release\<SLUG>_<ver>_full_<os>_<arch>.zip
#   dist\release\SHA256SUMS.txt
#
# Windows 上不用 zip 命令（Git for Windows 不带），压缩走 Compress-Archive；
# 它与 macOS 的 `zip -qry` 的差异只有一处：符号链接会被解引用，而 Windows 产物
# 里没有符号链接，所以不影响。
[CmdletBinding()]
param(
    [switch]$Full,
    [switch]$LegacyFlat,
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
$Slug = $Config['SLUG']
$Product = $Config['PRODUCT']
$Exe = $Config['EXE']
$HasPython = $Config['HAS_PYTHON']
$PyComponent = $Config['PY_COMPONENT']
$PyBuildName = $Config['PY_BUILD_NAME']
$Components = @($Config['COMPONENTS'] -split '\s+' | Where-Object { $_ })
$Bundled = @($Config['BUNDLED'] -split '\s+' | Where-Object { $_ })

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

$Dist = Join-Path $Root 'dist'
$Stage = Join-Path $Dist $Product
$Rel = Join-Path $Dist 'release'
$Bin = Join-Path $Root 'go\build\bin'

Step "组装分发目录（$Product $Version windows/amd64）"

if (Test-Path $Stage) { Remove-Item -Recurse -Force $Stage }
New-Item -ItemType Directory -Path $Stage -Force | Out-Null

$appExe = Join-Path $Bin "$Exe.exe"
if (-not (Test-Path $appExe)) { Die "未找到 $appExe，请先运行 tools\build.ps1（或 wails build）" }
Copy-Item $appExe $Stage
Note "App：$Exe.exe"

function Get-ComponentSource([string]$Name) {
    if ($PyComponent -and $Name -eq $PyComponent) {
        return (Join-Path $Root "python\build\pyi\dist\$PyBuildName")
    }
    return (Join-Path $Root "third_party\$Name\windows-amd64")
}

function Copy-Component([string]$Name) {
    $src = Get-ComponentSource $Name
    if (-not (Test-Path $src)) { Die "组件 $Name 的产物不存在：$src" }
    $dest = Join-Path $Stage "tools\$Name"
    New-Item -ItemType Directory -Path $dest -Force | Out-Null
    Copy-Item (Join-Path $src '*') $dest -Recurse -Force
    Note "组件：tools\$Name\"
}

if ($Full) {
    if ($HasPython -ne '1') { Note '模板 A 无组件，-Full 与标准包等价' }
    foreach ($c in $Components) { Copy-Component $c }
    foreach ($c in $Bundled) {
        if (Test-Path (Get-ComponentSource $c)) { Copy-Component $c }
        else { Note "缺少 ${c}—— 相关功能降级，其余不受影响" }
    }
}
else {
    Note '标准包：不含 tools\ 组件，首次使用时按需下载'
}

New-Item -ItemType Directory -Path $Rel -Force | Out-Null
$suffix = 'windows_amd64'
if ($Full -and $HasPython -eq '1') {
    $asset = Join-Path $Rel "${Slug}_${Version}_full_${suffix}.zip"
}
else {
    $asset = Join-Path $Rel "${Slug}_${Version}_${suffix}.zip"
}
if (Test-Path $asset) { Remove-Item -Force $asset }

Step "生成归档 $(Split-Path -Leaf $asset)"
if ($HasPython -eq '1') {
    # 模板 B：顶层是唯一目录 <PRODUCT>\，标准包与整包只差 tools\ 有没有。
    Compress-Archive -Path $Stage -DestinationPath $asset -Force
    $expect = $Product
}
else {
    # 模板 A：zip 根就是可执行文件，解压即用。
    Compress-Archive -Path (Join-Path $Stage "$Exe.exe") -DestinationPath $asset -Force
    $expect = "$Exe.exe"
}

$zip = [System.IO.Compression.ZipFile]::OpenRead($asset)
try {
    $names = @($zip.Entries | ForEach-Object { $_.FullName -replace '\\', '/' })
    $stray = @($names | Where-Object { -not ($_ -eq $expect -or $_.StartsWith("$expect/")) })
    if ($stray.Count -gt 0) {
        Die "$(Split-Path -Leaf $asset) 顶层应为 $expect，实际出现：$($stray[0])"
    }
    Write-Host "    归档结构：$($names.Count) 个条目，全部在 $expect 下"
}
finally { $zip.Dispose() }

if ($LegacyFlat) {
    if ($HasPython -ne '1') {
        Note '-LegacyFlat：模板 A 的归档本来就是扁平布局，跳过'
    }
    else {
        $legacy = Join-Path $Dist "legacy\$Product"
        if (Test-Path (Join-Path $Dist 'legacy')) { Remove-Item -Recurse -Force (Join-Path $Dist 'legacy') }
        New-Item -ItemType Directory -Path $legacy -Force | Out-Null
        Copy-Item (Join-Path $Stage "$Exe.exe") $legacy
        foreach ($c in ($Components + $Bundled)) {
            $p = Join-Path $Stage "tools\$c"
            if (Test-Path $p) { Copy-Item (Join-Path $p '*') $legacy -Recurse -Force }
        }
        Note "旧扁平布局：dist\legacy\$Product（不发布，只用于验证老用户升级）"
    }
}

Step '生成 SHA256SUMS.txt'
$sumPath = Join-Path $Rel 'SHA256SUMS.txt'
if (Test-Path $sumPath) { Remove-Item -Force $sumPath }
Get-ChildItem $Rel -Filter '*.zip' | Sort-Object Name | ForEach-Object {
    $hash = (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower()
    "$hash  $($_.Name)" | Add-Content -Path $sumPath -Encoding ascii
}
Get-Content $sumPath

Step '完成'
Note "分发目录：$Stage"
Note "归档目录：$Rel"
