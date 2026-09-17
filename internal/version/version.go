package version

import (
	"strconv"
	"strings"
)

// Version 是当前软件的编译版本号。
// 本地/CI 编译时通过 -ldflags "-X media-dedupe/internal/version.Version=x.y.z" 注入。
// 未注入时回落到开发版本，避免本地构建误报为正式版。
var Version = "0.0.0-dev"

// GetVersion 返回当前版本号字符串（带 v 前缀，便于展示）。
func GetVersion() string {
	v := strings.TrimSpace(Version)
	if v == "" {
		v = "0.0.0-dev"
	}
	if !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}

// Compare 返回 a 与 b 的版本比较结果：
// 若 a > b 返回 1
// 若 a < b 返回 -1
// 若 a == b 返回 0
func Compare(a, b string) int {
	aSegs, aPre := parseVersion(a)
	bSegs, bPre := parseVersion(b)

	maxLen := len(aSegs)
	if len(bSegs) > maxLen {
		maxLen = len(bSegs)
	}

	for i := 0; i < maxLen; i++ {
		var aVal, bVal int
		if i < len(aSegs) {
			aVal = aSegs[i]
		}
		if i < len(bSegs) {
			bVal = bSegs[i]
		}
		if aVal > bVal {
			return 1
		}
		if aVal < bVal {
			return -1
		}
	}

	// 主版本段一致时，正式版高于预发布版（空 pre-release > 非空 pre-release）
	if aPre == "" && bPre != "" {
		return 1
	}
	if aPre != "" && bPre == "" {
		return -1
	}
	if aPre > bPre {
		return 1
	}
	if aPre < bPre {
		return -1
	}

	return 0
}

// parseVersion 解析形如 "v1.2.3-beta" 为数字段 [1, 2, 3] 和 pre-release 字符串。
func parseVersion(v string) ([]int, string) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")

	parts := strings.SplitN(v, "-", 2)
	mainPart := parts[0]
	prePart := ""
	if len(parts) > 1 {
		prePart = parts[1]
	}

	subParts := strings.Split(mainPart, ".")
	var segs []int
	for _, s := range subParts {
		n, _ := strconv.Atoi(s)
		segs = append(segs, n)
	}
	return segs, prePart
}
