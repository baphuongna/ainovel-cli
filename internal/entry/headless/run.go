package headless

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/store"
)

type Options struct {
	Prompt string
	Stdout io.Writer
	Stderr io.Writer
}

// Run chạy nhân phiên ở chế độ không giao diện, tiêu thụ trực tiếp sự kiện Engine và
// luồng xuất. Nếu sau này bổ sung cách khởi động dùng chung như "tiếp tục viết tiểu
// thuyết đã có", không nên chất thẳng vào đây, mà phải hạ xuống internal/entry/startup
// trước, rồi headless gọi tới.
func Run(cfg bootstrap.Config, bundle assets.Bundle, opts Options) error {
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	eng, err := host.New(cfg, bundle, host.WithFileLog("headless.log", false))
	if err != nil {
		return err
	}
	// Khi chạy xong / trả về vì lỗi thì xuất một bản chẩn đoán đã làm mỏng thông tin,
	// thuận tiện cho người dùng headless dán vào issue. (Treo do bị kill từ bên ngoài
	// không đi qua defer, vẫn phải tự chạy /diag trong TUI.)
	// Chú ý thứ tự defer (LIFO): ở đây đăng ký diag trước, Close sau,
	// để eng.Close() (gồm đợt flush ghi đĩa cuối cùng) chạy trước khi xuất,
	// nếu không báo cáo chẩn đoán sẽ thiếu sự kiện kết thúc (review L4).
	defer func() {
		if _, err := diag.Export(store.NewStore(eng.Dir())); err != nil {
			fmt.Fprintf(stderr, "cảnh báo: xuất báo cáo chẩn đoán thất bại: %v\n", err)
		}
	}()
	defer eng.Close()
	if logErr := eng.FileLogError(); logErr != nil {
		fmt.Fprintf(stderr, "cảnh báo: nhật ký tệp không dùng được, tiếp tục dùng nhật ký terminal: %v\n", logErr)
	}

	prompt := strings.TrimSpace(opts.Prompt)
	if prompt != "" {
		prompt, err = startup.PrepareQuick(prompt)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "headless khởi động: %s\n", eng.Dir())
		// Bên khởi động sinh tất định ảnh snapshot luật người dùng cho sách này (chuẩn hóa
		// bằng prompt gốc), phải thực hiện trước StartPrepared.
		if err := eng.PrepareUserRules(prompt); err != nil {
			return err
		}
		if err := eng.StartPrepared(prompt); err != nil {
			return err
		}
	} else {
		items, err := eng.ReplayQueue(0)
		if err != nil {
			return err
		}
		replayQueue(items, stderr)
		label, err := eng.Resume()
		if err != nil {
			return err
		}
		if label == "" {
			return fmt.Errorf("chế độ headless cần --prompt, hoặc dưới thư mục xuất %q đã có phiên có thể khôi phục", eng.Dir())
		}
		fmt.Fprintf(stderr, "headless khôi phục: %s (%s)\n", eng.Dir(), label)
		return consume(eng, stdout, stderr, false)
	}

	return consume(eng, stdout, stderr, false)
}

func consume(eng *host.Host, stdout, stderr io.Writer, roundHasContent bool) error {
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				return nil
			}
			writeEvent(stderr, ev)
		case delta, ok := <-eng.Stream():
			if !ok {
				continue
			}
			if delta == host.StreamClearSentinel {
				if roundHasContent {
					if _, err := io.WriteString(stdout, "\n\n"); err != nil {
						return err
					}
					roundHasContent = false
				}
				continue
			}
			if delta == "" {
				continue
			}
			if _, err := io.WriteString(stdout, delta); err != nil {
				return err
			}
			roundHasContent = true
		case _, ok := <-eng.Done():
			if !ok {
				return nil
			}
			return drainPending(eng, stdout, stderr, roundHasContent)
		}
	}
}

func drainPending(eng *host.Host, stdout, stderr io.Writer, roundHasContent bool) error {
	for {
		select {
		case ev, ok := <-eng.Events():
			if ok {
				writeEvent(stderr, ev)
			}
		case delta, ok := <-eng.Stream():
			if !ok {
				continue
			}
			if delta == host.StreamClearSentinel {
				if roundHasContent {
					if _, err := io.WriteString(stdout, "\n\n"); err != nil {
						return err
					}
					roundHasContent = false
				}
				continue
			}
			if delta != "" {
				if _, err := io.WriteString(stdout, delta); err != nil {
					return err
				}
				roundHasContent = true
			}
		default:
			if roundHasContent {
				if _, err := io.WriteString(stdout, "\n"); err != nil {
					return err
				}
			}
			return nil
		}
	}
}

func writeEvent(w io.Writer, ev host.Event) {
	if w == nil || strings.TrimSpace(ev.Summary) == "" {
		return
	}
	ts := ev.Time.Format("15:04:05")
	if ts == "00:00:00" {
		ts = "--:--:--"
	}
	fmt.Fprintf(w, "[%s] [%s] %s\n", ts, ev.Category, ev.Summary)
}

func replayQueue(items []domain.RuntimeQueueItem, stderr io.Writer) {
	for _, item := range items {
		writeEvent(stderr, host.Event{
			Time:     item.Time,
			Category: item.Category,
			Summary:  item.Summary,
		})
	}
}
