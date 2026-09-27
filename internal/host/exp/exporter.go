package exp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// Run thực thi một lượt xuất. Trả về đồng bộ, lượng IO nhỏ (đọc ghi tệp cục bộ).
//
// Ngữ nghĩa thất bại:
//   - deps/opts bất hợp lệ → lỗi cấu hình trả về ngay
//   - không có chương nào đã hoàn thành → trả lỗi (để bên gọi nhận biết rõ)
//   - một chương trong phạm vi thiếu chapters/{ch}.md → trả lỗi (progress mâu thuẫn
//     với hệ thống tệp là bug ở tầng sự thật, phải để người dùng thấy)
//   - đường dẫn xuất đã tồn tại và không chỉ định Overwrite → trả lỗi
//
// Skipped dùng cho trường hợp "nằm trong phạm vi hợp lệ nhưng chưa hoàn thành"
// (người dùng truyền to=100 nhưng mới viết đến 80).
func Run(ctx context.Context, deps Deps, opts Options) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deps.Store == nil {
		return nil, fmt.Errorf("exp: deps.Store is nil")
	}

	if opts.Format == "" {
		f, err := inferFormat(opts.OutPath)
		if err != nil {
			return nil, err
		}
		opts.Format = f
	}
	if opts.Format != FormatTXT && opts.Format != FormatEPUB {
		return nil, fmt.Errorf("exp: chưa hỗ trợ định dạng %q", opts.Format)
	}

	progress, err := deps.Store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("tải progress thất bại: %w", err)
	}
	if progress == nil || len(progress.CompletedChapters) == 0 {
		return nil, fmt.Errorf("chưa có chương nào đã hoàn thành, không có nội dung để xuất")
	}
	book, err := deps.Store.Book.Load()
	if err != nil {
		return nil, fmt.Errorf("tải thông tin tác phẩm thất bại: %w", err)
	}
	if book == nil {
		return nil, fmt.Errorf("thông tin tác phẩm không tồn tại, không thể xuất")
	}

	completed := make(map[int]struct{}, len(progress.CompletedChapters))
	maxCh := 0
	for _, c := range progress.CompletedChapters {
		completed[c] = struct{}{}
		if c > maxCh {
			maxCh = c
		}
	}

	from := opts.From
	if from <= 0 {
		from = 1
	}
	to := opts.To
	if to <= 0 {
		to = maxCh
	}
	if from > to {
		return nil, fmt.Errorf("phạm vi chương không hợp lệ: from=%d > to=%d", from, to)
	}

	var chapters, skipped []int
	for ch := from; ch <= to; ch++ {
		if _, ok := completed[ch]; ok {
			chapters = append(chapters, ch)
		} else {
			skipped = append(skipped, ch)
		}
	}
	if len(chapters) == 0 {
		return nil, fmt.Errorf("không có chương đã hoàn thành nào trong phạm vi %d..%d", from, to)
	}

	bodies := make(map[int]string, len(chapters))
	for _, ch := range chapters {
		text, err := deps.Store.Drafts.LoadChapterText(ch)
		if err != nil {
			return nil, fmt.Errorf("đọc chương %d thất bại: %w", ch, err)
		}
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("progress đánh dấu chương %d đã hoàn thành, nhưng chapters/%02d.md thiếu hoặc rỗng", ch, ch)
		}
		bodies[ch] = text
	}

	outline, _ := deps.Store.Outline.LoadOutline()
	var volumes []domain.VolumeOutline
	if progress.Layered {
		volumes, _ = deps.Store.Outline.LoadLayeredOutline()
	}

	outPath := opts.OutPath
	if outPath == "" {
		outPath = filepath.Join(deps.Store.Dir(), sanitizeFileName(book.Title)+"."+string(opts.Format))
	}

	if !opts.Overwrite {
		if _, err := os.Stat(outPath); err == nil {
			return nil, fmt.Errorf("tệp đã tồn tại: %s (thêm --overwrite để ghi đè)", outPath)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("kiểm tra đường dẫn xuất thất bại: %w", err)
		}
	}

	titleIdx := buildTitleIndex(outline)
	for _, ch := range chapters {
		summary, err := deps.Store.Summaries.LoadSummary(ch)
		if err != nil {
			return nil, fmt.Errorf("đọc tóm tắt chương %d thất bại: %w", ch, err)
		}
		if summary != nil && strings.TrimSpace(summary.Title) != "" {
			titleIdx[ch] = summary.Title
		}
	}
	var locations map[int]chapterLocation
	if len(volumes) > 0 {
		locations = buildLocations(volumes)
	}

	var data []byte
	switch opts.Format {
	case FormatTXT:
		data = []byte(renderTXT(book.Title, chapters, titleIdx, locations, bodies))
	case FormatEPUB:
		buf, err := renderEPUB(*book, chapters, titleIdx, locations, bodies)
		if err != nil {
			return nil, fmt.Errorf("render EPUB thất bại: %w", err)
		}
		data = buf
	}

	if err := atomicWrite(outPath, data); err != nil {
		return nil, fmt.Errorf("ghi thất bại: %w", err)
	}

	return &Result{
		Path:     outPath,
		Chapters: len(chapters),
		Bytes:    len(data),
		Skipped:  skipped,
	}, nil
}

// inferFormat suy đoán định dạng từ hậu tố của đường dẫn xuất. Đường dẫn rỗng thì fallback TXT;
// hậu tố lạ thì báo lỗi (tránh lỗi thầm lặng).
func inferFormat(path string) (Format, error) {
	if path == "" {
		return FormatTXT, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case "", ".txt":
		return FormatTXT, nil
	case ".epub":
		return FormatEPUB, nil
	default:
		return "", fmt.Errorf("không thể suy đoán định dạng từ phần mở rộng %q (hỗ trợ .txt / .epub)", filepath.Ext(path))
	}
}

// atomicWrite cùng dạng với WriteFile trong store/io.go: tmp + sync + rename.
// Không tái dùng store.IO vì đường dẫn xuất có thể nằm ngoài store.Dir().
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// sanitizeFileName thay các ký tự bị cấm hoặc dễ gây nhầm lẫn trên đa số hệ thống tệp.
// Không transcode mạnh tay, chỉ chặn bộ tách đường dẫn, ký tự điều khiển, tên dành riêng
// của Windows và dấu chấm/khoảng trắng ở đuôi tên.
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "novel"
	}
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	name = replacer.Replace(name)
	// Ký tự điều khiển (gồm NUL/DEL): đa số hệ thống tệp từ chối hoặc hành vi không xác định (review F7).
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)
	// Tên thiết bị dành riêng của Windows: CON, PRN, AUX, NUL, COM1-9, LPT1-9,
	// kèm phần mở rộng (CON.txt) cũng bất hợp lệ, thống nhất thêm tiền tố để né.
	if isWindowsReservedName(stemOf(name)) {
		name = "_" + name
	}
	// Dấu chấm/khoảng trắng ở đuôi bị Windows lặng lẽ bóc đi, khiến tên xuất khác dự kiến.
	name = strings.TrimRight(name, ". ")
	if name == "" {
		return "novel"
	}
	return name
}

// stemOf lấy phần thân của tên tệp (phần trước dấu chấm đầu tiên), dùng để so tên dành riêng.
func stemOf(name string) string {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return name
}

// windowsReservedNames là tập tên thiết bị Windows không dùng làm tên tệp được (chữ thường).
var windowsReservedNames = map[string]struct{}{
	"con": {}, "prn": {}, "aux": {}, "nul": {},
	"com1": {}, "com2": {}, "com3": {}, "com4": {}, "com5": {}, "com6": {}, "com7": {}, "com8": {}, "com9": {},
	"lpt1": {}, "lpt2": {}, "lpt3": {}, "lpt4": {}, "lpt5": {}, "lpt6": {}, "lpt7": {}, "lpt8": {}, "lpt9": {},
}

func isWindowsReservedName(stem string) bool {
	_, ok := windowsReservedNames[strings.ToLower(strings.TrimSpace(stem))]
	return ok
}
