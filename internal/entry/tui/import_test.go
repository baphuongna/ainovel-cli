package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/voocel/ainovel-cli/internal/host/imp"
)

// TestImportHistoryCoalescesRetryLines canh giữ cập nhật tại chỗ của dòng retry: sự kiện
// liên tiếp cùng Key chỉ chiếm một dòng ("lần thứ N" nhảy trên một dòng), bị dòng tiến độ
// thường ngăn cách thì xuống dòng khác, giữ thứ tự thời gian.
func TestImportHistoryCoalescesRetryLines(t *testing.T) {
	s := newImportState(1, "book.txt", 100, 40, nil)
	base := len(s.history)
	retry := func(msg string) imp.Event {
		return imp.Event{Time: time.Now(), Stage: imp.StageSegmenting, Message: msg, Level: "warn", Key: "retry:segmenting"}
	}
	s.appendEvent(retry("thử lại sau 1s (lần 1)"), 80)
	s.appendEvent(retry("thử lại sau 2s (lần 2)"), 80)
	s.appendEvent(retry("thử lại sau 4s (lần 3)"), 80)
	if got := len(s.history) - base; got != 1 {
		t.Fatalf("retry liên tiếp cùng Key phải gộp thành 1 dòng, nhận %d", got)
	}
	if last := s.history[len(s.history)-1]; last.message != "thử lại sau 4s (lần 3)" {
		t.Fatalf("dòng gộp phải cập nhật thành message mới nhất, nhận %q", last.message)
	}
	// Sau khi bị dòng tiến độ thường ngăn cách, retry mới xuống dòng khác.
	s.appendEvent(imp.Event{Time: time.Now(), Stage: imp.StageAnalyzing, Message: "đang phân tích lô liên tiếp bắt đầu từ chương 1..."}, 80)
	s.appendEvent(retry("thử lại sau 1s (lần 1)"), 80)
	if got := len(s.history) - base; got != 3 {
		t.Fatalf("sau khi bị ngăn cách retry phải xuống dòng khác, tổng 3 dòng, nhận %d", got)
	}
}

// TestRenderImportLineWrapsWithoutClipping canh giữ chi tiết lỗi hiển thị đầy đủ: chính
// văn xuống dòng theo độ rộng còn lại sau khi trừ tiền tố, dòng tiếp thẳng hàng, không dòng
// nào được vượt contentW — viewport với dòng quá rộng là cắt cứng, HTTP status/provider/
// model trong lỗi chính là căn cứ truy vết, cắt cụt đồng nghĩa báo lỗi vô ích.
func TestRenderImportLineWrapsWithoutClipping(t *testing.T) {
	ln := importLine{
		at:      time.Now(),
		stage:   imp.StageSegmenting,
		message: "cắt vùng L1..L171",
		err: errors.New("imp: gọi model thất bại (tham số yêu cầu không hợp lệ, HTTP 400, openrouter, deepseek/deepseek-chat):" +
			"Provider returned error: invalid request payload with a very long gateway message tail"),
	}
	const contentW = 80
	out := renderImportLine(ln, contentW, time.Now())
	// Xuống dòng có thể ngắt ở bất kỳ ký tự nào, so sánh sau khi bỏ khoảng trắng, chỉ kiểm nội
	// dung không mất một chữ.
	norm := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == ' ' || r == '\n' {
				return -1
			}
			return r
		}, s)
	}
	for _, want := range []string{"HTTP 400", "openrouter", "gateway message tail"} {
		if !strings.Contains(norm(out), norm(want)) {
			t.Fatalf("nội dung dòng thiếu %q: %q", want, out)
		}
	}
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > contentW {
			t.Fatalf("dòng %d rộng %d vượt %d, sẽ bị viewport cắt cụt: %q", i, w, contentW, line)
		}
	}
	// Terminal hẹp: tiền tố (dấu thời gian + icon + tên giai đoạn dài) có thể chiếm quá nửa
	// độ rộng dòng, chính văn phải xuống dòng khác chứ không được nhồi vượt rộng theo mức dưới.
	ln.stage = imp.StageAwaitingConfirmation
	const narrowW = 40
	for i, line := range strings.Split(renderImportLine(ln, narrowW, time.Now()), "\n") {
		if w := lipgloss.Width(line); w > narrowW {
			t.Fatalf("terminal hẹp dòng %d rộng %d vượt %d: %q", i, w, narrowW, line)
		}
	}
}

// TestRenderImportLineMultilineBlock canh giữ bố cục của message khối nhiều dòng (xem
// trước xác nhận cắt): dòng tiếp thụt lề nông toàn khối (2 cột), không được thẳng hàng
// theo độ rộng tiền tố — tiền tố 40+ cột sẽ đẩy cả khối danh sách chương sang nửa phải
// bảng, nửa trái trống trơn.
func TestRenderImportLineMultilineBlock(t *testing.T) {
	ln := importLine{
		at:      time.Now(),
		stage:   imp.StageAwaitingConfirmation,
		current: 157, total: 157,
		message: "đã cắt 157 chương, hãy đối chiếu:\n  Chương 1 Lời dẫn\n  Chương 2 Tôi cố tình\n",
	}
	const contentW = 100
	out := strings.Split(renderImportLine(ln, contentW, time.Now()), "\n")
	if len(out) != 3 {
		t.Fatalf("phải là dòng tiền tố + 2 dòng chính văn, nhận %d dòng: %q", len(out), out)
	}
	for i, line := range out[1:] {
		if w := lipgloss.Width(line); w > contentW {
			t.Fatalf("dòng %d vượt rộng %d: %q", i+1, w, line)
		}
		if strings.HasPrefix(line, strings.Repeat(" ", 20)) {
			t.Fatalf("dòng tiếp của khối nhiều dòng không được thẳng hàng theo độ rộng tiền tố: %q", line)
		}
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("dòng tiếp của khối nhiều dòng phải thụt lề nông 2 cột: %q", line)
		}
	}
}

// TestWrapTextResetsAtNewlines canh giữ xuống dòng của message nhiều dòng: tại '\n'
// buộc phải reset bộ đếm độ rộng dòng, nếu không chỉ cần một dòng nào đó kích hoạt xuống
// dòng thì mọi dòng sau đều bị phán nhầm vượt rộng và chèn xuống dòng giả + thụt lề, cả
// bản xem trước xác nhận bị đánh tan.
func TestWrapTextResetsAtNewlines(t *testing.T) {
	in := strings.Repeat("宽", 30) + "\nDòng ngắn một\nDòng ngắn hai"
	out := wrapText(in, 20)
	for i, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w > 20 {
			t.Fatalf("dòng %d rộng %d vượt 20: %q", i, w, l)
		}
	}
	if !strings.Contains(out, "\nDòng ngắn một\nDòng ngắn hai") {
		t.Fatalf("dòng ngắn vốn có không được bị đánh tan: %q", out)
	}
}

// TestImportEscResumeGate canh giữ điểm rơi của phím Esc trên bảng import: import phát
// từ trang chào sau khi kết thúc thành công, đóng bảng phải chạy bù một lượt khôi phục
// (Resume của bootstrap chỉ chạy lúc khởi động), nếu không người dùng bị bỏ lại trang chào
// không có lối viết tiếp; trạng thái cuối lỗi và bối cảnh bàn làm việc chỉ đóng bảng; Esc
// khi đang chạy vẫn là hủy chứ không phải đóng.
func TestImportEscResumeGate(t *testing.T) {
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	// tea.Batch sau khi thực hiện trả về BatchMsg (lệnh con không được chạy), nhờ đó phân
	// biệt "tiêu điểm + khôi phục" với chỉ tiêu điểm.
	isBatch := func(cmd tea.Cmd) bool {
		_, ok := cmd().(tea.BatchMsg)
		return ok
	}
	newM := func(mode appMode, st *importState) Model {
		return Model{mode: mode, importer: st, textarea: textarea.New()}
	}

	m := newM(modeNew, &importState{done: true, stage: imp.StageDone})
	next, cmd := m.handleImportKey(esc)
	if next.(Model).importer != nil {
		t.Fatal("Esc ở trạng thái cuối phải đóng bảng")
	}
	if !isBatch(cmd) {
		t.Fatal("đóng bảng sau khi import thành công từ trang chào phải kèm lệnh khôi phục")
	}

	m = newM(modeNew, &importState{done: true, stage: imp.StageError, err: errors.New("boom")})
	if _, cmd := m.handleImportKey(esc); isBatch(cmd) {
		t.Fatal("trạng thái cuối lỗi không được kích hoạt khôi phục (sách có thể hoàn toàn chưa import thành công)")
	}

	m = newM(modeRunning, &importState{done: true, stage: imp.StageDone})
	if _, cmd := m.handleImportKey(esc); isBatch(cmd) {
		t.Fatal("bàn làm việc tự có cổng soát, không được kích hoạt khôi phục lặp")
	}

	canceled := false
	m = newM(modeNew, &importState{cancel: func() { canceled = true }})
	next, _ = m.handleImportKey(esc)
	if !canceled || next.(Model).importer == nil {
		t.Fatal("Esc khi đang chạy phải hủy import và giữ bảng chờ runner dọn dẹp")
	}
}

// TestRetryCountdown canh giữ hợp đồng render đếm ngược (bảng sự kiện và bảng import
// dùng chung): chưa đặt hạn hoặc đã đến hạn trả về rỗng (request đang trên đường); thời
// gian còn lại làm tròn lên tới giây, giảm dần từng giây và không xuất hiện 0s.
func TestRetryCountdown(t *testing.T) {
	now := time.Now()
	if got := retryCountdown(time.Time{}, now); got != "" {
		t.Fatalf("hạn bằng giá trị 0 phải trả về rỗng, nhận %q", got)
	}
	if got := retryCountdown(now.Add(-time.Second), now); got != "" {
		t.Fatalf("đã đến hạn phải trả về rỗng, nhận %q", got)
	}
	if got := retryCountdown(now.Add(7500*time.Millisecond), now); got != "Thử lại sau 8s" {
		t.Fatalf("7.5s phải làm tròn lên thành 8s, nhận %q", got)
	}
	if got := retryCountdown(now.Add(300*time.Millisecond), now); got != "Thử lại sau 1s" {
		t.Fatalf("chưa tới 1s phải hiển thị 1s, nhận %q", got)
	}
}

// TestParseImportArgsGuide canh giữ parse --guide: hướng dẫn ngôn ngữ tự nhiên có thể
// chứa khoảng trắng (mọi token sau đó gộp hết vào), có thể ghép với tùy chọn khác (đặt
// cuối cùng), nội dung rỗng báo lỗi.
func TestParseImportArgsGuide(t *testing.T) {
	opts, err := parseImportArgs([]string{"--guide=幕间·X", "也是", "独立章节"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Guidance != "幕间·X 也是 独立章节" {
		t.Fatalf("hướng dẫn chứa khoảng trắng phải gộp nguyên vẹn, nhận %q", opts.Guidance)
	}
	opts, err = parseImportArgs([]string{"book.txt", "--yes", "--guide=序章并入第一章"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.AutoConfirm || opts.SourcePath != "book.txt" || opts.Guidance != "序章并入第一章" {
		t.Fatalf("parse ghép với tùy chọn khác không khớp: %+v", opts)
	}
	if _, err := parseImportArgs([]string{"--guide="}); err == nil {
		t.Fatal("--guide rỗng phải báo lỗi")
	}
}
