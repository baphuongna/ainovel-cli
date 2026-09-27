package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisionStore_AppendAndRecent(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	first, err := s.Decisions.Append(DecisionRecord{
		Kind: "intervention", Decider: "arbiter",
		Input: "重写第3章", Facts: json.RawMessage(`{"phase":"writing"}`),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if first.ID == "" || first.At == "" || first.SchemaVersion != decisionSchemaVersion {
		t.Fatalf("Append phải điền đủ ID/At/SchemaVersion: %+v", first)
	}

	if _, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "继续写"}); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	// Phán định thất bại: error là dữ kiện kiểm toán, phải ghi xuống đĩa nguyên vẹn và đọc lại được.
	if _, err := s.Decisions.Append(DecisionRecord{Kind: "plan_start", Decider: "arbiter", Input: "凡人修仙", Error: "USER_INACTIVE"}); err != nil {
		t.Fatalf("append 3: %v", err)
	}

	recent, err := s.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("phải có 3 bản ghi, got %d", len(recent))
	}
	if recent[2].Error != "USER_INACTIVE" || len(recent[2].Decision) != 0 {
		t.Fatalf("phán định thất bại phải kèm error và không có decision: %+v", recent[2])
	}
	if recent[0].Input != "重写第3章" || recent[1].Input != "继续写" {
		t.Fatalf("thứ tự bản ghi phải là cũ→mới: %+v", recent)
	}

	// Cắt theo n: chỉ lấy 1 bản ghi gần nhất
	last, err := s.Decisions.Recent(1)
	if err != nil || len(last) != 1 || last[0].Input != "凡人修仙" {
		t.Fatalf("Recent(1) phải lấy bản ghi mới nhất, got %+v err=%v", last, err)
	}
}

func TestDecisionStore_InputTruncation(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	huge := strings.Repeat("长", maxDecisionInputBytes) // 3 byte/ký tự, vượt xa giới hạn
	rec, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: huge})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !rec.InputTruncated || len(rec.Input) > maxDecisionInputBytes {
		t.Fatalf("input vượt giới hạn phải bị cắt cụt và đánh dấu: truncated=%v len=%d", rec.InputTruncated, len(rec.Input))
	}
	// Bản ghi sau khi cắt cụt vẫn đọc lại được
	recent, err := s.Decisions.Recent(1)
	if err != nil || len(recent) != 1 {
		t.Fatalf("đọc lại thất bại: %v", err)
	}
}

// Dòng hỏng đã commit nằm giữa file (sau nó vẫn còn dòng commit hoàn chỉnh) phải fail
// cứng — không thể phán định trên một lịch sử không nguyên vẹn.
func TestDecisionStore_RecentRejectsCommittedCorruptLine(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "好的"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Dòng hỏng kết thúc bằng '\n' (đã commit trọn vẹn nhưng bị hỏng), sau đó lại thêm một
	// bản ghi hoàn chỉnh nữa.
	if err := s.Decisions.io.AppendLine(decisionsFile, []byte("{\"schema_version\":1,\"kind\":\"interv\n")); err != nil {
		t.Fatalf("append corrupt: %v", err)
	}
	if _, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "之后"}); err != nil {
		t.Fatalf("append trailing: %v", err)
	}
	if _, err := s.Decisions.Recent(10); err == nil {
		t.Fatal("Dòng hỏng đã commit nằm giữa file phải báo lỗi tường minh")
	}
}

// Dòng dở dang ở đuôi do crash để lại (lần thêm chưa commit có byte cuối không phải
// '\n') được dung thứ như not-exist: bỏ dòng dở dang, trả về các bản ghi hoàn chỉnh phía
// trước, không fail cứng — nếu không chỉ một lần crash đã đầu độc vĩnh viễn kiểm toán
// append-only.
func TestDecisionStore_RecentToleratesUncommittedTail(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "好的"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Mô phỏng dòng dở dang ở đuôi bị crash cắt ngang: không kết thúc bằng xuống dòng.
	if err := s.Decisions.io.AppendLine(decisionsFile, []byte(`{"schema_version":1,"kind":"interv`)); err != nil {
		t.Fatalf("append partial: %v", err)
	}
	recent, err := s.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("dòng dở dang ở đuôi phải được dung thứ, không được báo lỗi: %v", err)
	}
	if len(recent) != 1 || recent[0].Input != "好的" {
		t.Fatalf("phải bỏ dòng dở dang và giữ lại bản ghi đã commit, nhận được: %+v", recent)
	}
	// Việc khôi phục phải thực sự cắt phần đuôi trên đĩa, chứ không chỉ bỏ qua trong lần
	// đọc này; nếu không lần thêm kế tiếp sẽ nối hai đoạn JSON thành hỏng vĩnh viễn. Đọc
	// lại sau khi thêm bản ghi phải giữ được vòng khép kín trọn vẹn.
	if _, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "恢复后"}); err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
	recent, err = s.Decisions.Recent(10)
	if err != nil {
		t.Fatalf("recent after append: %v", err)
	}
	if len(recent) != 2 || recent[0].Input != "好的" || recent[1].Input != "恢复后" {
		t.Fatalf("sau khi khôi phục đuôi phải tiếp tục thêm được bản ghi, nhận được: %+v", recent)
	}
	raw, err := os.ReadFile(filepath.Join(dir, decisionsFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("file kiểm toán sau khôi phục phải kết thúc bằng xuống dòng commit: %q", raw)
	}
}

// Ngay cả khi phần đuôi tình cờ là JSON hoàn chỉnh, chỉ cần thiếu ký tự xuống dòng mà
// giao thức yêu cầu thì vẫn là bản ghi chưa commit; khôi phục phải loại bỏ nó và đảm bảo
// lần thêm sau không xảy ra tình trạng nối `}{`.
func TestDecisionStore_RecoveryDropsValidJSONWithoutCommitNewline(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := s.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "已提交"}); err != nil {
		t.Fatal(err)
	}
	partial, err := json.Marshal(DecisionRecord{SchemaVersion: decisionSchemaVersion, Kind: "intervention", Input: "未提交"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Decisions.io.AppendLine(decisionsFile, partial); err != nil {
		t.Fatal(err)
	}

	// Mô phỏng khởi động lại: lần đọc hoặc lần thêm bản ghi kế tiếp chính là ranh giới
	// khôi phục kiểm toán.
	reopened := NewStore(dir)
	if err := reopened.Init(); err != nil {
		t.Fatalf("restart init: %v", err)
	}
	if _, err := reopened.Decisions.Append(DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: "重启后"}); err != nil {
		t.Fatal(err)
	}
	recent, err := reopened.Decisions.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].Input != "已提交" || recent[1].Input != "重启后" {
		t.Fatalf("JSON chưa commit và không có xuống dòng không được chấp nhận, nhận được: %+v", recent)
	}
}
