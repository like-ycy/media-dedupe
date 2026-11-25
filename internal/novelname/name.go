// Package novelname extracts evidence from TXT filenames without treating a
// shared title as proof that two files contain the same text.
package novelname

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

type Range struct {
	Start, End int
	Unit       string
}

type Name struct {
	Title    string
	Keys     []string
	Authors  []string
	Ranges   []Range
	Parts    []string
	Versions []string
	Notes    []string
}

var (
	extensionRE   = regexp.MustCompile(`(?i)(?:\.txt)+$`)
	urlRE         = regexp.MustCompile(`(?i)(?:https?://)?(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+(?:com|net|org|cn|cc|me|xyz|info|club|top)(?:/[a-z0-9_./?=&%-]*)?`)
	siteRE        = regexp.MustCompile(`(?i)\[?搜书吧(?:网址)?\]?|\[书香门第\]|_?派派小说|_?powered_by_discuz!`)
	authorRE      = regexp.MustCompile(`作者\s*[:∶-]?\s*|作家\s*[:∶-]?\s*|\bby\s+|[》】]by\s*`)
	numberPattern = `[0-9零〇一二三四五六七八九十百千万两]+`
	rangeRE       = regexp.MustCompile(`(?:第)?(` + numberPattern + `)\s*[-_~～—–至到]+\s*(?:第)?(` + numberPattern + `)\s*([章节回卷部篇集]?)`)
	progressRE    = regexp.MustCompile(`(?:更新至|更新到|更至|连载至|[\[\(【](?:更|至))\s*(?:第)?(` + numberPattern + `)\s*([章节回卷部篇集]?)`)
	blockRE       = regexp.MustCompile(`[\(\[【]([^()\[\]【】]*)[\)\]】]`)
	partRE        = regexp.MustCompile(`[\(\[【]\s*([上中下]|[一二三四五六七八九十]+)\s*[\)\]】]`)
	versionRE     = regexp.MustCompile(`无绿(?:版)?|非绿(?:版)?|有绿(?:版)?|原版|二改|改写|改编|修改版|修订版|加强版|同人|前传|后传|外传|番外|续篇|续集|续写|(?:^|[-_\s·])改(?:$|[-_\s])`)
	statusRE      = regexp.MustCompile(`未删节|未删减|无删减|未删|全本|全集|完本|完结|全书完|全文完|全文字|珍藏版?|精校版?|校对版?|完整版?|连载|更新至|更新到|更新|更至|已完|已|全|完|章|节|回|卷|部|篇|集`)
	copyRE        = regexp.MustCompile(`^[0-9]+$`)
	exportRE      = regexp.MustCompile(`^\s*[0-9]+(?:\.[0-9]+)?\s*《`)
	titleRE       = regexp.MustCompile(`^\s*《([^《》]+)》`)
)

// Parse keeps chapter and edition evidence separate from the title. Unknown
// suffixes remain in the key; we prefer a reviewable miss to erasing a title.
func Parse(filename string) Name {
	s := norm.NFKC.String(strings.ToLower(filename))
	s = extensionRE.ReplaceAllString(strings.TrimSpace(s), "")
	n := Name{}
	// A hyphen before a known host is a packaging delimiter, not part of the
	// hostname (e.g. 1-57-soushu2022.com). Keep the chapter range intact.
	s = regexpHostBoundary(s)
	if urlRE.MatchString(s) {
		n.Notes = append(n.Notes, "已分离网址")
		s = urlRE.ReplaceAllString(s, " ")
	}
	s = siteRE.ReplaceAllString(s, " ")
	metadataSource := s
	for _, m := range partRE.FindAllStringSubmatch(s, -1) {
		n.Parts = appendUnique(n.Parts, m[1])
	}
	for _, v := range versionRE.FindAllString(s, -1) {
		n.Versions = appendUnique(n.Versions, strings.Trim(v, " -_·"))
	}
	if m := authorRE.FindStringIndex(s); m != nil {
		author := strings.Trim(s[m[1]:], " :∶-()[]【】《》_")
		if i := strings.IndexAny(author, "[【"); i >= 0 {
			author = author[:i]
		}
		author = progressRE.ReplaceAllString(author, "")
		author = rangeRE.ReplaceAllString(author, "")
		for _, a := range strings.FieldsFunc(author, func(r rune) bool { return strings.ContainsRune("&+、,()（）", r) }) {
			a = Key(a)
			if a != "" && a != "未知" && a != "不详" && a != "多人" {
				n.Authors = appendUnique(n.Authors, a)
			}
		}
		s = s[:m[0]]
	}
	for _, m := range rangeRE.FindAllStringSubmatch(metadataSource, -1) {
		start, end := number(m[1]), number(m[2])
		if start >= 0 && end >= start {
			unit := m[3]
			if unit == "" || unit == "节" || unit == "回" {
				unit = "章"
			}
			n.Ranges = append(n.Ranges, Range{start, end, unit})
		}
	}
	for _, m := range progressRE.FindAllStringSubmatch(rangeRE.ReplaceAllString(metadataSource, " "), -1) {
		if end := number(m[1]); end >= 1 {
			unit := m[2]
			if unit == "" || unit == "节" || unit == "回" {
				unit = "章"
			}
			n.Ranges = append(n.Ranges, Range{1, end, unit})
		}
	}
	if len(n.Ranges) > 1 {
		n.Notes = append(n.Notes, "复合章节范围，需核对")
	}
	s = exportRE.ReplaceAllString(s, "《")
	if title := titleRE.FindStringSubmatch(strings.TrimSpace(s)); title != nil {
		s = title[1]
	}
	s = rangeRE.ReplaceAllString(s, " ")
	s = progressRE.ReplaceAllString(s, " ")
	// Process inner blocks first, retaining unknown parenthetical subtitles as
	// alias candidates rather than blindly dropping every bracketed segment.
	var aliases []string
	for {
		before := s
		s = blockRE.ReplaceAllStringFunc(s, func(block string) string {
			body := []rune(block)
			inner := strings.TrimSpace(string(body[1 : len(body)-1]))
			if copyRE.MatchString(inner) {
				n.Notes = appendUnique(n.Notes, "括号数字可能为副本序号或分册")
				return " "
			}
			if metadata(inner) || slices.Contains(n.Parts, inner) {
				return " "
			}
			alias := Key(strings.TrimPrefix(strings.TrimPrefix(inner, "原名"), "又名"))
			if utf8.RuneCountInString(alias) >= 3 {
				aliases = appendUnique(aliases, alias)
			}
			return " "
		})
		if s == before {
			break
		}
	}
	// Edition markers remain evidence even when omitted from the core title.
	s = versionRE.ReplaceAllString(s, " ")
	s = strings.Trim(s, " @-_.[]【】()《》 ")
	// Whole-book labels are packaging only at the end, not inside a title.
	for {
		old := s
		for _, label := range []string{"精校版", "校对版", "未删节", "未删减", "全本", "全集", "完本", "完结", "全书完"} {
			s = strings.TrimSpace(strings.TrimSuffix(s, label))
		}
		if old == s {
			break
		}
	}
	for _, sep := range []string{"又名", "原名"} {
		if parts := strings.SplitN(s, sep, 2); len(parts) == 2 {
			aliases = appendUnique(aliases, Key(parts[1]))
			s = parts[0]
		}
	}
	n.Title = Key(s)
	if n.Title == "" && len(aliases) > 0 {
		n.Title, aliases = aliases[0], aliases[1:]
	}
	if useful(n.Title) {
		n.Keys = append(n.Keys, n.Title)
	}
	for _, alias := range aliases {
		if useful(alias) {
			n.Keys = appendUnique(n.Keys, alias)
		}
	}
	slices.Sort(n.Versions)
	return n
}

func regexpHostBoundary(s string) string {
	// Only hosts observed as packaging are special-cased; arbitrary hyphenated
	// domains still use the domain parser above.
	for _, host := range []string{"soushu", "www."} {
		s = strings.ReplaceAll(s, "-"+host, " "+host)
	}
	return s
}

func metadata(s string) bool {
	s = statusRE.ReplaceAllString(s, "")
	s = versionRE.ReplaceAllString(s, "")
	s = rangeRE.ReplaceAllString(s, "")
	s = progressRE.ReplaceAllString(s, "")
	return Key(s) == ""
}

func Key(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKC.String(strings.ToLower(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func useful(s string) bool {
	if utf8.RuneCountInString(s) < 2 {
		return false
	}
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	if value != "" && !slices.Contains(values, value) {
		return append(values, value)
	}
	return values
}

func number(s string) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	total, digit := 0, 0
	for _, r := range s {
		switch r {
		case '零', '〇':
			digit = 0
		case '一':
			digit = 1
		case '二', '两':
			digit = 2
		case '三':
			digit = 3
		case '四':
			digit = 4
		case '五':
			digit = 5
		case '六':
			digit = 6
		case '七':
			digit = 7
		case '八':
			digit = 8
		case '九':
			digit = 9
		case '十', '百', '千':
			unit := map[rune]int{'十': 10, '百': 100, '千': 1000}[r]
			if digit == 0 {
				digit = 1
			}
			total += digit * unit
			digit = 0
		default:
			return -1
		}
	}
	return total + digit
}

// Match returns a title score, never a probability of duplicate content.
func Match(a, b Name) float64 {
	for _, left := range a.Keys {
		for _, right := range b.Keys {
			if left == right {
				return 1
			}
		}
	}
	left, right := []rune(a.Title), []rune(b.Title)
	if len(left) < 6 || len(right) < 6 || abs(len(left)-len(right)) > 2 {
		return 0
	}
	limit := 1
	if min(len(left), len(right)) >= 12 {
		limit = 2
	}
	if distance(left, right, limit) <= limit {
		return 0.9
	}
	return 0
}

// Differences are retained even when titles match. A missing field is not
// agreement; it simply cannot establish a conflict.
func Differences(a, b Name) []string {
	var out []string
	if len(a.Authors) > 0 && len(b.Authors) > 0 && !intersects(a.Authors, b.Authors) {
		out = append(out, "作者标注不同")
	}
	if strings.Join(a.Versions, "|") != strings.Join(b.Versions, "|") && (len(a.Versions) > 0 || len(b.Versions) > 0) {
		out = append(out, "版本标注不同或未标注")
	}
	if len(a.Parts) > 0 && len(b.Parts) > 0 && !intersects(a.Parts, b.Parts) {
		out = append(out, "分册不同，可能互补")
	}
	if len(a.Ranges) == 1 && len(b.Ranges) == 1 {
		x, y := a.Ranges[0], b.Ranges[0]
		if x.Unit != y.Unit {
			out = append(out, "章节单位不同")
		} else if x.End < y.Start || y.End < x.Start {
			out = append(out, "章节范围不相交，可能互补")
		} else if x != y {
			out = append(out, "章节范围不同，需结合正文核对")
		}
	}
	return out
}

func (n Name) Reasons() []string {
	var out []string
	if n.Title != "" {
		out = append(out, "解析书名："+n.Title)
	}
	if len(n.Authors) > 0 {
		out = append(out, "作者标注："+strings.Join(n.Authors, " / "))
	}
	for _, r := range n.Ranges {
		out = append(out, fmt.Sprintf("章节标注：%d–%d%s", r.Start, r.End, r.Unit))
	}
	if len(n.Parts) > 0 {
		out = append(out, "分册标注："+strings.Join(n.Parts, " / "))
	}
	if len(n.Versions) > 0 {
		out = append(out, "版本标注："+strings.Join(n.Versions, " / "))
	}
	return append(out, n.Notes...)
}

func intersects(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func distance(a, b []rune, limit int) int {
	row := make([]int, len(b)+1)
	for i := range row {
		row[i] = i
	}
	for i, x := range a {
		prev := row[0]
		row[0] = i + 1
		best := row[0]
		for j, y := range b {
			old := row[j+1]
			cost := 0
			if x != y {
				cost = 1
			}
			row[j+1] = min(row[j]+1, old+1, prev+cost)
			prev = old
			best = min(best, row[j+1])
		}
		if best > limit {
			return limit + 1
		}
	}
	return row[len(b)]
}
