package tui

import (
	"errors"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
)

func TestBootstrapExistingBookFailureStaysInWorkbench(t *testing.T) {
	m := Model{mode: modeNew, textarea: textarea.New()}
	next, cmd, handled := m.handleRuntimeMsg(bootstrapMsg{existing: true, err: errors.New("chuyển đổi thất bại")})
	if !handled || cmd == nil {
		t.Fatal("tác phẩm sẵn có khôi phục thất bại vẫn phải refresh bàn làm việc")
	}
	got := next.(Model)
	if got.mode != modeRunning {
		t.Fatalf("tác phẩm sẵn có khôi phục thất bại phải ở lại bàn làm việc, nhận mode=%v", got.mode)
	}
	if got.err == nil || got.err.Error() != "chuyển đổi thất bại" {
		t.Fatalf("bàn làm việc phải hiển thị lỗi gốc, nhận %v", got.err)
	}
}

// TestBootstrapCompletedBookLandsOnDoneWorkbench canh giữ điểm rơi khởi động của sách
// đã hoàn: resumeLabel với complete trả về nhãn rỗng, hành vi cũ rơi trang chào — trang
// chào với sách sẵn có không nhắc gì, người dùng sẽ tưởng sách mất, và vị trí tự nhiên của
// /reopen, /export, đầu vào viết lại đều ở bàn làm việc trạng thái hoàn tất.
func TestBootstrapCompletedBookLandsOnDoneWorkbench(t *testing.T) {
	m := Model{mode: modeNew, textarea: textarea.New()}
	next, cmd, handled := m.handleRuntimeMsg(bootstrapMsg{completed: true})
	if !handled || cmd == nil {
		t.Fatal("completed bootstrap phải được xử lý và trả về lệnh")
	}
	got := next.(Model)
	if got.mode != modeDone {
		t.Fatalf("sách đã hoàn phải rơi vào bàn làm việc trạng thái hoàn tất, nhận mode=%v", got.mode)
	}
	if got.textarea.Placeholder != donePlaceholder {
		t.Fatalf("phải đưa ra dẫn hướng trạng thái hoàn tất (gồm /reopen), nhận %q", got.textarea.Placeholder)
	}

	// Đã ở trong bàn làm việc (như sau khi hoàn tất trong phiên lại nhận bootstrap) không
	// được chuyển trạng thái lặp.
	m = Model{mode: modeRunning, textarea: textarea.New()}
	next, _, _ = m.handleRuntimeMsg(bootstrapMsg{completed: true})
	if next.(Model).mode != modeRunning {
		t.Fatal("trang không phải trang chào không được bị completed bootstrap chuyển trạng thái")
	}
}
