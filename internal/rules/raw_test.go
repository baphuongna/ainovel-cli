package rules

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRawFileSources_ScansAllMarkdownInOrder xác nhận mọi .md trong thư mục đều được quét,
// trả theo thứ tự từ điển tên file; file không phải .md bị bỏ qua; văn bản gốc giữ nguyên trạng.
func TestRawFileSources_ScansAllMarkdownInOrder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("b.md", "# B 偏好")
	write("a.md", "# A 偏好")
	write("ignore.txt", "not a rule")
	write("empty.md", "   ") // file toàn khoảng trắng phải bỏ qua

	srcs := RawFileSources(LoadOptions{HomeRulesDir: dir})
	if len(srcs) != 2 {
		t.Fatalf("Phải quét được 2 nguồn a.md / b.md (.txt và trắng bỏ qua), nhận %d: %+v", len(srcs), srcs)
	}
	// Thứ tự từ điển: a trước b sau
	if srcs[0].Label != "global:a.md" || srcs[1].Label != "global:b.md" {
		t.Errorf("Phải trả theo thứ tự từ điển, nhận %q, %q", srcs[0].Label, srcs[1].Label)
	}
	for _, s := range srcs {
		if s.Kind != SourceGlobal {
			t.Errorf("Nguồn HomeRulesDir phải là SourceGlobal, nhận %v", s.Kind)
		}
	}
}

// TestRawFileSources_DirMissing xác nhận thư mục không tồn tại thì bỏ qua lặng lẽ (trả nil).
func TestRawFileSources_DirMissing(t *testing.T) {
	srcs := RawFileSources(LoadOptions{HomeRulesDir: filepath.Join(t.TempDir(), "nope")})
	if len(srcs) != 0 {
		t.Errorf("Thư mục thiếu phải trả 0 nguồn, nhận %d", len(srcs))
	}
	if len(RawFileSources(LoadOptions{})) != 0 {
		t.Error("LoadOptions rỗng phải trả 0 nguồn")
	}
}

// TestRawFileSources_IgnoresHiddenAndSubdirs chốt cứng: file ẩn/tạm của trình soạn thảo
// (bắt đầu bằng .) bị bỏ qua, thư mục con không đệ quy — tránh nội dung nhị phân bẩn của file
// bẩn bị inject vào LLM như văn bản sở thích.
func TestRawFileSources_IgnoresHiddenAndSubdirs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real.md"), []byte("# real"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dirty := range []string{"._real.md", ".#lock.md", ".hidden.md"} {
		if err := os.WriteFile(filepath.Join(dir, dirty), []byte("\x00binary garbage\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.md"), []byte("# nested"), 0o644); err != nil {
		t.Fatal(err)
	}

	srcs := RawFileSources(LoadOptions{HomeRulesDir: dir})
	if len(srcs) != 1 || srcs[0].Label != "global:real.md" {
		t.Fatalf("Phải chỉ quét được real.md (ẩn/bẩn/thư mục con bỏ qua), nhận %+v", srcs)
	}
}

// TestRawFileSources_GlobalThenProject xác nhận nguồn toàn cục đứng trước, nguồn dự án đứng sau.
func TestRawFileSources_GlobalThenProject(t *testing.T) {
	base := t.TempDir()
	global := filepath.Join(base, "global")
	project := filepath.Join(base, "project")
	for _, d := range []string{global, project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(global, "g.md"), []byte("# 全局"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "p.md"), []byte("# 本书"), 0o644); err != nil {
		t.Fatal(err)
	}

	srcs := RawFileSources(LoadOptions{HomeRulesDir: global, ProjectRulesDir: project})
	if len(srcs) != 2 || srcs[0].Kind != SourceGlobal || srcs[1].Kind != SourceProject {
		t.Fatalf("Phải toàn cục trước dự án sau, nhận %+v", srcs)
	}
}
