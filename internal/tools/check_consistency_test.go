package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/internal/store"
)

func TestCheckConsistencyReturnsPartialFactsWithWarnings(t *testing.T) {
	dir := t.TempDir()
	st := store.NewStore(dir)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := st.Drafts.SaveDraft(1, "可供检查的章节草稿"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "world_rules.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	raw, err := NewCheckConsistencyTool(st).Execute(context.Background(), json.RawMessage(`{"chapter":1}`))
	if err != nil {
		t.Fatalf("dữ liệu phụ bị hỏng không được làm gián đoạn kiểm tra nhất quán: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "partial" || len(got["_warnings"].([]any)) == 0 || got["content"] == "" {
		t.Fatalf("phải trả về nội dung, partial và cảnh báo dữ liệu: %+v", got)
	}
}
