package eval

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
)

// RunOptions điều khiển một lần chạy case.
type RunOptions struct {
	OutputDir string        // thư mục xuất cách ly (bắt buộc)
	Timeout   time.Duration // giới hạn wall-clock cho một case; 0 nghĩa là không giới hạn
	Progress  io.Writer     // xuất dòng tiến độ (tuỳ chọn, nil thì không in)
}

// RunCase điều khiển một lần chạy case: lắp host → khởi động → tiến tới giới hạn số chương →
// Abort đúng lúc. bundle đã được bên gọi ghi đè variant (nếu có). error trả về chính là "lỗi
// runtime" (căn cứ hard fail); viết xong bình thường hay dừng do chạm giới hạn đều trả nil.
//
// RunCase độc chiếm và đặt lại OutputDir: StartPrepared chỉ đặt lại progress/checkpoints, không
// dọn chapters/foundation v.v., tái dùng thư mục cũ sẽ để sản phẩm sót làm bẩn diag và
// novel_context. Vì thế xoá sạch trước khi chạy, bảo đảm cách ly.
func RunCase(cfg bootstrap.Config, bundle assets.Bundle, c Case, opts RunOptions) error {
	if strings.TrimSpace(opts.OutputDir) == "" {
		return fmt.Errorf("RunCase: thiếu OutputDir")
	}
	if err := os.RemoveAll(opts.OutputDir); err != nil {
		return fmt.Errorf("dọn thư mục xuất: %w", err)
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return fmt.Errorf("tạo thư mục xuất: %w", err)
	}
	cfg.OutputDir = opts.OutputDir
	if c.Style != "" {
		cfg.Style = c.Style
	}

	eng, err := host.New(cfg, bundle, host.WithFileLog("headless.log", false))
	if err != nil {
		return fmt.Errorf("lắp host: %w", err)
	}
	defer eng.Close()
	if logErr := eng.FileLogError(); logErr != nil {
		return fmt.Errorf("file log đánh giá không dùng được: %w", logErr)
	}

	prompt, err := startup.PrepareQuick(c.Prompt)
	if err != nil {
		return err
	}
	if err := eng.PrepareUserRules(prompt); err != nil {
		return fmt.Errorf("chuẩn bị quy tắc người dùng: %w", err)
	}
	if err := eng.StartPrepared(prompt); err != nil {
		return fmt.Errorf("khởi động: %w", err)
	}

	return drive(eng, c.MaxChapters, opts)
}

// driveEngine là giao diện engine tối thiểu mà drive tiêu thụ (*host.Host vốn thoả mãn).
// Tách ra để viết test xác định cho kỷ luật drain-to-Done — đoạn logic đồng thời này từng dính
// bẫy send-on-closed-channel.
type driveEngine interface {
	Events() <-chan host.Event
	Stream() <-chan string
	Done() <-chan struct{}
	Snapshot() host.UISnapshot
	Abort() bool
}

// drive tiêu thụ dòng sự kiện của engine, chạm giới hạn số chương hoặc quá thời hạn là Abort,
// chờ Done mới kết thúc.
//
// Kỷ luật then chốt: dù hoàn thành bình thường, dừng do chạm giới hạn số chương hay quá thời
// hạn, đều phải drain tới Done rồi mới trả về. waitDone chạy nền của host sẽ gửi vào done đúng
// một lần, còn eng.Close() (defer của RunCase) sẽ close(done) — trả về sớm làm Close kích hoạt
// sớm sẽ đua với lần gửi của waitDone trên kênh đang đóng mà panic (send on closed channel).
// headless cũng dựa vào "Done trước, Close sau". Đồng thời phải cạn Events và Stream, tránh
// chặn engine.
func drive(eng driveEngine, maxChapters int, opts RunOptions) error {
	var timeoutCh <-chan time.Time
	if opts.Timeout > 0 {
		t := time.NewTimer(opts.Timeout)
		defer t.Stop()
		timeoutCh = t.C
	}

	aborted, timedOut := false, false
	// finish được gọi sau khi drain tới Done (hoặc kênh bị đóng): quá thời hạn thì trả error,
	// không thì kết thúc bình thường.
	finish := func() error {
		if timedOut {
			return fmt.Errorf("chạy vượt thời hạn (%s)", opts.Timeout)
		}
		return nil
	}
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				return finish()
			}
			if opts.Progress != nil && strings.TrimSpace(ev.Summary) != "" {
				fmt.Fprintf(opts.Progress, "    [%s] %s\n", ev.Category, ev.Summary)
			}
			if !aborted && capReached(eng.Snapshot(), maxChapters) {
				eng.Abort()
				aborted = true
				timeoutCh = nil // đã đạt điều kiện dừng, chuyển sang kết thúc bình thường, không còn bị ràng buộc thời hạn (tránh nhầm dừng thành công là quá thời hạn)
			}
		case <-eng.Stream():
			// cạn phần tăng luồng streaming, không tiêu thụ nội dung — eval không quan tâm dòng
			// chính văn, chỉ nhìn sự thật ghi đĩa.
		case _, ok := <-eng.Done():
			if !ok {
				return finish()
			}
			return finish()
		case <-timeoutCh:
			eng.Abort() // aborted ở đây chắc chắn là false (dừng do cap sẽ đặt timeoutCh thành nil)
			aborted, timedOut = true, true
			timeoutCh = nil // vô hiệu hoá bộ đếm thời gian, tiếp tục drain tới Done, rồi finish trả lỗi quá thời hạn
		}
	}
}

// capReached phán đoán đã đạt điều kiện dừng chưa. maxChapters>0 tính theo số chương hoàn
// thành; <=0 coi là "loại hoạch định", hoạch định xong (vào writing hoặc đã complete) là dừng.
func capReached(snap host.UISnapshot, maxChapters int) bool {
	if maxChapters <= 0 {
		return snap.Phase == string(domain.PhaseWriting) || snap.Phase == string(domain.PhaseComplete)
	}
	return snap.CompletedCount >= maxChapters
}
