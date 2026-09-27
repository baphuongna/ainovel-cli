package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/utils"
)

const maxEvents = 500

// maxStreamRounds giới hạn số vòng mà bảng stream giữ lại. Mỗi LLM call kết thúc kích hoạt
// một streamClear mở vòng mới, một chương writer khoảng 3~5 vòng (agent header / suy nghĩ /
// draft / commit), 32 vòng tương đương xem lại stream của 6~10 chương gần nhất. Chính văn
// chương đã commit nằm trong store/drafts, vượt giới hạn thì bỏ đi để tránh mỗi token delta
// kích hoạt render lại O(toàn văn). Bộ nhớ ổn định tối đa khoảng 512KB, thấp xa ngưỡng giật.
const maxStreamRounds = 32

type focusPane int

const (
	focusEvents focusPane = iota
	focusStream
	focusDetail
	focusState // thanh trạng thái bên trái (cuộn được)

	focusPaneCount // tổng số pane, dùng cho vòng quay Tab
)

type appMode int

const (
	modeNew     appMode = iota // chờ người dùng nhập nhu cầu tiểu thuyết
	modeRunning                // đang sáng tác (kể cả dừng vì lỗi, nhập liệu có thể khôi phục)
	modeDone                   // sáng tác hoàn tất
)

// Chuỗi khung spinner dùng chung cho thanh trên / hoạt động stream (bubbles.Spinner.MiniDot).
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Chuỗi khung spinner riêng cho dòng "đang chạy" của luồng sự kiện (bubbles.Spinner.Dot).
// 7 chấm + 1 khuyết xoay theo chiều kim đồng hồ trên lưới 3×3, nhìn như vòng tròn loading
// hoàn chỉnh. Dùng chỉ số khung riêng + tick nhanh hơn, không ảnh hưởng nhịp của thanh trên
// và hoạt ảnh ngôi sao.
var toolSpinnerFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// Model là trạng thái tầng cao nhất của TUI.
type Model struct {
	runtime        *host.Host
	cocreate       *cocreateState
	help           *helpState
	modelSwitch    *modelSwitchState
	modelConfig    *modelConfigState
	report         *reportState
	version        string
	importer       *importState
	importSeq      int
	simulator      *simulationState
	simSeq         int
	compItems      []commandPaletteItem
	compIdx        int
	compActive     bool
	commandToken   string // token lệnh đã đăng ký hiện tại; chỉ render đoạn đó, không tô tham số
	snapshot       host.UISnapshot
	events         []host.Event
	eventIndex     map[string]int   // event.ID → chỉ số trong m.events; sự kiện dạng gọi đến thì cập nhật tại chỗ
	viewport       viewport.Model   // viewport luồng sự kiện
	streamVP       viewport.Model   // viewport stream
	detailVP       viewport.Model   // viewport chi tiết bên phải
	stateVP        viewport.Model   // viewport thanh trạng thái bên trái (cuộn được)
	streamBuf      *strings.Builder // đệm tích lũy văn bản stream
	streamRounds   []string
	textarea       textarea.Model
	width          int
	height         int
	autoScroll     bool
	streamScroll   bool      // tự động theo dõi bảng stream
	streamDirty    bool      // streamRounds còn delta chưa refresh
	flushPending   bool      // đã lên lịch một lần refresh stream, tránh mỗi delta lại khởi động timer
	lastKeyAt      time.Time // thời điểm phím khác Enter gần nhất; tiết lưu KeyEnter tránh dòng \n do paste lỗi kích hoạt nộp
	inputHistory   []string  // lịch sử đầu vào đã nộp (khử trùng: kề nhau không lặp)
	historyIdx     int       // chỉ số đang duyệt; == len(inputHistory) nghĩa là "chưa duyệt, đang soạn thảo"
	historyDraft   string    // bản thảo lưu trước khi vào duyệt lịch sử, khôi phục khi về cuối
	focusPane      focusPane
	hoverPane      focusPane
	hoverActive    bool
	mode           appMode
	starting       bool // UI đã vào bàn làm việc, Host đang chạy khởi tạo khởi động
	startupMode    startupMode
	importHint     string // gợi ý phát hiện import chưa hoàn thành lúc khởi động (hiện ở màn chào; xóa sau khi phát import)
	cocreateSeq    int
	reportSeq      int
	err            error
	spinnerIdx     int
	toolSpinnerIdx int  // chỉ số khung riêng của dòng đang chạy trong luồng sự kiện (tick 150ms, không ảnh hưởng thanh trên/ngôi sao)
	toolTicking    bool // đã khởi động timer hoạt ảnh công cụ; tự dừng khi không còn sự kiện chạy
	cursorIdx      int  // chỉ số khung con trỏ stream (tiến theo hoạt ảnh chính)
	streamRound    int  // bộ đếm vòng stream
	quitPending    bool // xác nhận thoát bằng Ctrl+C hai lần
	abortPending   bool // chờ Done quay về sau khi tạm dừng thủ công
	mouseOff       bool // true là đã tắt báo chuột, cho người dùng kéo chọn copy gốc; bật lại khi chuyển lần nữa
}

// NewModel dựng TUI Model.
func NewModel(rt *host.Host, version string) Model {
	ta := textarea.New()
	ta.Placeholder = placeholderForNewMode(startupModeQuick)
	ta.CharLimit = 5000
	ta.SetHeight(1)
	// MaxHeight=6 cho đầu vào siêu dài tự động wrap theo chiều rộng thành nhiều dòng (giới hạn trực quan 6 dòng).
	ta.MaxHeight = 6
	ta.ShowLineNumbers = false
	ta.Focus()

	// Mặc định Enter không xuống dòng (do handleEnterKey nộp);
	// xuống dòng chủ động gán lại ctrl+j (\n kiểu unix) và alt+enter (thói quen GUI).
	// Tầng giao thức terminal không phân biệt được Shift+Enter với Enter nên không hỗ trợ Shift+Enter.
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j", "alt+enter")

	vp := viewport.New(80, 20)
	vp.SetContent("")

	svp := viewport.New(80, 10)
	svp.SetContent("")

	dvp := viewport.New(40, 20)
	dvp.SetContent("")

	stvp := viewport.New(32, 20)
	stvp.SetContent("")

	// Kiểm tra một lần import chưa hoàn thành lúc khởi động (LoadState tính lại digest công cụ,
	// không vào vòng thăm dò snapshot); sách dở giữa nếu không chủ động báo, người dùng chỉ biết
	// khi sáng tác bị cổng chặn từ chối (RFC §18.2).
	importHint := ""
	if rt != nil {
		importHint = rt.ImportResumeHint()
	}

	return Model{
		runtime:      rt,
		version:      strings.TrimSpace(version),
		autoScroll:   true,
		streamScroll: true,
		mode:         modeNew,
		startupMode:  startupModeQuick,
		importHint:   importHint,
		textarea:     ta,
		viewport:     vp,
		streamVP:     svp,
		detailVP:     dvp,
		stateVP:      stvp,
		streamBuf:    &strings.Builder{},
		eventIndex:   make(map[string]int),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		listenEvents(m.runtime),
		listenDone(m.runtime),
		listenStream(m.runtime),
		tickSnapshot(m.runtime),
		bootstrapRuntime(m.runtime),
		tickSpinner(),
	)
}

func (m *Model) paneAtMouse(x, y int) (focusPane, bool) {
	if m.width == 0 || m.height == 0 {
		return focusEvents, false
	}

	topH, _, bodyH := m.layoutHeights()
	if bodyH < 1 {
		return focusEvents, false
	}

	bodyStartY := topH
	bodyEndY := topH + bodyH
	if y < bodyStartY || y >= bodyEndY {
		return focusEvents, false
	}

	leftW := m.sidebarWidth()
	rightW := m.detailWidth()
	centerStartX := leftW
	rightStartX := m.width - rightW

	if x >= rightStartX {
		return focusDetail, true
	}
	if x < centerStartX {
		return focusState, true
	}

	eventH, _ := m.splitHeights(bodyH)
	if y-bodyStartY < eventH {
		return focusEvents, true
	}
	return focusStream, true
}

func (m *Model) paneHighlighted(pane focusPane) bool {
	if m.focusPane == pane {
		return true
	}
	return m.hoverActive && m.hoverPane == pane
}

// hasRunningEvent có tồn tại sự kiện dạng gọi chưa hoàn thành (spinner vẫn quay) không.
// toolSpinnerTick dùng nó phán có đáng render lại không: không có sự kiện running thì khung
// spinner không ảnh hưởng đầu ra, cả refreshEventViewport là công việc vô ích chắc chắn.
func (m *Model) hasRunningEvent() bool {
	for i := range m.events {
		if m.events[i].Running() {
			return true
		}
	}
	return false
}

// flushStreamIfDirty render streamRounds tích lũy vào viewport; đánh dấu đã refresh.
// Trả về có thực sự refresh không, để bên gọi quyết định có cần GotoBottom.
func (m *Model) flushStreamIfDirty() bool {
	if !m.streamDirty {
		return false
	}
	m.refreshStreamViewport()
	m.streamDirty = false
	return true
}

// refreshEventViewport render lại nội dung luồng sự kiện và đặt viewport.
func (m *Model) refreshEventViewport() {
	centerW := m.eventFlowWidth()
	content := renderEventContent(m.events, centerW, m.toolSpinnerIdx)
	snap := m.snapshot
	if m.starting {
		snap.IsRunning = true
	}
	if activity := renderEventActivity(snap, m.spinnerIdx, centerW); activity != "" {
		if strings.TrimSpace(content) != "" {
			content += "\n" + activity
		} else {
			content = activity
		}
	}
	m.viewport.SetContent(content)
	if m.autoScroll {
		m.viewport.GotoBottom()
	}
}

func (m *Model) refreshStreamViewport() {
	cursor := ""
	if m.snapshot.IsRunning {
		cursor = renderStreamCursor(m.cursorIdx)
	}
	m.streamVP.SetContent(renderStreamContent(m.streamRounds, m.streamVP.Width, cursor))
}

func (m *Model) refreshDetailViewport() {
	rightW := m.detailWidth()
	if rightW <= 4 {
		return
	}
	m.detailVP.SetContent(renderDetailContent(m.snapshot, rightW-4))
}

// refreshStateViewport đưa nội dung thanh trạng thái bên trái vào viewport.
// Nội dung thanh phụ thuần suy ra từ snapshot nên khi snapshot hay kích thước đổi đều phải refresh.
func (m *Model) refreshStateViewport() {
	leftW := m.sidebarWidth()
	if leftW <= 4 {
		return
	}
	m.stateVP.SetContent(renderStateContent(m.snapshot, leftW-4))
}

// updateViewportSize cập nhật kích thước viewport theo cửa sổ hiện tại.
func (m *Model) updateViewportSize() {
	centerW := m.eventFlowWidth()
	rightW := m.detailWidth()
	bodyH := m.bodyHeight()
	eventH, streamH := m.splitHeights(bodyH)
	m.viewport.Width = centerW - 2
	m.viewport.Height = eventH - 1 // -1 cho dòng header bảng event
	m.streamVP.Width = centerW - 2
	m.streamVP.Height = streamH - 1 // -1 cho dòng header bảng stream
	m.detailVP.Width = rightW - 2
	m.detailVP.Height = bodyH
	leftW := m.sidebarWidth()
	m.stateVP.Width = max(1, leftW-2)
	m.stateVP.Height = max(1, bodyH-1) // -1 chừa khoảng trống trên, dòng cuối hiển thị nội dung trực tiếp
	// Sau khi chiều cao hay nội dung ngắn lại, hai cột trái phải cuộn tự do có thể dừng ở lệch
	// vượt giới (SetContent của bubbles chỉ chặn vượt dòng cuối), viewport sẽ dùng dòng trống
	// lấp đầy đáy. SetYOffset tự kẹp lại.
	m.stateVP.SetYOffset(m.stateVP.YOffset)
	m.detailVP.SetYOffset(m.detailVP.YOffset)
}

// splitHeights tính phân bổ chiều cao giữa luồng sự kiện và stream.
func (m *Model) splitHeights(bodyH int) (eventH, streamH int) {
	eventH = bodyH * 40 / 100
	if eventH < 3 {
		eventH = 3
	}
	streamH = bodyH - eventH - 1 // -1 cho đường phân cách
	if streamH < 3 {
		streamH = 3
	}
	return
}

func (m *Model) inputWidth() int {
	if m.width == 0 {
		return 60
	}
	return m.width - 6 // viền + padding + dấu nhắc "❯ "
}

func (m *Model) currentInputWidth() int {
	if m.cocreate != nil {
		return coCreateInputWidth(m.width, m.height)
	}
	return m.inputWidth()
}

// refitTextareaHeight ước lượng số dòng trực quan theo nội dung hiện tại, SetHeight động.
// Dòng trực quan = tổng sau khi wrap từng đoạn của dòng logic (tách bằng \n) theo chiều rộng.
// Kết hợp MaxHeight=6 hiện thực "nội dung siêu dài/xuống dòng chủ động tự hiển thị nhiều dòng,
// tối đa 6 dòng".
func (m *Model) refitTextareaHeight() {
	w := m.textarea.Width()
	if w <= 0 {
		return
	}
	// Ở chế độ đồng sáng tạo input cố định 1 dòng: nội dung nhiều dòng của textarea sẽ do chính
	// textarea cuộn theo con trỏ hiển thị. Nếu không chiều cao inputBox đổi theo nội
	// dung, sẽ làm conversation cột trái co lại, input trôi theo phương đứng, phá
	// ổn định bố cục.
	if m.cocreate != nil {
		m.textarea.SetHeight(1)
		return
	}
	text := m.textarea.Value()
	if text == "" {
		m.textarea.SetHeight(1)
		return
	}
	// Trừ 2 cột dư (ký hiệu prompt + con trỏ bên trong textarea), dư 1 dòng vẫn chấp nhận được.
	contentW := w - 2
	if contentW < 1 {
		contentW = 1
	}
	total := 0
	for line := range strings.SplitSeq(text, "\n") {
		lw := lipgloss.Width(line)
		if lw == 0 {
			total++
			continue
		}
		total += (lw + contentW - 1) / contentW
	}
	if total < 1 {
		total = 1
	}
	m.textarea.SetHeight(total) // SetHeight bên trong kẹp theo MaxHeight
}

// resizeTextarea đồng bộ đặt chiều rộng và chiều cao dựa trên nội dung.
// Thay cho các lệnh SetWidth(currentInputWidth()) rải rác, bảo đảm khi chiều rộng
// đổi thì chiều cao theo kịp.
func (m *Model) resizeTextarea() {
	m.textarea.SetWidth(m.currentInputWidth())
	m.refitTextareaHeight()
}

// maxInputHistory giới hạn chiều dài lịch sử, tránh bộ nhớ phình ra trong phiên dài.
const maxInputHistory = 200

// pushInputHistory nối nội dung nộp thành công vào lịch sử, khử trùng kề nhau.
// Đồng thời reset chỉ số duyệt.
func (m *Model) pushInputHistory(text string) {
	if text == "" {
		return
	}
	if n := len(m.inputHistory); n == 0 || m.inputHistory[n-1] != text {
		m.inputHistory = append(m.inputHistory, text)
		if len(m.inputHistory) > maxInputHistory {
			m.inputHistory = m.inputHistory[len(m.inputHistory)-maxInputHistory:]
		}
	}
	m.historyIdx = len(m.inputHistory)
	m.historyDraft = ""
}

// tryHistoryUp đi về một lịch sử cũ hơn; trả về có xử lý phím không.
// Lần đầu vào duyệt lịch sử thì lưu nội dung textarea hiện tại làm draft, khôi phục khi
// về cuối. Bên gọi tự phán trong tình huống nhiều dòng có nên né hay không (để textarea
// xử lý di chuyển con trỏ trong dòng).
func (m *Model) tryHistoryUp() bool {
	if len(m.inputHistory) == 0 || m.historyIdx <= 0 {
		return false
	}
	if m.historyIdx == len(m.inputHistory) {
		m.historyDraft = m.textarea.Value()
	}
	m.historyIdx--
	m.textarea.SetValue(m.inputHistory[m.historyIdx])
	m.textarea.CursorEnd()
	m.syncCommandInputHighlight()
	m.refitTextareaHeight()
	return true
}

// tryHistoryDown đi về một lịch sử mới hơn; đến cuối thì khôi phục draft.
func (m *Model) tryHistoryDown() bool {
	if m.historyIdx >= len(m.inputHistory) {
		return false
	}
	m.historyIdx++
	if m.historyIdx == len(m.inputHistory) {
		m.textarea.SetValue(m.historyDraft)
		m.historyDraft = ""
	} else {
		m.textarea.SetValue(m.inputHistory[m.historyIdx])
	}
	m.textarea.CursorEnd()
	m.syncCommandInputHighlight()
	m.refitTextareaHeight()
	return true
}

// textareaIsMultiline nội dung textarea hiện tại có chứa xuống dòng chủ động không;
// dùng quyết định ↑↓ là đi lịch sử hay di chuyển trong dòng.
func (m *Model) textareaIsMultiline() bool {
	return strings.Contains(m.textarea.Value(), "\n")
}

// inputHints sinh văn bản gợi ý đáy theo trạng thái hiện tại.
// Cuối luôn nối thêm copySuffix, cho người dùng ở mọi trạng thái không khẩn cấp đều thấy
// cách chọn copy; khi chuột đã tắt thì hiện gợi ý đỏ nổi bật, nhắc nhấn lần nữa để khôi
// phục tương tác chuột.
func (m *Model) inputHints() string {
	dimStyle := lipgloss.NewStyle().Foreground(colorDim)
	if m.quitPending {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Bold(true).Render("Nhấn Ctrl+C một lần nữa để thoát")
	}
	limitHint := m.inputLimitHint()
	suffix := limitHint + " · Ctrl+R Chế độ copy"
	if m.mode == modeNew {
		suffix = limitHint
	}
	if m.mouseOff && m.mode != modeNew {
		return lipgloss.NewStyle().Foreground(colorAccent).Bold(true).
			Render("✂ Chế độ bôi đen copy: Kéo chuột chọn văn bản để sao chép · Ctrl+R Quay lại")
	}
	if m.cocreate != nil {
		scrollHint := " · Tab Cuộn: Hội thoại"
		if m.cocreate.focusPrompt {
			scrollHint = " · Tab Cuộn: Chỉ thị"
		}
		switch {
		case m.cocreate.awaiting:
			return dimStyle.Render("Chờ AI phản hồi · Esc Thoát đồng sáng tác" + scrollHint + suffix)
		case m.cocreate.canStart():
			startLabel := "Ctrl+S Bắt đầu sáng tác"
			if m.cocreate.stage {
				startLabel = "Ctrl+S Áp dụng & Tiếp tục"
			}
			return dimStyle.Render("Enter Gửi · " + startLabel + " · Esc Thoát" + scrollHint + suffix)
		default:
			return dimStyle.Render("Enter Gửi · Esc Thoát đồng sáng tác" + scrollHint + suffix)
		}
	}
	if m.mode == modeNew {
		if m.startupMode == startupModeQuick {
			return dimStyle.Render("Tab Đổi chế độ · Gõ / để xem lệnh · Enter Bắt đầu viết ngay · Esc Xóa" + suffix)
		}
		return dimStyle.Render("Tab Đổi chế độ · Gõ / để xem lệnh · Enter Bắt đầu đồng sáng tác · Esc Xóa" + suffix)
	}
	switch m.snapshot.RuntimeState {
	case "pausing":
		return dimStyle.Render("Đang tạm dừng sáng tác · Vui lòng chờ vòng hiện tại kết thúc" + suffix)
	case "paused":
		return dimStyle.Render("Gõ / để xem lệnh · Enter Tiếp tục sáng tác · Esc Xóa" + suffix)
	}
	return dimStyle.Render("Gõ / để xem lệnh · Click/Tab Đổi panel · ↑↓ Cuộn · End Về cuối · Ctrl+L Xóa màn hình · Esc Tạm dừng · Enter Gửi" + suffix)
}

func (m *Model) inputLimitHint() string {
	limit := m.textarea.CharLimit
	if limit <= 0 {
		return ""
	}
	used := m.textarea.Length()
	if used < limit*4/5 {
		return ""
	}
	return fmt.Sprintf(" · Đã nhập %d/%d", used, limit)
}

func (m *Model) eventFlowWidth() int {
	if m.width == 0 {
		return 80
	}
	leftW := m.sidebarWidth()
	rightW := m.detailWidth()
	return m.width - leftW - rightW
}

func (m *Model) sidebarWidth() int {
	if m.width == 0 {
		return 32
	}
	return m.width * 23 / 100
}

func (m *Model) detailWidth() int {
	if m.width == 0 {
		return 40
	}
	return m.width * 27 / 100
}

func (m *Model) bodyHeight() int {
	_, _, bodyH := m.layoutHeights()
	return bodyH
}

func (m *Model) currentSpinnerFrame() string {
	if !m.snapshot.IsRunning && !m.starting {
		return ""
	}
	return spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
}

func (m *Model) outputDir() string {
	if m.runtime == nil {
		return ""
	}
	return m.runtime.Dir()
}

func defaultSteerPlaceholder() string {
	return "Nhập can thiệp cốt truyện, ví dụ: đẩy tuyến tình cảm lên chương 4"
}

func (m *Model) syncRuntimePlaceholder() {
	if m.mode != modeRunning || m.cocreate != nil {
		return
	}
	if m.starting {
		m.textarea.Placeholder = "Đang khởi tạo sáng tác..."
		return
	}
	switch m.snapshot.RuntimeState {
	case "completed":
		m.textarea.Placeholder = donePlaceholder
	case "pausing":
		m.textarea.Placeholder = "Đang tạm dừng sáng tác..."
	case "paused":
		if m.snapshot.AdvanceMode == "review" && m.snapshot.Phase == "writing" {
			m.textarea.Placeholder = "Chờ nghiệm thu: Nhập ý kiến chỉnh sửa, hoặc /next để duyệt chương tiếp"
		} else {
			m.textarea.Placeholder = "Sáng tác đã tạm dừng, nhập nội dung bất kỳ để tiếp tục"
		}
	default:
		if !m.snapshot.IsRunning {
			if m.snapshot.AdvanceMode == "review" && m.snapshot.Phase == "writing" {
				m.textarea.Placeholder = "Chờ nghiệm thu: Nhập ý kiến chỉnh sửa, hoặc /next để duyệt chương tiếp"
			} else {
				m.textarea.Placeholder = "Sáng tác bị gián đoạn, nhập nội dung bất kỳ để tiếp tục"
			}
		} else {
			m.textarea.Placeholder = defaultSteerPlaceholder()
		}
	}
}

func (m *Model) renderBottomBar() string {
	inputView := highlightCommandToken(m.textarea.View(), m.textarea.Value(), m.commandToken)
	inputBox := renderInputBox(
		inputView,
		m.inputHints(),
		m.snapshot,
		m.outputDir(),
		m.width,
	)
	if m.mode != modeNew || m.cocreate != nil {
		return inputBox
	}
	return renderStartupModeBar(m.width, m.startupMode) + "\n" + inputBox
}

func (m *Model) layoutHeights() (topH, inputH, bodyH int) {
	if m.width == 0 || m.height == 0 {
		return 1, 4, 20
	}
	topH = lipgloss.Height(renderTopBar(m.snapshot, m.width, m.currentSpinnerFrame(), m.version))
	inputH = lipgloss.Height(m.renderBottomBar())
	bodyH = m.height - topH - inputH
	if bodyH < 3 {
		bodyH = 3
	}
	return
}

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Đang tải..."
	}
	if m.width < 100 {
		return lipgloss.NewStyle().
			Width(m.width).Height(m.height).
			AlignHorizontal(lipgloss.Center).
			AlignVertical(lipgloss.Center).
			Render("Độ rộng terminal quá nhỏ, vui lòng mở rộng tối thiểu 100 cột")
	}
	if m.cocreate != nil {
		return renderCoCreateModal(m.width, m.height, m.cocreate, errorText(m.err), m.textarea.View(), m.spinnerIdx, m.quitPending)
	}
	if m.help != nil {
		return renderHelpModal(m.width, m.height, m.help)
	}
	if m.report != nil {
		return renderReportModal(m.width, m.height, m.report)
	}
	if m.importer != nil {
		// Import không phụ thuộc trạng thái chạy của Engine, khung hoạt ảnh lấy thẳng
		// spinnerIdx (currentSpinnerFrame trả về rỗng khi engine dừng).
		return renderImportModal(m.width, m.height, m.importer, m.spinnerIdx)
	}
	if m.simulator != nil {
		return renderSimulationModal(m.width, m.height, m.simulator)
	}

	topBar := renderTopBar(m.snapshot, m.width, m.currentSpinnerFrame(), m.version)
	inputBox := m.renderBottomBar()
	_, inputH, bodyH := m.layoutHeights()

	var body string
	if m.mode == modeNew {
		errMsg := ""
		if m.err != nil {
			errMsg = m.err.Error()
		}
		body = renderWelcome(m.width, bodyH, errMsg, m.startupMode, m.importHint)
	} else {
		leftW := m.sidebarWidth()
		rightW := m.detailWidth()
		centerW := m.width - leftW - rightW
		eventH, streamH := m.splitHeights(bodyH)

		if m.viewport.Width != centerW-2 || m.viewport.Height != eventH-1 {
			m.viewport.Width = centerW - 2
			m.viewport.Height = eventH - 1 // -1 cho dòng header bảng event
		}
		if m.streamVP.Width != centerW-2 || m.streamVP.Height != streamH-1 {
			m.streamVP.Width = centerW - 2
			m.streamVP.Height = streamH - 1 // -1 cho dòng header bảng stream
		}

		eventFlow := renderEventFlowViewport(m.viewport, centerW, eventH, m.paneHighlighted(focusEvents))
		streamPanel := renderStreamPanel(m.streamVP, centerW, streamH, m.paneHighlighted(focusStream), m.snapshot.IsRunning || m.starting, m.spinnerIdx)
		center := lipgloss.JoinVertical(lipgloss.Left, eventFlow, streamPanel)

		left := renderStatePanel(m.stateVP, leftW, bodyH, m.paneHighlighted(focusState))
		right := renderDetailPanel(m.detailVP, rightW, bodyH, m.paneHighlighted(focusDetail))
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, center, right)
	}

	view := lipgloss.JoinVertical(lipgloss.Left, topBar, body, inputBox)

	// Lớp phủ modal: nổi phía trên đáy body, không ảnh hưởng bố cục
	if m.modelSwitch != nil {
		commandBar := renderModelSwitchBar(m.width, m.modelSwitch)
		view = overlayAboveInput(view, commandBar, inputH)
	} else if m.modelConfig != nil {
		view = overlayAboveInput(view, renderModelConfigModal(m.width, m.modelConfig), inputH)
	} else if m.compActive {
		commandBar := renderCommandPalette(m.width, m.compItems, m.compIdx)
		view = overlayAboveInput(view, commandBar, inputH)
	}
	return view
}

// sendCoCreate phát một vòng yêu cầu đồng sáng tạo, xử lý thống nhất reqID, textarea, placeholder.
func (m *Model) sendCoCreate() tea.Cmd {
	m.cocreateSeq++
	m.cocreate.reqID = m.cocreateSeq
	m.cocreate.awaiting = true
	m.resizeTextarea()
	m.textarea.Placeholder = placeholderForCoCreate(m.cocreate)
	m.textarea.Blur()
	return runCoCreate(m.runtime, m.cocreate)
}

func (m Model) handleCoCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.cocreate == nil {
		return m, nil
	}
	state := m.cocreate

	// Phím ↑↓/PgUp/PgDn/Home/End cuộn; Tab chuyển tiêu điểm cuộn giữa cột hội thoại trái
	// ↔ cột lệnh sáng tác phải (mặc định cột trái, người dùng xem lại phần chính). Trang chào
	// đã tắt báo chuột để giữ copy gốc, cột phải tràn thì nhờ Tab chuyển tiêu điểm rồi cuộn
	// bằng bàn phím. Cột trái: cuộn lên tắt follow, cuộn tới đáy bật lại follow (theo stream).
	switch msg.Type {
	case tea.KeyTab:
		state.focusPrompt = !state.focusPrompt
		return m, nil
	case tea.KeyUp, tea.KeyPgUp:
		if state.focusPrompt {
			var cmd tea.Cmd
			state.promptVP, cmd = state.promptVP.Update(msg)
			return m, cmd
		}
		state.convFollow = false
		var cmd tea.Cmd
		state.convVP, cmd = state.convVP.Update(msg)
		return m, cmd
	case tea.KeyDown, tea.KeyPgDown:
		if state.focusPrompt {
			var cmd tea.Cmd
			state.promptVP, cmd = state.promptVP.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		state.convVP, cmd = state.convVP.Update(msg)
		if state.convVP.AtBottom() {
			state.convFollow = true
		}
		return m, cmd
	case tea.KeyHome:
		if state.focusPrompt {
			state.promptVP.GotoTop()
			return m, nil
		}
		state.convFollow = false
		state.convVP.GotoTop()
		return m, nil
	case tea.KeyEnd:
		if state.focusPrompt {
			state.promptVP.GotoBottom()
			return m, nil
		}
		state.convFollow = true
		state.convVP.GotoBottom()
		return m, nil
	case tea.KeyEsc:
		return m.exitCoCreate()
	}

	// Khi chờ AI trả lời thì các phím dạng chỉnh sửa (gõ ký tự/xóa/con trỏ/Ctrl+U/xuống dòng
	// nhiều dòng) được thả — người dùng có thể soạn trước câu tiếp trong lúc AI suy nghĩ.
	// Các phím dạng nộp bị chặn hạ xuống từng case bên trong, để tiết lưu Enter chạy trước
	// việc chặn awaiting — nhờ đó mảnh \n do paste vẫn được thay bằng dấu cách.

	switch msg.Type {
	case tea.KeyCtrlS:
		if state.awaiting {
			return m, nil
		}
		if !state.canStart() {
			return m, nil
		}
		// Đồng sáng tạo theo giai đoạn: tiêm "brief hướng tiếp theo" và khôi phục sáng tác, về bàn chạy.
		if state.stage {
			draft := state.draftPrompt()
			m.cocreate = nil
			m.err = nil
			m.resizeTextarea()
			m.textarea.Placeholder = defaultSteerPlaceholder()
			return m, tea.Batch(resumeFromCoCreate(m.runtime, draft), m.textarea.Focus())
		}
		// Đồng sáng tạo khởi động nguội: bắt đầu sáng tác bằng lệnh sáng tác đã chỉnh lý.
		prompt, err := state.buildPrompt()
		if err != nil {
			m.err = err
			return m, nil
		}
		cmd := m.enterStarting(prompt)
		return m, tea.Batch(startRuntime(m.runtime, prompt), cmd)
	case tea.KeyEnter:
		// Alt+Enter → xuống dòng chủ động, để textarea.Update tiếp quản (KeyMap.InsertNewline đã gán phím này)
		if msg.Alt {
			break
		}
		// Khoảng cách với lần gõ ký tự trước quá ngắn → coi là mảnh \n của dòng paste:
		// thay bằng dấu cách thay vì nộp. Phải phán trước khi chặn awaiting — nếu không
		// mảnh \n paste trong lúc awaiting sẽ bị chặn, khiến "abc\ndef" bị nuốt thành
		// "abcdef", khác ngữ nghĩa với đường base.
		if !m.lastKeyAt.IsZero() && time.Since(m.lastKeyAt) < 50*time.Millisecond {
			var cmd tea.Cmd
			state.resetSuggestionInput()
			m.textarea, cmd = m.textarea.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
			m.refitTextareaHeight()
			return m, cmd
		}
		// Ý định nộp thật sự: chặn trong lúc awaiting (không thể phát request song song)
		if state.awaiting {
			return m, nil
		}
		text := utils.CleanInputLine(m.textarea.Value())
		if text == "" {
			return m, nil
		}
		m.err = nil
		state.appendUser(text)
		m.textarea.Reset()
		m.refitTextareaHeight()
		cmd := m.sendCoCreate()
		return m, cmd
	case tea.KeyCtrlU:
		state.resetSuggestionInput()
		m.textarea.Reset()
		m.refitTextareaHeight()
		return m, nil
	}

	// Phím số 1/2/3 có thể ghép gợi ý liên tục: lần đầu điền vào, lần sau nối thêm bằng
	// dấu chấm phẩy, chọn lặp bỏ qua. Mọi chỉnh tay đều thoát trạng thái ghép nhanh,
	// số sau đó giữ ngữ nghĩa nhập bình thường.
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && !state.awaiting {
		if r := msg.Runes[0]; r >= '1' && r <= '3' {
			if value, handled := state.appendSuggestion(int(r-'1'), m.textarea.Value()); handled {
				m.textarea.SetValue(value)
				m.textarea.CursorEnd()
				m.refitTextareaHeight()
				return m, nil
			}
		}
	}

	// Đầu vào thường quy chuyển tiếp cho textarea
	if msg.Type == tea.KeyRunes && (containsSGRFragment(string(msg.Runes)) || isCSILeak(msg.Runes)) {
		return m, nil
	}
	var ok bool
	if msg, ok = cleanHumanKeyRunes(msg); !ok {
		return m, nil
	}
	state.resetSuggestionInput()
	if msg.Type == tea.KeyRunes {
		m.lastKeyAt = time.Now()
	}
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	m.refitTextareaHeight()
	return m, cmd
}

// exitCoCreate thoát chế độ đồng sáng tạo, hủy request LLM đang chạy, khôi phục trạng thái ô nhập.
func (m Model) exitCoCreate() (tea.Model, tea.Cmd) {
	if m.cocreate.cancel != nil {
		m.cocreate.cancel()
	}
	stage := m.cocreate.stage
	initial := m.cocreate.initialInput()
	m.cocreate = nil
	m.resizeTextarea()
	// Hủy đồng sáng tạo theo giai đoạn: xóa cờ chiếm chỗ, giữ tạm dừng, về trạng thái nhập của
	// bàn chạy (không đổ lại phần mở đầu tổng hợp).
	if stage {
		m.textarea.SetValue("")
		m.textarea.Placeholder = defaultSteerPlaceholder()
		return m, tea.Batch(cancelCoCreate(m.runtime), fetchSnapshot(m.runtime), m.textarea.Focus())
	}
	m.textarea.SetValue(initial)
	m.textarea.Placeholder = placeholderForNewMode(m.startupMode)
	return m, m.textarea.Focus()
}

// overlayAboveInput nổi overlay phía trên đáy của base (phía trên inputBox),
// không đổi chiều cao bố cục tổng thể. Chỉ phủ đúng độ rộng thẻ overlay, bên phải lộ
// nội dung tầng dưới.
func overlayAboveInput(base, overlay string, inputLineCount int) string {
	baseLines := strings.Split(base, "\n")
	overLines := strings.Split(strings.TrimRight(overlay, "\n"), "\n")

	endY := len(baseLines) - inputLineCount
	startY := endY - len(overLines)
	if startY < 0 {
		startY = 0
	}

	for i, ol := range overLines {
		y := startY + i
		if y >= 0 && y < endY {
			olW := lipgloss.Width(ol)
			// Cắt bên trái baseline olW ký tự nhìn thấy được, nối overlay + phần nội dung phải còn lại
			right := ansi.TruncateLeft(baseLines[y], olW, "")
			baseLines[y] = ol + right
		}
	}
	return strings.Join(baseLines, "\n")
}

// isCSILeak phát hiện KeyRunes có phải mảnh sót của chuỗi thoát CSI bị rò không.
// Khi terminal gửi phím mũi tên \x1b[A, gõ quá nhanh có thể làm chuỗi bị tách:
// \x1b bị phân tích thành Escape, "[" hoặc "[A" rò vào textarea dưới dạng KeyRunes.
func isCSILeak(runes []rune) bool {
	if len(runes) == 0 || runes[0] != '[' {
		return false
	}
	for _, r := range runes[1:] {
		if (r >= '0' && r <= '9') || r == ';' ||
			(r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' {
			continue
		}
		return false
	}
	return true
}

// containsSGRFragment phát hiện văn bản có chứa mảnh sót chuỗi chuột SGR (mẫu "<số;só;") không.
func containsSGRFragment(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		j := i + 1
		if j >= len(s) || s[j] < '0' || s[j] > '9' {
			continue
		}
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j < len(s) && s[j] == ';' {
			return true
		}
	}
	return false
}

func cleanHumanKeyRunes(msg tea.KeyMsg) (tea.KeyMsg, bool) {
	if msg.Type != tea.KeyRunes {
		return msg, true
	}
	cleaned := utils.CleanInputRunes(msg.Runes)
	if cleaned == "" {
		return msg, false
	}
	msg.Runes = []rune(cleaned)
	return msg, true
}
