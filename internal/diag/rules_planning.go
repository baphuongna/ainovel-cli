package diag

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// StaleForeshadow phát hiện phục bút lâu không được đẩy tiến.
func StaleForeshadow(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Foreshadow) == 0 {
		return nil
	}
	latest := snap.LatestCompleted()
	threshold := staleForeshadowThreshold(snap.CompletedCount())

	var stale []string
	for _, f := range snap.Foreshadow {
		if f.Status != "planted" {
			continue
		}
		gap := latest - f.PlantedAt
		if gap > threshold {
			stale = append(stale, fmt.Sprintf("%s(gieo ở ch%d, đã qua %d chương)", f.ID, f.PlantedAt, gap))
		}
	}
	if len(stale) == 0 {
		return nil
	}
	return []Finding{{
		Rule:       "StaleForeshadow",
		Category:   CatPlanning,
		Severity:   SevWarning,
		Confidence: ConfMedium,
		AutoLevel:  AutoNone,
		Target:     "context.foreshadow",
		Title:      fmt.Sprintf("Phục bút đình trệ: %d mục quá %d chương chưa đẩy tiến", len(stale), threshold),
		Evidence:   strings.Join(stale, "; "),
		Suggestion: "Việc nạp nhắc nhở phục bút của novel_context có thể chưa có hiệu lực, hoặc prompt Writer thiếu chỉ dẫn đẩy tiến phục bút. Kiểm tra foreshadow_ledger và logic bơm ngữ cảnh.",
	}}
}

// CompassDrift phát hiện la bàn lâu không được cập nhật.
func CompassDrift(snap *Snapshot) []Finding {
	if snap.Progress == nil || !snap.Progress.Layered {
		return nil
	}
	if snap.Compass == nil {
		if snap.CompletedCount() > 5 {
			return []Finding{{
				Rule:       "CompassDrift",
				Category:   CatPlanning,
				Severity:   SevWarning,
				Confidence: ConfMedium,
				AutoLevel:  AutoNone,
				Target:     "prompt.architect",
				Title:      "Chế độ trường thiên thiếu la bàn",
				Evidence:   fmt.Sprintf("layered=true, completed=%d, compass=nil", snap.CompletedCount()),
				Suggestion: "Architect cần tạo compass lúc lập kế hoạch ban đầu. Kiểm tra architect-long.md có chứa chỉ dẫn tạo compass không.",
			}}
		}
		return nil
	}

	gap := snap.LatestCompleted() - snap.Compass.LastUpdated
	if gap <= ThresholdCompassDrift {
		return nil
	}
	return []Finding{{
		Rule:       "CompassDrift",
		Category:   CatPlanning,
		Severity:   SevInfo,
		Confidence: ConfLow,
		AutoLevel:  AutoNone,
		Target:     "prompt.architect",
		Title:      fmt.Sprintf("La bàn đã %d chương chưa cập nhật", gap),
		Evidence:   fmt.Sprintf("last_updated=ch%d, latest=ch%d, open_threads=%d", snap.Compass.LastUpdated, snap.LatestCompleted(), len(snap.Compass.OpenThreads)),
		Suggestion: "Architect cần cập nhật compass ở ranh giới cung/tập. Kiểm tra architect-long.md có chứa chỉ dẫn cập nhật compass không.",
	}}
}

// OutlineExhausted phát hiện dàn ý cạn kiệt nhưng tiểu thuyết chưa hoàn tất.
func OutlineExhausted(snap *Snapshot) []Finding {
	if snap.Progress == nil {
		return nil
	}
	p := snap.Progress
	if p.Phase == domain.PhaseComplete || p.Phase == domain.PhaseInit {
		return nil
	}

	completed := snap.CompletedCount()
	if completed == 0 {
		return nil
	}

	outlinedCount := p.TotalChapters
	if outlinedCount <= 0 {
		outlinedCount = len(snap.Outline)
	}
	if outlinedCount <= 0 {
		return nil
	}

	if completed < outlinedCount {
		return nil
	}

	return []Finding{{
		Rule:       "OutlineExhausted",
		Category:   CatPlanning,
		Severity:   SevCritical,
		Confidence: ConfHigh,
		AutoLevel:  AutoSafe,
		Target:     "runtime.recovery",
		Title:      fmt.Sprintf("Dàn ý cạn kiệt: đã hoàn thành %d chương >= đã quy hoạch %d chương", completed, outlinedCount),
		Evidence:   fmt.Sprintf("phase=%s, completed=%d, outlined=%d", p.Phase, completed, outlinedCount),
		Suggestion: "Tín hiệu triển khai/tập mới có thể chưa được kích hoạt. Kiểm tra chiến lược nộp và logic khôi phục phía host, xác nhận phát hiện ranh giới cung, expand_arc hay append_volume có chạy bình thường không.",
	}}
}

// MissingSummaries phát hiện chương đã hoàn thành thiếu tóm tắt.
func MissingSummaries(snap *Snapshot) []Finding {
	if snap.Progress == nil || len(snap.Progress.CompletedChapters) == 0 {
		return nil
	}

	var missing []int
	for _, ch := range snap.Progress.CompletedChapters {
		if _, ok := snap.Summaries[ch]; !ok {
			missing = append(missing, ch)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []Finding{{
		Rule:       "MissingSummaries",
		Category:   CatPlanning,
		Severity:   SevWarning,
		Confidence: ConfHigh,
		AutoLevel:  AutoNone,
		Target:     "runtime.flow",
		Title:      fmt.Sprintf("Thiếu tóm tắt: %d chương không có tóm tắt", len(missing)),
		Evidence:   fmt.Sprintf("missing=[%s]", intsToStr(missing)),
		Suggestion: "Tóm tắt là chìa khóa của tính liên tục ngữ cảnh. Kiểm tra logic ghi tóm tắt của commit_chapter có hoạt động bình thường không.",
	}}
}
