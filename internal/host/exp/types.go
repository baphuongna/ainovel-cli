// Package exp hiện thực năng lực xuất các chương đã hoàn thành.
//
// Đối xứng với imp/: IO thuần cục bộ, không phụ thuộc LLM, không đổi trạng thái store.
// Xuất có thể chạy song song với Engine (chỉ đọc Progress + chính văn bản cuối của chương),
// thuộc nhóm năng lực ngang.
//
// Hiện hỗ trợ TXT và EPUB.
package exp

import "github.com/voocel/ainovel-cli/internal/store"

// Format định danh định dạng xuất.
type Format string

const (
	// FormatTXT xuất văn bản thuần.
	FormatTXT Format = "txt"
	// FormatEPUB container EPUB 3 chuẩn (zip + xhtml).
	FormatEPUB Format = "epub"
)

// Options điều khiển hành vi xuất. Giá trị zero tương đương "xuất toàn bộ sách ra
// đường dẫn mặc định, báo lỗi khi tệp đã tồn tại".
//
// Bố cục: 《Tên sách》 → phân cách tập → chính văn chương. Hai loại dữ liệu nội bộ
// không vào bản xuất: premise (bản thiết kế sáng tác, gồm độc giả mục tiêu /
// điểm hấp dẫn cốt lõi / vùng cấm khi viết… là meta hậu trường dành cho tác giả
// và engine xem, không phải lời tựa cho độc giả); phân cách cung (ở góc nhìn
// độc giả, cung là cấu trúc nội bộ quá chi tiết). Tên sách và phân cách tập luôn giữ.
type Options struct {
	// Format rỗng thì suy đoán theo hậu tố OutPath (.txt → TXT, .epub → EPUB);
	// khi OutPath cũng rỗng thì fallback FormatTXT. Bên gọi SDK có thể chỉ định
	// tường minh để bỏ bước suy đoán.
	Format Format

	// OutPath đường dẫn tệp xuất; rỗng nghĩa là {novelDir}/{BookMetadata.Title}.{ext}.
	OutPath string

	// From / To phạm vi chương, đoạn đóng. 0 nghĩa là từ chương 1 / đến chương cuối.
	// Chương chưa hoàn thành nằm trong phạm vi sẽ bị bỏ qua và ghi vào Result.Skipped,
	// không tính là lỗi.
	From, To int

	// Overwrite có ghi đè khi tệp đã tồn tại hay không; mặc định từ chối.
	Overwrite bool
}

// Deps là dependency cần cho Run. Chỉ store; xuất không cần LLM, prompt, bundle.
type Deps struct {
	Store *store.Store
}

// Result là tóm tắt sản phẩm của một lần xuất thành công.
type Result struct {
	// Path đường dẫn tệp thực tế đã ghi (tuyệt đối, hoặc tương đối như bên gọi truyền vào).
	Path string
	// Chapters số chương thực tế đã ghi.
	Chapters int
	// Bytes số byte của tệp (UTF-8).
	Bytes int
	// Skipped số chương nằm trong phạm vi yêu cầu nhưng chưa hoàn thành.
	Skipped []int
}
