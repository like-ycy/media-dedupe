#!/usr/bin/env bash
# tools/*.sh 的共用部分：路径、配置、平台探测、版本号、日志。
#
# 这个文件与其余三个仓库逐字相同（真实仓库名见各自 tools/config.env），
# 改动请四个仓库一起改，否则「同一套打包」就只剩说法。
#
# 只被 source，不单独执行。

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
. "$ROOT/tools/config.env"

die() { echo "错误：$*" >&2; exit 1; }

step() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

note() { printf '    %s\n' "$*"; }

# 版本号优先级：--version 参数 > VERSION 环境变量 > git tag > 0.0.0-dev。
# 不带 v 前缀：归档名与 -ldflags 注入的都用这个值。
resolve_version() {
    local arg="${1:-}"
    if [[ -n "$arg" ]]; then
        printf '%s' "${arg#v}"
        return
    fi
    if [[ -n "${VERSION:-}" ]]; then
        printf '%s' "${VERSION#v}"
        return
    fi
    local tag
    tag="$(git -C "$ROOT" describe --tags --exact-match 2>/dev/null || true)"
    if [[ -n "$tag" ]]; then
        printf '%s' "${tag#v}"
    else
        printf '0.0.0-dev'
    fi
}

# HOST_OS 取 GOOS 的值（darwin / windows），HOST_ARCH 取 GOARCH 的值。
# 归档名里的平台段必须等于 GOOS：更新器按 runtime.GOOS 拼后缀找资产，
# 换成 "macos" 会让老版本用户的自更新静默失效。
host_platform() {
    case "$(uname -s)" in
        Darwin) HOST_OS=darwin ;;
        MINGW* | MSYS* | CYGWIN*) HOST_OS=windows ;;
        *) die "只支持 macOS 与 Windows（Git Bash）构建，当前系统：$(uname -s)" ;;
    esac
    case "$(uname -m)" in
        arm64 | aarch64) HOST_ARCH=arm64 ;;
        x86_64 | amd64) HOST_ARCH=amd64 ;;
        *) die "未识别的 CPU 架构：$(uname -m)" ;;
    esac
}

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$@"
    else
        shasum -a 256 "$@"
    fi
}

# 把 <dir>/<top> 打包成 zip，zip 内顶层条目就是 top。
# -y 保留符号链接（.app 里的 .dylib 链接不能解引用成副本），
# -X 不写入 uid/gid 与资源分支（macOS 上会平白多出几 MB 的 ._ 文件）。
zip_top() {
    local base="$1" top="$2" out="$3"
    [[ -e "$base/$top" ]] || die "待打包路径不存在：$base/$top"
    ( cd "$base" && zip -qry -X "$out" "$top" )
}

# 断言 zip 的顶层条目只有期望的那一个，防止多套一层或少套一层。
# 客户端解压后「找不到可执行文件」的根因基本都是这个，而构建会一路全绿。
assert_zip_top() {
    local zip="$1" expect="$2"
    local names
    names="$(unzip -Z1 "$zip")"
    # 空名单说明 zip 读不出来（损坏或不是 zip）。不拦的话下面的 grep -v 会
    # 输出空、判断为「没有多余条目」，坏包一路绿灯到最后。
    [[ -n "$names" ]] || die "$(basename "$zip") 读不出条目，归档可能损坏"
    local stray
    stray="$(printf '%s\n' "$names" | grep -v "^${expect}\(/\|\$\)" || true)"
    if [[ -n "$stray" ]]; then
        printf '%s\n' "$stray" | head -5 >&2
        die "$(basename "$zip") 顶层应为 ${expect}/，实际出现上面的条目"
    fi
    note "归档结构：$(printf '%s\n' "$names" | wc -l | tr -d ' ') 个条目，全部在 ${expect}/ 下"
}
