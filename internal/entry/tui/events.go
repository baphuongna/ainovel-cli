package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Các loại message
type (
	eventMsg       host.Event
	snapshotMsg    host.UISnapshot
	doneMsg        struct{ complete bool } // complete=true toàn sách hoàn tất, false là dừng vì lỗi
	abortResultMsg struct{ stopped bool }
	bootstrapMsg   struct {
		existing  bool // đã có tác phẩm; dù khôi phục thành công hay không cũng nên vào bàn làm việc
		resumed   bool
		completed bool // trong thư mục là sách đã hoàn tất: vào bàn làm việc trạng thái hoàn tất chứ không phải trang chào
		err       error
	}
	reportLoadedMsg struct {
		reqID      int
		report     diag.Report
		exportPath string // đường dẫn tuyệt đối của file chẩn đoán đã khử nhạy; rỗng = xuất thất bại
		exportErr  error
		finishedAt time.Time
	}
	startResultMsg   struct{ err error }
	cocreateDeltaMsg struct {
		reqID int
		kind  string // host.CoCreateProgressThinking | host.CoCreateProgressReply
		text  string
	}
	// cocreateStreamItem là payload nội bộ của deltaCh, đưa kind stream và văn bản tích
	// lũy tới TUI cùng nhau.
	cocreateStreamItem struct {
		kind string
		text string
	}
	cocreateDoneMsg struct {
		reqID int
		reply host.CoCreateReply
		err   error
	}
	steerResultMsg     struct{ err error }
	continueResultMsg  struct{ err error }
	spinnerTickMsg     time.Time
	toolSpinnerTickMsg time.Time // tick riêng của spinner công cụ luồng sự kiện (nhanh hơn, độc lập với thanh trên/ngôi sao)
	streamDeltaMsg     string    // token delta của stream
	streamClearMsg     struct{}  // xóa đệm stream (bắt đầu message mới)
	streamFlushTickMsg struct{}  // tiết lưu refresh stream (chỉ lên lịch khi có dữ liệu chờ refresh)
	quitResetMsg       struct{}  // reset sau khi siêu thời xác nhận Ctrl+C hai lần
)

// --- Các hàm Cmd ---

func listenEvents(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-rt.Events()
		if !ok {
			return nil
		}
		return eventMsg(ev)
	}
}

func listenDone(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		_, ok := <-rt.Done()
		if !ok {
			return nil
		}
		snap := rt.Snapshot()
		return doneMsg{complete: snap.Phase == "complete"}
	}
}

func tickSnapshot(rt *host.Host) tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return snapshotMsg(rt.Snapshot())
	})
}

func fetchSnapshot(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		return snapshotMsg(rt.Snapshot())
	}
}

func bootstrapRuntime(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		snapshot := rt.Snapshot()
		msg := bootstrapMsg{
			existing:  snapshot.Phase != "" || snapshot.BookTitle != "",
			completed: snapshot.Phase == "complete",
		}
		label, err := rt.Resume()
		if err != nil {
			msg.err = err
			return msg
		}
		if label == "" {
			if msg.existing {
				return msg
			}
			return nil
		}
		msg.resumed = true
		return msg
	}
}

// resumeBook chạy bù một lượt cổng khôi phục trong phiên (Resume của bootstrap chỉ chạy
// một lần lúc khởi động): đóng bảng sau khi import xong, sau khi /reopen mở lại đều dựa
// vào nó để rơi về bàn sáng tác. Không phát lại hàng đợi sự kiện — sự kiện của phiên này
// đã được listenEvents thường trực hiển thị, phát lại sẽ hiển thị lặp. Can thiệp đang chờ
// xử lý (như hướng viết tiếp do /reopen đăng ký) do Resume đưa qua Arbiter phán định tiêu
// hóa trước, rồi mới tiếp tục chạy engine.
func resumeBook(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		snapshot := rt.Snapshot()
		label, err := rt.Resume()
		return bootstrapMsg{
			existing: snapshot.Phase != "" || snapshot.BookTitle != "", completed: snapshot.Phase == "complete",
			resumed: label != "", err: err,
		}
	}
}

func startRuntime(rt *host.Host, prompt string) tea.Cmd {
	return func() tea.Msg {
		// Bên khởi động sinh tất định snapshot quy tắc người dùng của sách (chuẩn hóa bằng
		// prompt gốc), phải làm trước StartPrepared.
		if err := rt.PrepareUserRules(prompt); err != nil {
			return startResultMsg{err: err}
		}
		err := rt.StartPrepared(prompt)
		return startResultMsg{err: err}
	}
}

func runCoCreate(rt *host.Host, state *cocreateState) tea.Cmd {
	history := state.session.History()
	// Phòng vào lại: nếu vòng trước vẫn đang chạy, trước tiên hủy ctx cũ, tránh goroutine
	// cũ rò (dòng cũ không thu hồi được) và về sau đọc sai channel của vòng mới.
	if state.cancel != nil {
		state.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	state.cancel = cancel
	// Closure chỉ tham chiếu channel cục bộ (đừng đọc state.deltaCh/doneCh nữa):
	// re-entry sẽ gán lại trường state, dòng cũ nếu bind muộn đọc sẽ đẩy kết quả vào/đóng
	// channel mới, gây panic send on closed channel (review M1).
	deltaCh := make(chan cocreateStreamItem, 64)
	doneCh := make(chan cocreateDoneMsg, 1)
	state.deltaCh = deltaCh
	state.doneCh = doneCh
	// Đồng sáng tạo theo giai đoạn mang tóm tắt trạng thái truyện, sản xuất "brief hướng
	// tiếp theo"; khởi động nguội làm rõ nhu cầu từ đầu. Hai bên chữ ký giống nhau.
	stream := rt.CoCreateStream
	if state.stage {
		stream = rt.StageCoCreateStream
	}
	start := func() tea.Msg {
		go func() {
			reply, err := stream(ctx, history, func(kind, text string) {
				select {
				case deltaCh <- cocreateStreamItem{kind: kind, text: text}:
				default:
				}
			})
			doneCh <- cocreateDoneMsg{reply: reply, err: err}
			close(deltaCh)
			close(doneCh)
		}()
		return nil
	}
	return tea.Batch(start, listenCoCreateDelta(state), listenCoCreateDone(state))
}

func listenCoCreateDelta(state *cocreateState) tea.Cmd {
	if state == nil || state.deltaCh == nil {
		return nil
	}
	// Nắm tham chiếu cục bộ channel: tránh khi state.deltaCh bị gán lại sau này thì
	// closure listen cũ đọc nhầm channel mới (dù quy trình hiện tại không kích hoạt,
	// để lại làm bẫy bảo trì thì không nên).
	reqID := state.reqID
	ch := state.deltaCh
	return func() tea.Msg {
		item, ok := <-ch
		if !ok {
			return nil
		}
		return cocreateDeltaMsg{reqID: reqID, kind: item.kind, text: item.text}
	}
}

func listenCoCreateDone(state *cocreateState) tea.Cmd {
	if state == nil || state.doneCh == nil {
		return nil
	}
	reqID := state.reqID
	ch := state.doneCh
	return func() tea.Msg {
		result, ok := <-ch
		if !ok {
			return nil
		}
		result.reqID = reqID
		return result
	}
}

func steerRuntime(rt *host.Host, text string) tea.Cmd {
	return func() tea.Msg {
		return steerResultMsg{err: rt.Steer(text)}
	}
}

func continueRuntime(rt *host.Host, text string) tea.Cmd {
	return func() tea.Msg {
		err := rt.Continue(text)
		return continueResultMsg{err: err}
	}
}

// resumeFromCoCreate tiêm brief hướng tiếp theo do đồng sáng tạo theo giai đoạn sản xuất
// và khôi phục sáng tác. Tái dùng continueResultMsg: thành công thì nối listenDone chạy
// tiếp, thất bại hiển thị lại lỗi.
func resumeFromCoCreate(rt *host.Host, draft string) tea.Cmd {
	return func() tea.Msg {
		err := rt.ResumeFromCoCreate(draft)
		return continueResultMsg{err: err}
	}
}

// cancelCoCreate bỏ đồng sáng tạo theo giai đoạn: xóa cờ chiếm chỗ, giữ tạm dừng.
// Sự kiện theo kênh events chảy về, không cần trả message.
func cancelCoCreate(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		rt.CancelCoCreate()
		return nil
	}
}

func abortRuntime(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		return abortResultMsg{stopped: rt.Abort()}
	}
}

func loadReport(dir string, reqID int) tea.Cmd {
	return func() tea.Msg {
		s := store.NewStore(dir)
		// Diagnose = chẩn đoán sáng tác + kiểm tra runtime, Finding của runtime cũng vào
		// báo cáo trên màn hình.
		rep, rc := diag.Diagnose(s)
		// Tái dùng rep+rc ghi file chẩn đoán đã khử nhạy (xuất thất bại không ảnh hưởng báo
		// cáo trên màn hình).
		exportPath, exportErr := diag.WriteExport(s, rep, rc)
		return reportLoadedMsg{
			reqID:      reqID,
			report:     rep,
			exportPath: exportPath,
			exportErr:  exportErr,
			finishedAt: time.Now(),
		}
	}
}

func tickSpinner() tea.Cmd {
	return tea.Tick(350*time.Millisecond, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
}

// tickToolSpinner chạy spinner của dòng "đang chạy" trong luồng sự kiện. Độc lập với
// tickSpinner, nhịp nhanh hơn (150ms).
func tickToolSpinner() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
		return toolSpinnerTickMsg(t)
	})
}

// tickStreamFlush gộp delta stream trong một cửa sổ 16ms. Nó do delta đầu tiên chờ
// refresh khởi động, refresh xong là dừng, lúc rảnh không liên tục đánh thức TUI.
func tickStreamFlush() tea.Cmd {
	return tea.Tick(16*time.Millisecond, func(t time.Time) tea.Msg {
		return streamFlushTickMsg{}
	})
}

func listenStream(rt *host.Host) tea.Cmd {
	return func() tea.Msg {
		delta, ok := <-rt.Stream()
		if !ok {
			return nil
		}
		// sentinel được phát đi thành streamClearMsg, bảo đảm đến TUI theo thứ tự emit
		// trong cùng một kênh với delta bình thường. Hai kênh thì clearCh và streamCh không
		// có thứ tự, header ✻ thường bị nhét nhầm xuống cuối đoạn thinking trước đó.
		if delta == host.StreamClearSentinel {
			return streamClearMsg{}
		}
		return streamDeltaMsg(delta)
	}
}
