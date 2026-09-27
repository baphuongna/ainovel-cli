package rules

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RawSource là một nguồn gốc chờ chuẩn hóa (toàn bộ văn bản của file rules).
//
// Sau khi cắt YAML, file rules chỉ là prompt ngôn ngữ tự nhiên thường; chuẩn hóa chỉ cần văn bản gốc, không còn phân tích front matter.
type RawSource struct {
	Label string     // nhãn nguồn, vào Snapshot.Sources (như global:my-style.md)
	Kind  SourceKind // tầng mức ưu tiên
	Text  string     // nội dung gốc của file
}

// RawFileSources liệt kê file .md trong thư mục rules theo thứ tự Global → Project và trả văn bản gốc.
//
// Cùng quy ước quét như readDirFromDisk (.md tầng cao nhất, thứ tự từ điển, bỏ qua file ẩn), nhưng
// không phân tích YAML, toàn bộ văn bản giao nguyên trạng cho bộ chuẩn hóa. System defaults / prompt
// khởi động / yêu cầu lúc chạy do service cung cấp riêng.
func RawFileSources(opts LoadOptions) []RawSource {
	var out []RawSource
	out = append(out, rawDir(opts.HomeRulesDir, SourceGlobal)...)
	proj := rawDir(opts.ProjectRulesDir, SourceProject)
	// Rules tầng dự án đến từ thư mục làm việc hiện tại, thuộc biên không tin cậy (workspace
	// tiểu thuyết chia sẻ/tải về có thể mang chỉ lệnh inject điều khiển hướng viết) — khi tải
	// để lại dấu vết tường minh, cho người dùng biết quy tắc nào đang hiệu lực.
	if len(proj) > 0 {
		names := make([]string, 0, len(proj))
		for _, s := range proj {
			names = append(names, s.Label)
		}
		slog.Info("Đã tải quy tắc viết cấp dự án (ưu tiên: dự án > toàn cục > dựng sẵn; thư mục nguồn là thư mục làm việc hiện tại, nếu không phải do bạn cấu hình hãy kiểm tra ./.ainovel/rules/)",
			"module", "rules", "files", strings.Join(names, ", "))
	}
	return append(out, proj...)
}

func rawDir(dir string, kind SourceKind) []RawSource {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Thư mục không tồn tại là chuyện thường, bỏ qua lặng lẽ; nhưng lỗi kiểu quyền/đường dẫn
		// thực ra là file phải để lại dấu vết — nếu không, người dùng viết quy tắc mà hoàn toàn
		// không hiệu lực, không một phản hồi, chi phí truy vét cực cao (xem known_rules_path_stale_readme).
		if !os.IsNotExist(err) {
			slog.Warn("Đọc thư mục quy tắc thất bại, đã bỏ qua", "module", "rules", "dir", dir, "err", err)
		}
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var out []RawSource
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("Đọc file quy tắc thất bại, đã bỏ qua", "module", "rules", "file", path, "err", err)
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		out = append(out, RawSource{
			Label: kind.String() + ":" + name,
			Kind:  kind,
			Text:  text,
		})
	}
	return out
}
