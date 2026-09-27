// Package store cung cấp tầng lưu trữ bền vững dựa trên hệ thống file.
//
// Kiến trúc: 1 nền IO + nhiều kho con + 1 gốc tổ hợp.
// Mỗi kho con giữ một instance IO riêng và một sync.RWMutex riêng.
// Các lĩnh vực chính (Progress, Outline, Drafts, Summaries...) đọc ghi không chặn lẫn
// nhau; WorldStore gộp nhiều lĩnh vực nhỏ tần suất thấp để dùng chung một khoá.
//
// Gốc tổ hợp Store giữ tham chiếu tới mọi kho con, và điều phối tuần tự các thao tác
// xuyên lĩnh vực (ExpandArc, AppendVolume, ClearHandledSteer); nhiều file không tạo
// thành giao dịch commit nguyên tử, bên gọi dựa vào thứ tự ghi an toàn, lỗi tường minh
// và replay idempotent cùng tham số để phục hồi.
//
// Phân chia kho con:
//   - ProgressStore: trạng thái chính của tiến độ (meta/progress.json)
//   - OutlineStore: tiền đề, dàn ý (phẳng/phân tầng), la bàn
//   - DraftStore: ý tưởng chương, bản nháp, chính văn bản cuối
//   - SummaryStore: tóm tắt chương/cung/tập
//   - RunMetaStore: siêu dữ liệu vận hành (model, lịch sử can thiệp)
//   - SignalStore: file tín hiệu dùng một lần (khôi phục PendingCommit)
//   - CheckpointStore: checkpoint cấp step (meta/checkpoints.jsonl)
//   - RuntimeStore: hàng đợi sự kiện runtime (meta/runtime/*.jsonl)
//   - CharacterStore: hồ sơ nhân vật, ảnh chụp trạng thái
//   - WorldStore: dòng thời gian, phục bút, quan hệ, thay đổi trạng thái, quy tắc thế
//     giới, quy tắc phong cách, xem xét
package store
