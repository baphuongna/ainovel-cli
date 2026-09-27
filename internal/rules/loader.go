package rules

import (
	"os"
	"path/filepath"
)

// LoadOptions liệt kê các thư mục nguồn file rules, để RawFileSources quét và chuẩn hóa.
//
// Thư mục không tồn tại không tính là lỗi, khi quét bỏ qua lặng lẽ.
type LoadOptions struct {
	// HomeRulesDir là thư mục ~/.ainovel/rules/; quét mọi .md tầng cao nhất của nó (hợp nhất theo thứ tự từ điển tên file). Rỗng nghĩa là bỏ qua.
	HomeRulesDir string

	// ProjectRulesDir là thư mục ./.ainovel/rules/ (soi gương tầng toàn cục, cũng quét mọi .md tầng cao nhất của nó). Rỗng nghĩa là bỏ qua.
	ProjectRulesDir string
}

// ainovelDirName là tên dotdir dùng chung cho ainovel ở hai tầng user / project.
// Toàn cục ~/.ainovel/rules/ và dự án ./.ainovel/rules/ đối xứng nhờ đây.
const ainovelDirName = ".ainovel"

// DefaultProjectRulesDir ghép đường dẫn tuyệt đối của ./.ainovel/rules/ (dựa trên thư mục dự án cho trước).
// Nơi gọi truyền vào gốc dự án, tránh phụ thuộc cwd bên trong loader; soi gương DefaultHomeRulesDir.
func DefaultProjectRulesDir(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	return filepath.Join(projectDir, ainovelDirName, "rules")
}

// DefaultHomeRulesDir ghép đường dẫn tuyệt đối của thư mục ~/.ainovel/rules/.
// Phân giải home thất bại trả chuỗi rỗng (nơi gọi dựa đó bỏ qua nguồn này).
func DefaultHomeRulesDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ainovelDirName, "rules")
}

// homeRulesReadme là phần hướng dẫn ghi vào ~/.ainovel/rules/README.txt khi lần đầu dẫn dắt.
// Cố ý dùng hậu tố .txt thay vì .md — phần quét chỉ nhận .md, bản hướng dẫn này không bị coi là quy tắc để chuẩn hóa.
const homeRulesReadme = `Đặt sở thích viết toàn cục ở đây, có hiệu lực với mọi cuốn sách.

Tạo một file .md mới (ví dụ my-style.md), viết yêu cầu bằng lời lẽ bình dân là được —
không cần định dạng gì, không cần YAML:

    # Nhân vật
    - Đừng viết nhân vật chính thành dạng thánh thiện, lạnh ngoài nóng trong là được
    # Phong cách
    - Dùng nhiều cảm nhận thân thể (khớp ngón tay trắng bệch) thay cho nhãn cảm xúc (căng thẳng)
    - Đối thoại đừng quá văn vhơ, mỗi chương khoảng 3000 chữ
    - Không xuất hiện giọng AI kiểu "某种程度上"

Viết xong không cần bận tâm định dạng: hệ thống sẽ dùng model chuẩn hóa các yêu cầu
ngôn ngữ tự nhiên này thành ràng buộc cấu trúc (khoảng số chữ, từ cấm, ngưỡng từ mệt mỏi
v.v.), tự động tuân theo lúc viết, tự động tự kiểm lúc nộp.

Nhiều file .md hợp nhất theo thứ tự từ điển tên file; file ẩn bắt đầu bằng dấu chấm, file
không phải .md đều bị bỏ qua (cho nên README.txt này không bị coi là quy tắc).

Cơ sở cơ học cho sáo câu AI thường gặp và từ mệt mỏi đã dựng sẵn, dùng được ngay, không viết cũng không sao.

Thứ tự ưu tiên tải (cao → thấp): ./.ainovel/rules/*.md (cuốn sách này) > ~/.ainovel/rules/*.md (ở đây) > mặc định dựng sẵn
`

// EnsureHomeRulesDir cố gắng tạo thư mục ~/.ainovel/rules/ và ghi README.txt dẫn dắt,
// để người dùng khám phá điểm mở rộng sở thích toàn cục này và biết cách viết.
// nice-to-have, không phải đường tới quan trọng: phân giải home thất bại hay ghi lỗi đều nuốt lặng lẽ, tuyệt đối không chặn khởi động.
func EnsureHomeRulesDir() {
	if dir := DefaultHomeRulesDir(); dir != "" {
		_ = ensureRulesDirAt(dir)
	}
}

// ensureRulesDirAt tạo thư mục và ghi README.txt theo mẫu dẫn dắt hiện hành, là nhân hạt
// test được của EnsureHomeRulesDir. README.txt là file dẫn dắt do hệ thống sinh (sở thích người
// dùng viết trong *.md, nó không bị quét tải), mỗi lần đều đè bằng mẫu mới nhất — không giữ nội
// dung cũ, nên cũng không cần bất kỳ logic tương thích phiên bản nào.
func ensureRulesDirAt(dir string) error {
	// 0o700: thư mục thuộc cấu hình riêng của người dùng (cả họ .ainovel siết chặt thống nhất, review L2).
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "README.txt"), []byte(homeRulesReadme), 0o644)
}

// DefaultOptions dựng LoadOptions thường dùng dựa trên thư mục làm việc hiện tại.
//
// Hợp để Host gọi một lần lúc khởi động, cho dịch vụ quy tắc người dùng tái dụng cùng một
// cấu hình nguồn. Phân giải cwd thất bại thì ProjectRulesDir để rỗng (quét sẽ bỏ qua nguồn đó).
//
// Ngữ nghĩa đường dẫn: ProjectRulesDir buộc vào **thư mục làm việc hiện tại (cwd)** chứ không phải outputDir.
// Người dùng cd sang thư mục khác để viết sách khác, ./.ainovel/rules/ tự nhiên theo cwd; nếu cần
// chia sẻ xuyên sách, đặt vào thư mục toàn cục ~/.ainovel/rules/ là được (mọi .md dưới nó đều được tải).
func DefaultOptions() LoadOptions {
	cwd, _ := os.Getwd()
	return LoadOptions{
		HomeRulesDir:    DefaultHomeRulesDir(),
		ProjectRulesDir: DefaultProjectRulesDir(cwd),
	}
}
