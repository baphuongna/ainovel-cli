package tools

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/store"
)

// requireAggregateTarget ràng buộc bản ghi tổng hợp mới của Editor vào artefact duy nhất mà Router hiện còn chờ bổ sung.
// Đích được suy ra hoàn toàn từ dữ liệu đã ghi đĩa, không phụ thuộc mô tả nhiệm vụ, cũng không tin số chương/số cung volume do model tự điền;
// việc kết thúc idempotent khi cùng nội dung đã ghi đĩa do từng công cụ tự nhận diện trước khi gọi hàm này.
func requireAggregateTarget(st *store.Store, kind flow.AggregateKind, volume, arc, endChapter int) error {
	state, err := flow.LoadState(st)
	if err != nil {
		return fmt.Errorf("load aggregate state: %w: %w", errs.ErrStoreRead, err)
	}
	due := state.AggregateRefresh
	if due == nil {
		return fmt.Errorf("hiện không có artefact %s nào chờ xử lý: %w", kind, errs.ErrToolPrecondition)
	}
	targetMismatch := due.Kind != kind
	switch kind {
	case flow.AggregateArcReview, flow.AggregateArcSummary:
		targetMismatch = targetMismatch || due.Volume != volume || due.Arc != arc
	case flow.AggregateVolumeSummary:
		targetMismatch = targetMismatch || due.Volume != volume
	case flow.AggregateGlobalReview:
		// Xem xét toàn cục không có tọa độ volume/arc, chỉ định vị bằng kind và chương kết thúc.
	}
	endMismatch := endChapter > 0 && due.EndChapter != endChapter
	if targetMismatch || endMismatch {
		return fmt.Errorf(
			"đích ghi tổng hợp không khớp: hiện đang cần xử lý kind=%s volume=%d arc=%d end_chapter=%d, nhận được kind=%s volume=%d arc=%d end_chapter=%d: %w",
			due.Kind, due.Volume, due.Arc, due.EndChapter,
			kind, volume, arc, endChapter, errs.ErrToolConflict,
		)
	}
	return nil
}
