#!/usr/bin/env bash
# 一键构建：Python 组件（模板 B）→ Go/Wails → 组装 dist/<PRODUCT> → 归档 dist/release。
#
# 用法
#   tools/build.sh                 标准包
#   tools/build.sh --full          整包（模板 B 带 tools/ 组件）
#   tools/build.sh --no-python     复用 python/build/pyi/dist 里已有产物
#   tools/build.sh --package-only  只组装与归档，不构建
#   tools/build.sh -v 1.2.3        覆盖版本号（默认取 git tag，无 tag 用 0.0.0-dev）
#
# 为什么把构建收进脚本而不是写在 CI 里：CI 里那份命令没人能在本地跑一遍，
# 「本机过了、CI 挂了」和「CI 过了、用户装了跑不起来」都只能靠推 tag 才发现。
set -euo pipefail

# shellcheck source=common.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
host_platform

want_full=0
build_python=1
build_app=1
version_arg=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --full) want_full=1 ;;
        --no-python) build_python=0 ;;
        --package-only)
            build_python=0
            build_app=0
            ;;
        -v | --version)
            [[ $# -ge 2 ]] || die "$1 需要一个版本号"
            version_arg="$2"
            shift
            ;;
        -h | --help)
            sed -n '3,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "未知参数：$1（可用：--full、--no-python、--package-only、-v）" ;;
    esac
    shift
done

VERSION="$(resolve_version "$version_arg")"
[[ "$HAS_PYTHON" == 1 ]] || build_python=0

echo "==> 目标：${HOST_OS}/${HOST_ARCH}  版本：${VERSION}"

# 每次完整构建都清空归档目录：残留的旧 zip 会被 SHA256SUMS 与资产数量断言算进去，
# 表现为「本地 12 个资产、CI 11 个」。--package-only 不清，它本来就复用已有产物。
if [[ "$build_app" == 1 || "$build_python" == 1 ]]; then
    rm -rf "$ROOT/dist/release"
fi

if [[ "$build_python" == 1 ]]; then
    step "1/4 构建 Python 组件（${PY_BUILD_NAME}）"
    command -v uv >/dev/null 2>&1 || die "未找到 uv。安装：https://docs.astral.sh/uv/"
    # 只改 __version__ 那一行：文件里通常还有「这个值是注入的、产物自述才是结果」
    # 之类的说明，整文件覆盖会把它们一起抹掉，下一个人照着空文件改就丢了依据。
    ver_file="$ROOT/python/src/$PY_PACKAGE/_version.py"
    if command -v python3 >/dev/null 2>&1 && [[ -f "$ver_file" ]]; then
        python3 - "$ver_file" "$VERSION" <<'VEREOF'
import pathlib, re, sys
path = pathlib.Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
line = f'__version__ = "{sys.argv[2]}"'
if re.search(r'^__version__ = ".*"$', text, re.M):
    text = re.sub(r'^__version__ = ".*"$', line, text, count=1, flags=re.M)
else:
    text = text.rstrip("\n") + "\n\n" + line + "\n"
path.write_text(text, encoding="utf-8")
VEREOF
    else
        printf '__version__ = "%s"\n' "$VERSION" > "$ver_file"
    fi
    ( cd "$ROOT/python" && uv sync && rm -rf build/pyi &&
        uv run pyinstaller packaging/pyinstaller.spec --noconfirm \
            --distpath build/pyi/dist --workpath build/pyi/work )
    py_out="$ROOT/python/build/pyi/dist/$PY_BUILD_NAME"
    [[ -e "$py_out" ]] || die "PyInstaller 未产出 $py_out"
    note "产物：python/build/pyi/dist/$PY_BUILD_NAME"
else
    step "1/4 跳过 Python 组件（复用已有产物）"
fi

# 组件包必须排在 wails build 之前：模板 B 会把「本平台的组件清单」编译进 App，
# 而清单要从组件产物里读版本。生成清单的事放 tools/prebuild.sh，各仓库自定。
if [[ -f "$ROOT/tools/component.sh" ]]; then
    step "2/4 生成组件包"
    bash "$ROOT/tools/component.sh" -v "$VERSION"
else
    step "2/4 无组件包（模板 A）"
fi

if [[ -f "$ROOT/tools/prebuild.sh" ]]; then
    step "3/4 预构建钩子（tools/prebuild.sh）"
    bash "$ROOT/tools/prebuild.sh" -v "$VERSION"
else
    step "3/4 无预构建钩子"
fi

if [[ "$build_app" == 1 ]]; then
    step "4/4 构建 Go / Wails 应用（${PRODUCT}）"
    command -v wails >/dev/null 2>&1 || die "未找到 wails CLI。安装：go install github.com/wailsapp/wails/v2/cmd/wails@latest"
    # wails.json 的 productVersion 决定 macOS Info.plist / Windows 文件属性，
    # 与 -ldflags 注入的是同一个版本号。它只改工作区，不提交。
    if command -v python3 >/dev/null 2>&1; then
        ( cd "$ROOT/go" && python3 - "$VERSION" <<'PY'
import json, sys
from pathlib import Path
p = Path("wails.json")
d = json.loads(p.read_text(encoding="utf-8"))
d.setdefault("info", {})["productVersion"] = sys.argv[1]
p.write_text(json.dumps(d, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
PY
        )
    else
        note "未找到 python3，跳过 wails.json productVersion 更新（不影响构建）"
    fi
    wails_args=(-clean -platform "${HOST_OS}/${HOST_ARCH}")
    # GO_MODULE 为空表示这个仓库没有 internal/version 注入点（版本只体现在
    # wails.json 的 productVersion 上），此时不要硬塞 -X。
    if [[ -n "$GO_MODULE" ]]; then
        wails_args+=(-ldflags "-X ${GO_MODULE}/internal/version.Version=${VERSION}")
    fi
    # Windows 上 wails 2.16 会省略 .exe，显式给 -o 才能拿到确定的文件名。
    [[ "$HOST_OS" == windows ]] && wails_args+=(-o "$EXE.exe")
    ( cd "$ROOT/go" && wails build "${wails_args[@]}" )
    note "产物：go/build/bin/"
else
    step "4/4 跳过 Go / Wails 构建（复用已有产物）"
fi

step "组装与归档"
package_args=(-v "$VERSION")
[[ "$want_full" == 1 ]] && package_args+=(--full)
"$ROOT/tools/package.sh" "${package_args[@]}"
