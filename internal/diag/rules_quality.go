package diag

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// ChronicLowDimension phát hiện một chiều xem xét liên tục điểm thấp qua nhiều chương.
func ChronicLowDimension(snap *Snapshot) []Finding {
	if len(snap.Reviews) < 2 {
		return nil
	}

	dimSums := make(map[string]float64)
	dimCounts := make(map[string]int)
	for _, r := range snap.Reviews {
		for _, d := range r.Dimensions {
			dimSums[d.Dimension] += float64(d.Score)
			dimCounts[d.Dimension]++
		}
	}

	var findings []Finding
	for name, sum := range dimSums {
		count := dimCounts[name]
		if count < 2 {
			continue
		}
		avg := sum / float64(count)
		if avg >= ThresholdDimScoreLow {
			continue
		}
		findings = append(findings, Finding{
			Rule:       "ChronicLowDimension",
			Category:   CatQuality,
			Severity:   SevWarning,
			Confidence: ConfMedium,
			AutoLevel:  AutoNone,
			Target:     "prompt.writer",
			Title:      fmt.Sprintf("Chiều [%s] liên tục điểm thấp (trung bình %.0f)", name, avg),
			Evidence:   fmt.Sprintf("Tổng %d lần xem xét, điểm trung bình %.1f", count, avg),
			Suggestion: fmt.Sprintf("Kiểm tra hướng dẫn về %s trong prompt Writer có rõ ràng không, hoặc chuẩn chấm điểm %s trong prompt Editor có hợp lý không.", name, name),
		})
	}
	return findings
}

// ContractMissPattern phát hiện tỷ lệ tuân thủ hợp đồng quá thấp.
func ContractMissPattern(snap *Snapshot) []Finding {
	if len(snap.Reviews) == 0 {
		return nil
	}

	var total, missed int
	var missedChapters []string
	for ch, r := range snap.Reviews {
		total++
		if r.ContractStatus == "partial" || r.ContractStatus == "missed" {
			missed++
			missedChapters = append(missedChapters, fmt.Sprintf("ch%d", ch))
		}
	}
	if total == 0 {
		return nil
	}
	rate := float64(missed) / float64(total)
	if rate <= ThresholdContractMissRate {
		return nil
	}
	return []Finding{{
		Rule:       "ContractMissPattern",
		Category:   CatQuality,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "prompt.writer",
		Title:      fmt.Sprintf("Tỷ lệ tuân thủ hợp đồng thấp (%.0f%% không đạt)", rate*100),
		Evidence:   fmt.Sprintf("Không đạt: [%s], tổng %d/%d", strings.Join(missedChapters, ", "), missed, total),
		Suggestion: "Writer có thể chưa đọc contract, hoặc required_beats của contract quá hăng. Kiểm tra sự phối hợp giữa plan_chapter và writer.md.",
	}}
}

// HookWeakChain phát hiện điểm hook của chương liên tiếp yếu.
func HookWeakChain(snap *Snapshot) []Finding {
	if len(snap.Reviews) < ThresholdHookWeakChain {
		return nil
	}

	chapters := sortedChapterReviews(snap)
	var weakChain []int
	for _, ch := range chapters {
		review := snap.Reviews[ch]
		if review == nil || review.Scope != "chapter" {
			continue
		}
		hook := review.Dimension("hook")
		if hook == nil || hook.Score >= ThresholdHookWeakScore {
			if len(weakChain) >= ThresholdHookWeakChain {
				break
			}
			weakChain = weakChain[:0]
			continue
		}
		weakChain = append(weakChain, ch)
	}
	if len(weakChain) < ThresholdHookWeakChain {
		return nil
	}

	var parts []string
	for _, ch := range weakChain {
		if hook := snap.Reviews[ch].Dimension("hook"); hook != nil {
			parts = append(parts, fmt.Sprintf("ch%d(%d)", ch, hook.Score))
		}
	}
	return []Finding{{
		Rule:       "HookWeakChain",
		Category:   CatQuality,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "prompt.writer",
		Title:      fmt.Sprintf("Hook cuối chương liên tiếp yếu (liên tục %d chương)", len(weakChain)),
		Evidence:   strings.Join(parts, ", "),
		Suggestion: "Kiểm tra việc thực thi hook_goal trong writer.md có rõ ràng không, khi cần nêu rõ ham muốn đọc tiếp của chương trong plan_chapter, và hiệu chỉnh chuẩn chứng minh của Editor với hook.",
	}}
}

// PayoffMissPattern phát hiện các chương có payoff_points lâu không được hoàn hiện.
func PayoffMissPattern(snap *Snapshot) []Finding {
	var total, missed int
	var details []string
	for ch, plan := range snap.Plans {
		if plan == nil || len(plan.Contract.PayoffPoints) == 0 {
			continue
		}
		review := snap.Reviews[ch]
		if review == nil {
			continue
		}
		total++
		if review.ContractStatus == "partial" || review.ContractStatus == "missed" {
			missed++
			details = append(details, fmt.Sprintf("ch%d(%d điểm payoff)", ch, len(plan.Contract.PayoffPoints)))
		}
	}
	if total < 2 {
		return nil
	}
	rate := float64(missed) / float64(total)
	if rate <= ThresholdPayoffMissRate {
		return nil
	}
	sort.Strings(details)
	return []Finding{{
		Rule:       "PayoffMissPattern",
		Category:   CatQuality,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "prompt.writer",
		Title:      fmt.Sprintf("Tỷ lệ hoàn hiện điểm thỏa mãn/điểm cốt truyện thấp (%.0f%% không đạt)", rate*100),
		Evidence:   fmt.Sprintf("Chương chưa hoàn hiện: [%s], tổng %d/%d", strings.Join(details, ", "), missed, total),
		Suggestion: "Kiểm tra payoff_points trong plan_chapter có quá nhiều hoặc quá rỗng không, bảo đảm Writer hoàn hiện rõ ràng trong chính văn, thay vì chỉ dọn đường.",
	}}
}

// ExcessiveRewrites phát hiện tỷ lệ viết lại quá cao.
func ExcessiveRewrites(snap *Snapshot) []Finding {
	if len(snap.Reviews) < 2 {
		return nil
	}

	var total, rewrites int
	for _, r := range snap.Reviews {
		total++
		if r.Verdict == "rewrite" {
			rewrites++
		}
	}
	if total == 0 {
		return nil
	}
	rate := float64(rewrites) / float64(total)
	if rate <= ThresholdRewriteRate {
		return nil
	}
	return []Finding{{
		Rule:       "ExcessiveRewrites",
		Category:   CatQuality,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "prompt.editor",
		Title:      fmt.Sprintf("Tỷ lệ viết lại quá cao (%d/%d = %.0f%%)", rewrites, total, rate*100),
		Evidence:   fmt.Sprintf("Tổng %d lần xem xét, %d lần rewrite", total, rewrites),
		Suggestion: "Writer liên tục tạo nội dung dưới ngưỡng của Editor. Kiểm tra chuẩn chất lượng trong prompt Writer có thẳng hàng với chuẩn xem xét của Editor hay không.",
	}}
}

// WordCountAnomaly phát hiện số chữ của chương bất thường.
func WordCountAnomaly(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Progress.ChapterWordCounts) < 3 {
		return nil
	}
	wc := snap.Progress.ChapterWordCounts

	var sum float64
	for _, w := range wc {
		sum += float64(w)
	}
	avg := sum / float64(len(wc))
	if avg == 0 {
		return nil
	}

	var anomalies []string
	for ch, w := range wc {
		ratio := float64(w) / avg
		if ratio < ThresholdWordShortRatio {
			anomalies = append(anomalies, fmt.Sprintf("ch%d(%d chữ,%.0f%%)", ch, w, ratio*100))
		} else if ratio > ThresholdWordLongRatio {
			anomalies = append(anomalies, fmt.Sprintf("ch%d(%d chữ,%.0f%%)", ch, w, ratio*100))
		}
	}
	if len(anomalies) == 0 {
		return nil
	}
	return []Finding{{
		Rule:       "WordCountAnomaly",
		Category:   CatQuality,
		Severity:   SevInfo,
		Confidence: ConfLow,
		AutoLevel:  AutoNone,
		Target:     "context.window",
		Title:      fmt.Sprintf("Số chữ chương bất thường (trung bình %d chữ)", int(math.Round(avg))),
		Evidence:   strings.Join(anomalies, "; "),
		Suggestion: "Chương cực ngắn có thể do đầu ra bị cắt cụt (giới hạn token), chương cực dài có thể ngốn quá nhiều cửa sổ ngữ cảnh. Kiểm tra cấu hình max_tokens của mô hình.",
	}}
}

func sortedChapterReviews(snap *Snapshot) []int {
	chapters := make([]int, 0, len(snap.Reviews))
	for ch := range snap.Reviews {
		chapters = append(chapters, ch)
	}
	sort.Ints(chapters)
	return chapters
}
