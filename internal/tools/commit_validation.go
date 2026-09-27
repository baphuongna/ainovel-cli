package tools

import (
	"fmt"

	"github.com/voocel/ainovel-cli/internal/chapterfacts"
	"github.com/voocel/ainovel-cli/internal/errs"
)

// validateCommitArgs kiểm tra toàn bộ payload ngữ nghĩa mà model nộp trước khi tạo PendingCommit.
// Lỗi được trả thẳng về cho model sửa; không tạo trạng thái dở dang, cũng không đoán giá trị thiếu.
func (t *CommitChapterTool) validateCommitArgs(a commitArgs) error {
	if err := chapterfacts.Validate(a.ChapterFacts); err != nil {
		return fmt.Errorf("%v: %w", err, errs.ErrToolArgs)
	}

	if len(a.ForeshadowUpdates) > 0 {
		ledger, err := t.store.World.LoadForeshadowLedger()
		if err != nil {
			return fmt.Errorf("load foreshadow ledger: %w: %w", errs.ErrStoreRead, err)
		}
		// Sổ cái là phép chiếu của cả cuốn sách, còn Projector phát lại bản ghi chương theo thứ tự chương. Khi viết lại chương trước, sổ cái
		// vẫn còn chứa phục bút do các chương sau mới gieo — nếu cho phép chúng, kết quả kiểm tra trước khi nộp sẽ trái với kết luận phát lại,
		// model không có chỗ sửa, hàng đợi viết lại theo đó bị khóa cứng. Vì vậy lấy chuẩn thống nhất là "hiện hình trong chương này".
		plantedAt := make(map[string]int, len(ledger))
		for _, entry := range ledger {
			plantedAt[entry.ID] = entry.PlantedAt
		}
		declared := make(map[string]struct{}, len(a.ForeshadowUpdates))
		for i, update := range a.ForeshadowUpdates {
			switch update.Action {
			case "plant":
				declared[update.ID] = struct{}{}
			case "advance", "resolve":
				if _, ok := declared[update.ID]; ok {
					continue
				}
				at, known := plantedAt[update.ID]
				if !known {
					return fmt.Errorf("foreshadow_updates[%d] references unknown id %q: %w", i, update.ID, errs.ErrToolPrecondition)
				}
				if at > a.Chapter {
					return fmt.Errorf("foreshadow_updates[%d]: phục bút %q được gieo ở chương %d, không thể triển khai hay thu hồi ở chương %d: %w",
						i, update.ID, at, a.Chapter, errs.ErrToolPrecondition)
				}
			}
		}
	}
	return nil
}
