package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// ReopenBookTool mở lại sách đã hoàn thành và đưa vào trạng thái viết lại, do Engine gọi tại biên can thiệp.
// Sau khi hết sách, completePhaseGate chặn cứng mọi lệnh giao subagent, người dùng không thể viết lại chương đã viết.
// Công cụ này không phải subagent, có thể gọi trong giai đoạn complete: nó một thể nguyên tử chuyển phase về writing, chương mục tiêu vào
// PendingRewrites, flow=rewriting, sau đó Flow Router theo hàng đợi viết lại sẵn có giao writer viết lại từng chương,
// hàng đợi chạy xong thì commit_chapter tự động thu hồi và hoàn thành lại. Gate / Router / logic nặng của edit / commit đều không cần sửa.
type ReopenBookTool struct {
	store *store.Store
}

func NewReopenBookTool(s *store.Store) *ReopenBookTool {
	return &ReopenBookTool{store: s}
}

func (t *ReopenBookTool) Name() string  { return "reopen_book" }
func (t *ReopenBookTool) Label() string { return "Mở lại để viết lại" }

func (t *ReopenBookTool) Description() string {
	return "Mở lại toàn sách đã hoàn thành (phase=complete) vào trạng thái viết lại, dùng khi người dùng sau khi hết sách yêu cầu viết lại/đánh bóng vài chương." +
		"chapters là số các chương đã hoàn thành cần viết lại; sau khi gọi, các chương này vào hàng đợi viết lại, Host sẽ giao writer viết lại từng chương, sửa xong hết thì tự hoàn thành lại." +
		"Chỉ dùng khi toàn sách đã hoàn thành và người dùng rõ ràng yêu cầu sửa chương đã viết; người dùng muốn thêm cốt truyện/mở rộng độ dài không thuộc viết lại, đừng dùng công cụ này."
}

// Công cụ ghi, cấm đồng thời.
func (t *ReopenBookTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *ReopenBookTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *ReopenBookTool) ActivityDescription(_ json.RawMessage) string {
	return "Mở lại toàn sách để viết lại"
}

func (t *ReopenBookTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapters", schema.Array("Danh sách số chương đã hoàn thành cần viết lại (ít nhất một chương)", schema.Int(""))).Required(),
		schema.Property("reason", schema.String("Lý do viết lại (tùy chọn, ví dụ \"dọn ký tự đặc biệt\")")),
	)
}

func (t *ReopenBookTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapters []int  `json:"chapters"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if len(a.Chapters) == 0 {
		return nil, fmt.Errorf("chapters không được để trống, cần nêu rõ chương cần viết lại: %w", errs.ErrToolArgs)
	}

	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
	}
	if progress == nil {
		return nil, fmt.Errorf("progress chưa khởi tạo: %w", errs.ErrToolPrecondition)
	}
	// Chỉ được viết lại chương đã viết; số chương không nằm trong tập đã hoàn thành thuộc tiếp viết/vượt phạm vi, từ chối rõ ràng để dẫn người dùng đi điều chỉnh độ dài.
	var invalid []int
	for _, ch := range a.Chapters {
		if !slices.Contains(progress.CompletedChapters, ch) {
			invalid = append(invalid, ch)
		}
	}
	if len(invalid) > 0 {
		return nil, fmt.Errorf("chương %v chưa viết xong, reopen chỉ viết lại được chương đã hoàn thành (thêm/mở rộng cốt truyện hãy đi điều chỉnh độ dài): %w", invalid, errs.ErrToolPrecondition)
	}

	// Kiểm tra điều kiện trước của phase do store.Reopen làm lớp chốt (chỉ gọi được khi complete).
	if err := t.store.Progress.Reopen(a.Chapters, a.Reason); err != nil {
		return nil, fmt.Errorf("reopen: %w: %w", errs.ErrStoreWrite, err)
	}

	// checkpoint: đối xứng với complete_book (GlobalScope + meta/progress.json).
	if _, err := t.store.Checkpoints.AppendArtifact(domain.GlobalScope(), "reopen", "meta/progress.json"); err != nil {
		return nil, fmt.Errorf("checkpoint reopen: %w: %w", errs.ErrStoreWrite, err)
	}

	return json.Marshal(map[string]any{
		"reopened":         true,
		"phase":            string(domain.PhaseWriting),
		"pending_rewrites": a.Chapters,
		"next_step":        "Đã mở lại và xếp chương mục tiêu vào hàng đợi. Hãy chờ Host ra lệnh giao writer viết lại từng chương; sửa xong hết sẽ tự hoàn thành lại.",
	})
}
