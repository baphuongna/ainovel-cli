package diag

import (
	"fmt"
	"sort"

	"github.com/voocel/ainovel-cli/internal/store"
)

// ── Ngưỡng chẩn đoán ─────────────────────────────────────

const (
	ThresholdDimScoreLow      = 70  // ChronicLowDimension: cảnh báo khi điểm trung bình chiều thấp hơn giá trị này
	ThresholdContractMissRate = 0.3 // ContractMissPattern: giới hạn trên tỷ lệ hợp đồng không đạt
	ThresholdRewriteRate      = 0.5 // ExcessiveRewrites: giới hạn trên tỷ lệ viết lại
	ThresholdWordShortRatio   = 0.4 // WordCountAnomaly: số chữ dưới trung bình theo tỷ lệ này coi là bất thường
	ThresholdWordLongRatio    = 2.5 // WordCountAnomaly: số chữ trên trung bình theo tỷ lệ này coi là bất thường
	ThresholdHookWeakScore    = 75  // HookWeakChain: hook dưới điểm này coi là yếu
	ThresholdHookWeakChain    = 3   // HookWeakChain: ngưỡng số chương yếu liên tiếp
	ThresholdPayoffMissRate   = 0.4 // PayoffMissPattern: giới hạn trên tỷ lệ payoff chưa hoàn hiện
	ThresholdCompassDrift     = 15  // CompassDrift: giới hạn trên số chương la bàn chưa cập nhật
	ThresholdTimelineGapRate  = 0.3 // TimelineGaps: giới hạn trên dung sai tỷ lệ thiếu
	ThresholdForeshadowMin    = 8   // StaleForeshadow: số chương đình trệ phục bút tối thiểu
)

// allRules xếp theo flow → quality → planning → context.
var allRules = []RuleFunc{
	// Flow
	InvalidPendingRewrites,
	RewritePendingPressure,
	OrphanedSteer,
	PhaseFlowMismatch,
	ChapterGaps,
	// Quality
	ChronicLowDimension,
	ContractMissPattern,
	HookWeakChain,
	PayoffMissPattern,
	ExcessiveRewrites,
	WordCountAnomaly,
	// Planning
	StaleForeshadow,
	CompassDrift,
	OutlineExhausted,
	MissingSummaries,
	// Context
	GhostCharacter,
	TimelineGaps,
	RelationshipStagnation,
}

// Analyze là cổng vào duy nhất của hệ chẩn đoán.
func Analyze(s *store.Store) Report {
	snap := Load(s)

	var findings []Finding
	for _, e := range snap.LoadErrors {
		findings = append(findings, Finding{
			Rule:       "LoadError",
			Category:   CatFlow,
			Severity:   SevWarning,
			Confidence: ConfHigh,
			AutoLevel:  AutoNone,
			Target:     "runtime.flow",
			Title:      fmt.Sprintf("Tải artifact thất bại: %s", e),
			Suggestion: "Tệp có thể hỏng hoặc thiếu quyền, kết quả của các quy tắc chẩn đoán liên quan có thể không đầy đủ.",
		})
	}
	for _, rule := range allRules {
		findings = append(findings, rule(&snap)...)
	}
	sortFindings(findings)

	return Report{
		Stats:    buildStats(&snap),
		Findings: findings,
		Actions:  PlanActions(findings),
	}
}

func buildStats(snap *Snapshot) Stats {
	st := Stats{}
	if snap.Progress == nil {
		return st
	}
	p := snap.Progress
	st.CompletedChapters = len(p.CompletedChapters)
	st.TotalChapters = p.TotalChapters
	st.TotalWords = p.TotalWordCount
	st.Phase = string(p.Phase)
	st.Flow = string(p.Flow)

	if st.CompletedChapters > 0 {
		st.AvgWordsPerCh = st.TotalWords / st.CompletedChapters
	}

	if snap.RunMeta != nil {
		st.PlanningTier = string(snap.RunMeta.PlanningTier)
	}

	// Thống kê xem xét
	st.ReviewCount = len(snap.Reviews)
	var totalScore float64
	var dimCount int
	for _, r := range snap.Reviews {
		if r.Verdict == "rewrite" {
			st.RewriteCount++
		}
		for _, d := range r.Dimensions {
			totalScore += float64(d.Score)
			dimCount++
		}
	}
	if dimCount > 0 {
		st.AvgReviewScore = totalScore / float64(dimCount)
	}

	// Thống kê phục bút
	latest := snap.LatestCompleted()
	for _, f := range snap.Foreshadow {
		if f.Status == "planted" || f.Status == "advanced" {
			st.ForeshadowOpen++
			if f.Status == "planted" && latest-f.PlantedAt > staleForeshadowThreshold(st.CompletedChapters) {
				st.ForeshadowStale++
			}
		}
	}
	return st
}

// sortFindings sắp theo mức nghiêm trọng: critical > warning > info.
func sortFindings(findings []Finding) {
	order := map[Severity]int{SevCritical: 0, SevWarning: 1, SevInfo: 2}
	sort.SliceStable(findings, func(i, j int) bool {
		return order[findings[i].Severity] < order[findings[j].Severity]
	})
}

// staleForeshadowThreshold tính ngưỡng đình trệ phục bút theo tổng số chương.
func staleForeshadowThreshold(completedChapters int) int {
	t := completedChapters / 3
	if t < ThresholdForeshadowMin {
		return ThresholdForeshadowMin
	}
	return t
}
