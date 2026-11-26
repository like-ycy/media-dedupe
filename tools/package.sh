#!/usr/bin/env bash
# 组装分发目录并生成发布归档。
#
# 用法
#   tools/package.sh                    标准包（模板 B 不含 tools/ 组件）
#   tools/package.sh --full             整包（模板 B 带 tools/ 组件）
#   tools/package.sh --legacy-flat      额外产出旧扁平布局，仅供升级回归
#   tools/package.sh -v 1.2.3           覆盖版本号
#
# 前置：wails build 已产出 go/build/bin/ 下的 App（整包还需 Python 产物）。
# 产物：
#   dist/<PRODUCT>/                     分发目录（运行时的路径契约）
#   dist/release/<SLUG>_<ver>_<os>_<arch>.zip
#   dist/release/<SLUG>_<ver>_full_<os>_<arch>.zip
#   dist/release/SHA256SUMS.txt
#
# 目录契约（Go 侧按同一路径探测，见 go/internal/toolpath | locate）：
#   <PRODUCT>.app | <EXE>.exe
#   tools/<组件>/<可执行文件>
#
# 这个文件与其余三个仓库逐字相同，项目差异全部在 tools/config.env。
set -euo pipefail

# shellcheck source=common.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
host_platform

want_full=0
want_legacy=0
version_arg=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --full) want_full=1 ;;
        --legacy-flat) want_legacy=1 ;;
        -v | --version)
            [[ $# -ge 2 ]] || die "$1 需要一个版本号"
            version_arg="$2"
            shift
            ;;
        -h | --help)
            sed -n '3,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *) die "未知参数：$1（可用：--full、--legacy-flat、-v）" ;;
    esac
    shift
done

VERSION="$(resolve_version "$version_arg")"
DIST="$ROOT/dist"
STAGE="$DIST/$PRODUCT"
REL="$DIST/release"
BIN="$ROOT/go/build/bin"

step "组装分发目录（$PRODUCT $VERSION ${HOST_OS}/${HOST_ARCH}）"

# ── App ────────────────────────────────────────────────────────────────
if [[ "$HOST_OS" == darwin ]]; then
    app="$BIN/$PRODUCT.app"
    if [[ ! -d "$app" ]]; then
        # wails.json 的 name 决定 .app 目录名；这里只兜底大小写敏感的卷，
        # 不做「随便找个 .app」那种猜测 —— 名字不对就是打包脚本和 wails.json 脱节了。
        candidates=("$BIN"/*.app)
        if [[ ${#candidates[@]} -eq 1 ]]; then
            app="${candidates[0]}"
            note "wails.json 的 name 与 PRODUCT=${PRODUCT} 不一致，实际产物：$(basename "$app")"
        else
            die "未找到 $BIN/$PRODUCT.app，请先运行 tools/build.sh（或 wails build）"
        fi
    fi
else
    app="$BIN/$EXE.exe"
    [[ -f "$app" ]] || die "未找到 $BIN/$EXE.exe，请先运行 tools/build.ps1（或 wails build）"
fi

rm -rf "$STAGE"
mkdir -p "$STAGE"
cp -R "$app" "$STAGE/"
note "App：$(basename "$app")"

# ── 组件 ──────────────────────────────────────────────────────────────
# 组件产物只有一个来源：Python 的 onedir 产物（PY_COMPONENT），或 third_party 下的
# 预置二进制。两者在 dist 里都是「同名目录 + 目录内可执行文件」的形态。
component_src() {
    local c="$1"
    if [[ -n "${PY_COMPONENT:-}" && "$c" == "$PY_COMPONENT" ]]; then
        printf '%s' "$ROOT/python/build/pyi/dist/$PY_BUILD_NAME"
    else
        printf '%s' "$ROOT/third_party/$c/$HOST_OS-$HOST_ARCH"
    fi
}

copy_component() {
    local name="$1" src="$2"
    [[ -e "$src" ]] || die "组件 $name 的产物不存在：$src"
    mkdir -p "$STAGE/tools/$name"
    # 拷「源目录的内容」而不是源目录本身：后者在目标已存在时会多出一层。
    cp -R "$src"/. "$STAGE/tools/$name/"
    note "组件：tools/$name/"
}

# ── 标准包（先打，此时 STAGE 里只有 App）────────────────────────────────
# 顺序是刻意的：标准包必须在拷组件之前归档，这样「标准包不含 tools/」不需要
# 额外的临时目录或二次拷贝 —— 它就是「拷组件之前的那棵树」。
mkdir -p "$REL"
suffix="${HOST_OS}_${HOST_ARCH}"
asset_std="$REL/${SLUG}_${VERSION}_${suffix}.zip"
rm -f "$asset_std"

step "生成标准包 $(basename "$asset_std")"
if [[ "$HAS_PYTHON" == 1 ]]; then
    # 模板 B：顶层是唯一目录 <PRODUCT>/，标准包与整包只差 tools/ 有没有。
    zip_top "$DIST" "$PRODUCT" "$asset_std"
    assert_zip_top "$asset_std" "$PRODUCT"
    # 名单先落盘再判断：`unzip | grep -q` 里 grep 一命中就退出，unzip 被 SIGPIPE
    # 顶死，pipefail 下整条管道算失败 —— 恰好会把「确实有问题」判成「没问题」。
    names_std="$(unzip -Z1 "$asset_std")"
    if printf '%s\n' "$names_std" | grep -q "^${PRODUCT}/tools/"; then
        die "标准包里出现了 tools/ —— 组件顺序被改乱了"
    fi
else
    # 模板 A：zip 根就是可执行文件（.app 或 .exe），解压即用。
    if [[ "$HOST_OS" == darwin ]]; then
        top="$PRODUCT.app"
    else
        top="$EXE.exe"
    fi
    zip_top "$STAGE" "$top" "$asset_std"
    assert_zip_top "$asset_std" "$top"
fi

# ── 组件 ──────────────────────────────────────────────────────────────
# 组件产物只有一个来源：Python 的 onedir 产物（PY_COMPONENT），或 third_party 下的
# 预置二进制。两者在 dist 里都是「同名目录 + 目录内可执行文件」的形态。
component_src() {
    local c="$1"
    if [[ -n "${PY_COMPONENT:-}" && "$c" == "$PY_COMPONENT" ]]; then
        printf '%s' "$ROOT/python/build/pyi/dist/$PY_BUILD_NAME"
    else
        printf '%s' "$ROOT/third_party/$c/$HOST_OS-$HOST_ARCH"
    fi
}

copy_component() {
    local name="$1" src="$2"
    [[ -e "$src" ]] || die "组件 $name 的产物不存在：$src"
    mkdir -p "$STAGE/tools/$name"
    # 拷「源目录的内容」而不是源目录本身：后者在目标已存在时会多出一层。
    cp -R "$src"/. "$STAGE/tools/$name/"
    note "组件：tools/$name/"
}

if [[ "$want_full" == 1 ]]; then
    if [[ "$HAS_PYTHON" != 1 ]]; then
        note "模板 A 无组件，--full 与标准包等价"
    else
        for c in $COMPONENTS; do
            copy_component "$c" "$(component_src "$c")"
        done
        for c in $BUNDLED; do
            src="$(component_src "$c")"
            if [[ -d "$src" ]]; then
                copy_component "$c" "$src"
            else
                note "缺少 ${c}（${src}）—— 相关功能降级，其余不受影响"
            fi
        done
    fi

    if [[ "$HAS_PYTHON" == 1 ]]; then
        asset_full="$REL/${SLUG}_${VERSION}_full_${suffix}.zip"
        rm -f "$asset_full"
        step "生成整包 $(basename "$asset_full")"
        zip_top "$DIST" "$PRODUCT" "$asset_full"
        assert_zip_top "$asset_full" "$PRODUCT"
        names_full="$(unzip -Z1 "$asset_full")"
        for c in $COMPONENTS $BUNDLED; do
            [[ -e "$STAGE/tools/$c" ]] || continue
            printf '%s\n' "$names_full" | grep -q "^${PRODUCT}/tools/$c/" ||
                die "整包里缺少 tools/${c}/"
        done
    fi
else
    note "标准包：不含 tools/ 组件，首次使用时按需下载"
fi

# ── 旧扁平布局（仅模板 B，仅升级回归） ─────────────────────────────────
if [[ "$want_legacy" == 1 ]]; then
    if [[ "$HAS_PYTHON" != 1 ]]; then
        note "--legacy-flat：模板 A 的归档本来就是扁平布局，跳过"
    else
        legacy="$DIST/legacy/$PRODUCT"
        rm -rf "$DIST/legacy"
        mkdir -p "$legacy"
        if [[ "$HOST_OS" == darwin ]]; then
            cp -R "$STAGE/$PRODUCT.app" "$legacy/"
            for c in $COMPONENTS $BUNDLED; do
                [[ -e "$STAGE/tools/$c" ]] || continue
                cp "$STAGE/tools/$c/${c}" "$legacy/" 2>/dev/null ||
                    cp -R "$STAGE/tools/$c" "$legacy/"
            done
        else
            cp "$STAGE/$EXE.exe" "$legacy/"
            for c in $COMPONENTS $BUNDLED; do
                [[ -e "$STAGE/tools/$c" ]] || continue
                cp -R "$STAGE/tools/$c"/. "$legacy/"
            done
        fi
        note "旧扁平布局：dist/legacy/${PRODUCT}（不发布，只用于验证老用户升级）"
    fi
fi

# ── 校验和 ────────────────────────────────────────────────────────────
step "生成 SHA256SUMS.txt"
( cd "$REL" && rm -f SHA256SUMS.txt && sha256_file ./*.zip > SHA256SUMS.txt && cat SHA256SUMS.txt )

step "完成"
note "分发目录：${STAGE}（$(du -sh "$STAGE" | cut -f1)）"
note "归档目录：$REL"
