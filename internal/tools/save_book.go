package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveBookTool lưu thông tin đối ngoại của tác phẩm, chuyên dùng cho Architect.
type SaveBookTool struct{ store *store.Store }

func NewSaveBookTool(store *store.Store) *SaveBookTool { return &SaveBookTool{store: store} }

func (t *SaveBookTool) Name() string { return "save_book" }
func (t *SaveBookTool) Description() string {
	return "Lưu tên sách và giới thiệu không tiết lộ cốt truyện hướng đến người đọc. Giới thiệu nên trình bày nhân vật chính, xung đột cốt lõi và móc đọc, không được viết thành dàn ý nội bộ hoặc tóm tắt kết cục."
}
func (t *SaveBookTool) Label() string                          { return "Lưu thông tin tác phẩm" }
func (t *SaveBookTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveBookTool) ConcurrencySafe(_ json.RawMessage) bool { return false }
func (t *SaveBookTool) StrictSchema() bool                     { return true }

func (t *SaveBookTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("title", schema.String("Tên sách chính thức, không đặt trong dấu ngoặc nháy")).Required(),
		schema.Property("synopsis", schema.String("Giới thiệu tiểu thuyết không tiết lộ cốt truyện, hướng đến người đọc")).Required(),
	)
}

func (t *SaveBookTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var book domain.BookMetadata
	if err := json.Unmarshal(args, &book); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if err := book.Validate(); err != nil {
		return nil, fmt.Errorf("invalid book metadata: %w: %w", errs.ErrToolArgs, err)
	}
	if err := t.store.Book.Save(book); err != nil {
		return nil, fmt.Errorf("save book metadata: %w: %w", errs.ErrStoreWrite, err)
	}
	if _, err := t.store.Checkpoints.AppendArtifact(domain.GlobalScope(), "book", "meta/book.json"); err != nil {
		return nil, fmt.Errorf("checkpoint book metadata: %w: %w", errs.ErrStoreWrite, err)
	}
	remaining, err := t.store.FoundationMissing()
	if err != nil {
		return nil, fmt.Errorf("load foundation state: %w: %w", errs.ErrStoreRead, err)
	}
	return json.Marshal(map[string]any{
		"saved":            true,
		"foundation_ready": len(remaining) == 0,
		"remaining":        remaining,
	})
}
