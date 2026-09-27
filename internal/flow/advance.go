package flow

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// StartsForwardChapter xác định một chỉ thị có bắt đầu chương mới hướng đi
// chính chưa hoàn thành hay không. Nó chỉ đọc sự kiện, không quyết định có cho
// phép hay không; nội dung Task/Reason không tham gia phán đoán.
func StartsForwardChapter(inst *Instruction, progress *domain.Progress, pending *domain.PendingCommit) bool {
	if inst == nil || inst.Agent != "writer" || progress == nil || progress.Phase != domain.PhaseWriting {
		return false
	}
	if pending != nil || len(progress.PendingRewrites) > 0 || progress.InProgressChapter > 0 {
		return false
	}
	target := inst.Chapter
	if target == 0 {
		target = progress.NextChapter()
	}
	return target > 0 && target == progress.NextChapter()
}

// AdvanceHoldResolution là kết quả xử lý lệnh tạm dừng một lần dưới các sự
// kiện hiện tại.
type AdvanceHoldResolution int

const (
	AdvanceHoldKeep AdvanceHoldResolution = iota
	AdvanceHoldConsume
	AdvanceHoldConsumeAndStop
)

// ResolveAdvanceHold là hàm thuần phân giải lệnh tạm dừng một lần. Điều kiện
// lạ và sự kiện thiếu đều báo lỗi tường minh, không được âm thầm hạ cấp theo
// "tiếp tục chạy".
func ResolveAdvanceHold(hold *domain.AdvanceHold, progress *domain.Progress) (AdvanceHoldResolution, error) {
	if hold == nil {
		return AdvanceHoldKeep, nil
	}
	if err := hold.Validate(); err != nil {
		return AdvanceHoldKeep, err
	}
	if progress == nil {
		return AdvanceHoldKeep, fmt.Errorf("thiếu Progress, không thể phân giải lệnh tạm dừng một lần")
	}
	if progress.Phase == domain.PhaseComplete {
		return AdvanceHoldConsume, nil
	}
	if progress.Phase != domain.PhaseWriting {
		return AdvanceHoldKeep, fmt.Errorf("lệnh tạm dừng một lần chỉ áp dụng cho giai đoạn writing/complete (hiện tại %s)", progress.Phase)
	}
	switch hold.After {
	case domain.AdvanceHoldAtBoundary:
		return AdvanceHoldConsumeAndStop, nil
	case domain.AdvanceHoldAfterRewritesDrained:
		if len(progress.PendingRewrites) > 0 {
			return AdvanceHoldKeep, nil
		}
		return AdvanceHoldConsumeAndStop, nil
	case domain.AdvanceHoldAtChapter:
		if progress.LatestCompleted() < hold.TargetChapter {
			return AdvanceHoldKeep, nil
		}
		return AdvanceHoldConsumeAndStop, nil
	default:
		return AdvanceHoldKeep, fmt.Errorf("không hỗ trợ điều kiện tạm dừng một lần %q", hold.After)
	}
}
