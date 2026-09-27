package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/voocel/agentcore/schema"
	agentcoretools "github.com/voocel/agentcore/tools"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// EditChapterTool thay thế chuỗi tại điểm cố định trên bản nháp chương, hợp với cảnh đánh bóng.
// So với viết lại cả chương bằng draft_chapter, tiết kiệm token hơn 10 lần.
//
// Hợp đồng ghi đĩa: chỉ sửa drafts/{ch:02d}.draft.md, cấm sửa trực tiếp chapters/ (chính văn bản cuối do commit_chapter độc quyền).
// Ngữ nghĩa Seed: drafts không tồn tại nhưng chapters có → tự động sao chép chapters sang drafts làm điểm khởi đầu.
// Kiểm tra quyền sở hữu: chỉ cho phép sửa chương đã hoàn thành và đang nằm trong hàng đợi PendingRewrites.
//
// Công cụ này là lớp bọc mỏng của agentcore.EditTool, logic tìm-thay (khớp dung sai đa tầng, xuất diff, giữ nguyên cuối dòng/BOM)
// dùng lại toàn bộ cài đặt thượng nguồn.
type EditChapterTool struct {
	store *store.Store
	edit  *agentcoretools.EditTool
}

func NewEditChapterTool(s *store.Store) *EditChapterTool {
	return &EditChapterTool{
		store: s,
		edit:  agentcoretools.NewEdit(s.Dir(), nil),
	}
}

func (t *EditChapterTool) Name() string  { return "edit_chapter" }
func (t *EditChapterTool) Label() string { return "Sửa chương" }

// ReadOnly khai báo tường minh đây là công cụ ghi (kết hợp ConcurrencySafeTool tránh bị điều độ đồng thời).
func (t *EditChapterTool) ReadOnly(_ json.RawMessage) bool { return false }

// ConcurrencySafe cấm đồng thời tường minh: nhiều edit_chapter cùng chương chạy song song sẽ đua đọc-sửa-ghi,
// kể cả khác chương chạy song song cũng làm xâu xén thứ tự checkpoint. Thống nhất tuần tự là ổn nhất.
func (t *EditChapterTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

// ActivityDescription cung cấp cho UI/log mô tả hoạt động của công cụ hiện tại.
func (t *EditChapterTool) ActivityDescription(_ json.RawMessage) string {
	return "Sửa bản nháp chương"
}

func (t *EditChapterTool) Description() string {
	return "Chỉ thay thế chuỗi tại điểm cố định trên bản nháp của chương đã hoàn thành và đã vào hàng đợi PendingRewrites (lựa chọn hàng đầu cho cảnh đánh bóng, tiết kiệm token hơn viết lại cả chương bằng draft_chapter)." +
		"Bản nháp ban đầu của chương mới cấm dùng công cụ này; nháp ban đầu có lỗi nặng hãy gọi draft_chapter(mode=\"write\") ghi đè cả chương." +
		"Tìm old_string và thay bằng new_string, yêu cầu khớp chính xác và duy nhất (khớp nhiều chỗ cần replace_all=true)." +
		"old_string phải sao chép nguyên văn từ kết quả trả về của lần read_chapter(source=\"draft\") gần nhất, cấm dựng lại nguyên văn theo trí nhớ;" +
		"Chú ý giá trị trả về là chuỗi JSON, \\n phải hoàn nguyên thành ký tự xuống dòng thật. Sau khi draft_chapter đã sửa bản nháp thì phải read_chapter lại rồi mới chỉnh sửa." +
		"Báo lỗi khi khớp thất bại sẽ kèm đoạn ứng viên gần nhất trong bản nháp, hãy sao chép nguyên văn từ ứng viên rồi thử lại." +
		"Ghi vào drafts/{ch}.draft.md; khi drafts không tồn tại thì tự seed từ chapters." +
		"Từ chối thực thi khi chương đã hoàn thành mà không nằm trong hàng đợi PendingRewrites. Mỗi lần gọi chỉ sửa một chỗ, cần sửa nhiều chỗ hãy gọi nhiều lần."
}

func (t *EditChapterTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("số chương")).Required(),
		schema.Property("old_string", schema.String("đoạn nguyên văn chính xác cần thay thế, nhiều dòng phải chứa ký tự xuống dòng; khi không kèm replace_all phải xuất hiện duy nhất trong bản nháp")).Required(),
		schema.Property("new_string", schema.String("văn bản mới sau khi thay thế")).Required(),
		schema.Property("replace_all", schema.Bool("thay thế mọi chỗ khớp (mặc định false)")),
	)
}

func (t *EditChapterTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter    int    `json:"chapter"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if a.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	if a.OldString == "" {
		return nil, fmt.Errorf("old_string không được rỗng: %w", errs.ErrToolArgs)
	}
	if a.OldString == a.NewString {
		return nil, fmt.Errorf("old_string giống new_string, không cần sửa: %w", errs.ErrToolArgs)
	}
	if err := t.store.Progress.ValidateChapterWork(a.Chapter); err != nil {
		return nil, err
	}

	// Kiểm tra quyền sở hữu: thực thi cơ khí giao thức writer. Bản nháp ban đầu của chương mới chỉ được ghi đè cả chương, không thể dựa
	// model tự giác tuân theo prompt mà vẫn lộ đường chỉnh sửa chính xác mong manh thành lối thực thi được.
	completed, err := t.store.Progress.IsChapterCompleted(a.Chapter)
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if !completed {
		return nil, fmt.Errorf("chương %d chưa hoàn thành, bản nháp ban đầu cấm dùng edit_chapter; có lỗi nặng hãy gọi draft_chapter(mode=\"write\", chapter=%d) ghi đè cả chương: %w", a.Chapter, a.Chapter, errs.ErrToolPrecondition)
	}
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil || !slices.Contains(progress.PendingRewrites, a.Chapter) {
		return nil, fmt.Errorf("chương %d đã hoàn thành và không nằm trong hàng đợi PendingRewrites nên không thể sửa; cần sửa hãy để editor xem xét kích hoạt viết lại/đánh bóng trước: %w", a.Chapter, errs.ErrToolPrecondition)
	}
	if err := EnsureChapterExpanded(t.store, a.Chapter); err != nil {
		return nil, err
	}

	// Seed: khi drafts không tồn tại thì sao chép một bản từ chapters làm điểm khởi đầu
	if err := t.ensureDraft(a.Chapter); err != nil {
		return nil, err
	}

	// Ủy thác agentcore.EditTool hoàn thành tìm-thay
	subArgs, _ := json.Marshal(map[string]any{
		"path":        fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		"file_path":   fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
		"old_text":    a.OldString,
		"old_string":  a.OldString,
		"new_text":    a.NewString,
		"new_string":  a.NewString,
		"replace_all": a.ReplaceAll,
	})
	result, err := t.edit.Execute(ctx, subArgs)
	if err != nil {
		return nil, fmt.Errorf("apply edit: %w: %w", errs.ErrToolPrecondition, err)
	}

	if _, err := t.store.Checkpoints.AppendArtifact(
		domain.ChapterScope(a.Chapter), "edit",
		fmt.Sprintf("drafts/%02d.draft.md", a.Chapter),
	); err != nil {
		return nil, fmt.Errorf("checkpoint edit: %w: %w", errs.ErrStoreWrite, err)
	}

	// Chỉ dẫn kèm theo: cho writer biết các bước tiếp theo, tránh bỏ sót check_consistency / commit_chapter
	var passthrough map[string]any
	if err := json.Unmarshal(result, &passthrough); err != nil {
		return result, nil
	}
	passthrough["chapter"] = a.Chapter
	passthrough["next_step"] = "edit đã ghi đĩa. Vẫn còn lỗi nặng có thể edit_chapter tiếp; nếu không thì check_consistency rồi commit_chapter"
	return json.Marshal(passthrough)
}

// ensureDraft bảo đảm drafts/{ch}.draft.md tồn tại:
//   - Đã có bản nháp → trả về ngay
//   - Không có bản nháp nhưng có chính văn bản cuối → sao chép chính văn bản cuối sang drafts làm điểm khởi đầu sửa (thường gặp ở cảnh đánh bóng)
//   - Đều không có → báo lỗi, gợi ý dùng draft_chapter tạo bản nháp ban đầu trước
func (t *EditChapterTool) ensureDraft(chapter int) error {
	draft, err := t.store.Drafts.LoadDraft(chapter)
	if err != nil {
		return fmt.Errorf("load draft: %w: %w", errs.ErrStoreRead, err)
	}
	if draft != "" {
		return nil
	}
	text, err := t.store.Drafts.LoadChapterText(chapter)
	if err != nil {
		return fmt.Errorf("load chapter: %w: %w", errs.ErrStoreRead, err)
	}
	if text == "" {
		return fmt.Errorf("chương %d không có bản nháp lẫn chính văn bản cuối, hãy gọi draft_chapter(mode=write, chapter=%d) tạo bản nháp ban đầu trước: %w", chapter, chapter, errs.ErrToolPrecondition)
	}
	if err := t.store.Drafts.SaveDraft(chapter, text); err != nil {
		return fmt.Errorf("seed draft from chapter: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}
