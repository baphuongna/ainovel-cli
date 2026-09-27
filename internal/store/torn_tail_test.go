package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// ── review V-2 MAJOR1: giao thức tự chữa torn-tail của loadJSONLines ──

// Nửa dòng do mất điện/crash để lại (không có xuống dòng kết thúc) phải bị cắt khi tải,
// nếu không một torn write sẽ làm treo vĩnh viễn headless --resume và việc nối thêm hàng đợi.
func TestLoadJSONLinesHealsTornTail(t *testing.T) {
	dir := t.TempDir()
	io := newIO(dir)
	if err := io.EnsureDirs([]string{"meta/runtime"}); err != nil {
		t.Fatal(err)
	}
	rel := "meta/runtime/queue.jsonl"
	if err := io.AppendLine(rel, []byte(`{"seq":1,"kind":"tick"}`+"\n")); err != nil {
		t.Fatal(err)
	}
	// Mô phỏng mất điện: dòng thứ hai viết dở (không \n).
	if err := io.AppendLine(rel, []byte(`{"seq":2,"kind":"tor`)); err != nil {
		t.Fatal(err)
	}

	items, err := loadJSONLines[map[string]any](io, rel)
	if err != nil {
		t.Fatalf("torn tail phải được tự chữa thay vì báo lỗi: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("phải chỉ còn 1 bản ghi đã commit, nhận được %d", len(items))
	}
	// Nửa dòng trên đĩa phải thực sự bị cắt đi.
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "\n") || strings.Contains(string(data), `"tor`) {
		t.Fatalf("tệp phải bị cắt đến cuối dòng đã commit: %q", data)
	}
}

// Dòng hoàn chỉnh (có \n) nhưng JSON hỏng: giữ fail-loud, nhưng lỗi phải nêu rõ số dòng và đường dẫn sửa.
func TestLoadJSONLinesCorruptCompleteLineFailsWithHint(t *testing.T) {
	dir := t.TempDir()
	io := newIO(dir)
	if err := io.EnsureDirs([]string{"meta/runtime"}); err != nil {
		t.Fatal(err)
	}
	rel := "meta/runtime/queue.jsonl"
	if err := io.AppendLine(rel, []byte(`{"seq":1}`+"\n")); err != nil {
		t.Fatal(err)
	}
	if err := io.AppendLine(rel, []byte(`{"seq":2, NOT-JSON}`+"\n")); err != nil {
		t.Fatal(err)
	}

	_, err := loadJSONLines[map[string]any](io, rel)
	if err == nil {
		t.Fatal("dòng hoàn chỉnh bị hỏng phải báo lỗi (fail-loud)")
	}
	for _, want := range []string{"dòng 2", "sửa"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("thông báo lỗi phải chứa %q (đường dẫn sửa khả thi), nhận được: %v", want, err)
		}
	}
}

// ── review V-2.5: taskID từ chối nối đường dẫn ──

func TestTaskLogRejectsTraversalTaskID(t *testing.T) {
	rs := NewRuntimeStore(newIO(t.TempDir()))
	for _, bad := range []string{`..\..\evil`, "../../evil", "a/b", "..", "con\x00"} {
		if err := rs.AppendTaskLog(bad, domain.RuntimeTaskLogEntry{}); err == nil {
			t.Errorf("AppendTaskLog(%q) phải từ chối task id không hợp lệ", bad)
		}
		if _, err := rs.LoadTaskLog(bad); err == nil {
			t.Errorf("LoadTaskLog(%q) phải từ chối task id không hợp lệ", bad)
		}
	}
	// Id hợp lệ vẫn dùng được.
	if err := rs.AppendTaskLog("task-01.sub_x", domain.RuntimeTaskLogEntry{}); err != nil {
		t.Errorf("task id hợp lệ không được từ chối: %v", err)
	}
}

// ── review V-2 MAJOR1b: khi mirror checkpoints hỏng thì All() không còn im lặng ──

func TestCheckpointStoreAllWarnsWhenLoadFailed(t *testing.T) {
	dir := t.TempDir()
	io := newIO(dir)
	if err := io.EnsureDirs([]string{"meta"}); err != nil {
		t.Fatal(err)
	}
	// Dòng hoàn chỉnh hỏng → loadErr (giữ strict).
	if err := io.AppendLine("meta/checkpoints.jsonl", []byte(`{"seq":"bogus"}`+"\n")); err != nil {
		t.Fatal(err)
	}
	cs := NewCheckpointStore(io)
	if cs.InitError() == nil {
		t.Fatal("dòng hoàn chỉnh hỏng phải tạo InitError")
	}
	if all := cs.All(); all != nil {
		t.Fatalf("All() của mirror hỏng phải trả về rỗng, nhận được %d bản ghi", len(all))
	}
	// Thông báo lỗi Init phải chứa chỉ dẫn sửa.
	if err := cs.InitError(); !strings.Contains(err.Error(), "checkpoint") {
		t.Errorf("InitError phải dễ đọc: %v", err)
	}
}
