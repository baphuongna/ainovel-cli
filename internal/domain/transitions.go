package domain

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/errs"
)

// Quy tắc chuyển trạng thái (bản tối thiểu)
//
// Phase biểu thị giai đoạn lớn, áp ràng buộc "chỉ tiến không lùi":
//
//	init -> premise -> outline -> writing -> complete
//	  \---------> outline ------^
//	  \-----------------> writing
//
// Flow biểu thị luồng đang hoạt động, cho phép chuyển trong kỳ viết nhưng không cho phép
// những cú nhảy rõ ràng phản thường:
//
//	writing   -> reviewing / rewriting / polishing / steering / writing
//	reviewing -> writing / rewriting / polishing / steering / reviewing
//	rewriting -> writing / steering / rewriting
//	polishing -> writing / steering / polishing
//	steering  -> writing / reviewing / rewriting / polishing / steering
//
// Trạng thái rỗng (giá trị 0) được coi là "chưa khởi tạo", cho phép chuyển sang bất kỳ trạng
// thái hợp lệ nào khác rỗng.

var phaseOrder = map[Phase]int{
	PhaseInit:     1,
	PhasePremise:  2,
	PhaseOutline:  3,
	PhaseWriting:  4,
	PhaseComplete: 5,
}

// CanTransitionPhase xác định Phase có được phép chuyển không.
// Quy tắc giữ đơn giản: cho phép chuyển đồng trạng, cho phép tiến, không cho phép lùi.
func CanTransitionPhase(from, to Phase) bool {
	if to == "" {
		return false
	}
	if from == "" || from == to {
		return true
	}
	fromOrder, fromOK := phaseOrder[from]
	toOrder, toOK := phaseOrder[to]
	if !fromOK || !toOK {
		return false
	}
	return toOrder >= fromOrder
}

// ValidatePhaseTransition kiểm tra chuyển Phase có hợp pháp không.
func ValidatePhaseTransition(from, to Phase) error {
	if CanTransitionPhase(from, to) {
		return nil
	}
	return fmt.Errorf("invalid phase transition: %q -> %q: %w", from, to, errs.ErrPhaseTransition)
}

// CanTransitionFlow xác định FlowState có được phép chuyển không.
func CanTransitionFlow(from, to FlowState) bool {
	if to == "" {
		return false
	}
	if from == "" || from == to {
		return true
	}

	switch from {
	case FlowWriting:
		return to == FlowReviewing || to == FlowRewriting || to == FlowPolishing || to == FlowSteering
	case FlowReviewing:
		return to == FlowWriting || to == FlowRewriting || to == FlowPolishing || to == FlowSteering
	case FlowRewriting:
		return to == FlowWriting || to == FlowSteering
	case FlowPolishing:
		return to == FlowWriting || to == FlowSteering
	case FlowSteering:
		return to == FlowWriting || to == FlowReviewing || to == FlowRewriting || to == FlowPolishing
	default:
		return false
	}
}

// ValidateFlowTransition kiểm tra chuyển FlowState có hợp pháp không.
func ValidateFlowTransition(from, to FlowState) error {
	if CanTransitionFlow(from, to) {
		return nil
	}
	return fmt.Errorf("invalid flow transition: %q -> %q: %w", from, to, errs.ErrFlowTransition)
}
