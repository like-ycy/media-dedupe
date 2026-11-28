package textdedupe

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"media-dedupe/internal/model"
)

type verificationText struct {
	content string
	compact string
}

type VerificationCache interface {
	LoadTextVerification(leftHash, rightHash string) (model.TextVerification, bool, error)
	SaveTextVerification(leftHash, rightHash string, result model.TextVerification) error
}

// VerifyGroups adds full-text evidence to candidate groups. It leaves actions
// alone: containment and whitespace changes are reviewable version relations.
// Content is checked against the scan fingerprint before it supplies evidence.
func VerifyGroups(ctx context.Context, groups []model.ReportGroup, facts []Fact, cache VerificationCache, progress ProgressFunc) []FileError {
	byID := make(map[int64]Fact, len(facts))
	for _, f := range facts {
		byID[f.FileID] = f
	}
	total := 0
	for _, g := range groups {
		if g.GroupType.IsTextAnalysis() {
			total += verifyPairCount(len(g.Items))
		}
	}
	done := 0
	var errs []FileError
	cacheFailures := 0
	for gi := range groups {
		g := &groups[gi]
		if !g.GroupType.IsTextAnalysis() {
			continue
		}
		verified := 0
		// Bound retained text independently of group size. Large entries are
		// used only for the current pair; no whole-library text cache is built.
		const textBudget = 64 << 20
		retained := 0
		loaded := map[int64]*verificationText{}
		failed := map[int64]bool{}
		load := func(id int64) *verificationText {
			f := byID[id]
			if f.Digest == "" || f.Length < 200 || failed[id] || ctx.Err() != nil {
				return nil
			}
			if text := loaded[id]; text != nil {
				return text
			}
			content, err := readText(f.Path)
			if err == nil && textDigest(content) != f.Digest {
				err = fmt.Errorf("文件在扫描后发生变化，请重新扫描")
			}
			if err != nil {
				failed[id] = true
				errs = append(errs, FileError{f.Path, "text_verify", err})
				return nil
			}
			text := &verificationText{content: content, compact: strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, content)}
			size := len(text.content) + len(text.compact)
			if retained+size <= textBudget {
				loaded[id] = text
				retained += size
			}
			return text
		}
		// Every pair is a cache lookup plus two file reads. Hundreds of editions of
		// one book would mean tens of thousands of them, so the group is verified up
		// to a bound and every member says it was not fully covered.
	pairLoop:
		for i := range g.Items {
			for j := i + 1; j < len(g.Items); j++ {
				if verified >= maxVerifyPairsPerGroup {
					break pairLoop
				}
				if ctx.Err() != nil {
					return errs
				}
				a, b := &g.Items[i], &g.Items[j]
				fa, fb := byID[a.FileID], byID[b.FileID]
				if fa.Digest != "" && fb.Digest != "" && fa.Length >= 200 && fb.Length >= 200 {
					if fa.Digest > fb.Digest {
						a, b = b, a
						fa, fb = fb, fa
					}
					var result model.TextVerification
					var cached bool
					// A broken verification cache costs speed, not correctness: the pair
					// is simply compared again. Record the first failure and keep going
					// without the cache instead of dropping the rest of the scan's
					// evidence on the floor.
					if cache != nil && cacheFailures == 0 {
						var err error
						result, cached, err = cache.LoadTextVerification(fa.Digest, fb.Digest)
						if err != nil {
							cacheFailures++
							errs = append(errs, FileError{a.Path, "text_cache", err})
							cached = false
						}
					}
					if !cached {
						left, right := load(a.FileID), load(b.FileID)
						if ctx.Err() != nil {
							return errs
						}
						if left != nil && right != nil {
							result = verifyTextPair(left, right)
							if cache != nil && cacheFailures == 0 {
								if err := cache.SaveTextVerification(fa.Digest, fb.Digest, result); err != nil {
									cacheFailures++
									errs = append(errs, FileError{a.Path, "text_cache", err})
								}
							}
						}
					}
					appendVerification(a, b, result)
				}
				verified++
				done++
				if progress != nil {
					progress(done, total, g.Items[j].Path)
				}
			}
		}
		if verified < len(g.Items)*(len(g.Items)-1)/2 {
			note := fmt.Sprintf("组内共 %d 个成员，仅复核了前 %d 对全文，其余组合未复核，判断前请抽查", len(g.Items), verified)
			for i := range g.Items {
				g.Items[i].Reasons = append(g.Items[i].Reasons, note)
			}
		}
	}
	return errs
}

// A group of editions can be arbitrarily large; past this many pairs the scan
// reports "not fully verified" instead of spending minutes per group.
const maxVerifyPairsPerGroup = 400

func verifyPairCount(members int) int {
	pairs := members * (members - 1) / 2
	if pairs > maxVerifyPairsPerGroup {
		return maxVerifyPairsPerGroup
	}
	return pairs
}

func verifyTextPair(left, right *verificationText) model.TextVerification {
	for _, compact := range []bool{false, true} {
		shorterIsLeft := true
		shortText, longText := left.content, right.content
		if compact {
			shortText, longText = left.compact, right.compact
		}
		if len(shortText) > len(longText) {
			shorterIsLeft = false
			shortText, longText = longText, shortText
		}
		if shortText == "" {
			continue
		}
		offset := strings.Index(longText, shortText)
		if offset < 0 {
			continue
		}
		start := utf8.RuneCountInString(longText[:offset]) + 1
		count := utf8.RuneCountInString(shortText)
		extra := utf8.RuneCountInString(longText) - count
		return model.TextVerification{Matched: true, ShorterIsLeft: shorterIsLeft, WhitespaceIgnored: compact, StartChar: start, CharacterCount: count, ExtraCharacters: extra}
	}
	return model.TextVerification{}
}

func appendVerification(a, b *model.ReportItem, result model.TextVerification) {
	if !result.Matched {
		return
	}
	shorter, longer := a, b
	if !result.ShorterIsLeft {
		shorter, longer = b, a
	}
	unit := "统一换行后的文本"
	qualification := "全文连续包含，保留空白与标点"
	if result.WhitespaceIgnored {
		unit = "去空白后的文本"
		qualification = "忽略空白后全文连续包含，需核对排版差异"
	}
	start, count, extra := result.StartChar, result.CharacterCount, result.ExtraCharacters
	shorter.Reasons = append(shorter.Reasons, fmt.Sprintf("与「%s」：全文复核确认%s；本文件 %d 字符对应对方%s第 %d–%d 字符，对方另有 %d 字符", longer.Path, qualification, count, unit, start, start+count-1, extra))
	longer.Reasons = append(longer.Reasons, fmt.Sprintf("与「%s」：全文复核确认%s；对方全文位于本文件%s第 %d–%d 字符，本文件另有 %d 字符", shorter.Path, qualification, unit, start, start+count-1, extra))
}
