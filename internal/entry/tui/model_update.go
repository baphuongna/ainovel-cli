package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/utils"
)

const maxPromptEventCols = 160

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Chiều cao body phụ thuộc chiều cao thời gian thực của thanh trên/thanh dưới (thanh
	// chế độ trang mới, đầu vào nhiều dòng đều làm nó đổi), đồng bộ mỗi tin trước khi xử
	// lý, tránh viewport dừng ở chiều cao cũ, đáy bảng lấp dòng trống. Luật hợp nhất và rẻ.
	if m.width > 0 {
		m.updateViewportSize()
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeTextarea()
		m.updateViewportSize()
		m.refreshDetailViewport()
		m.refreshStateViewport()
		return m, nil
	case tea.KeyMsg:
		return m.handleKeyMsg(msg)
	case tea.MouseMsg:
		return m.handleMouseMsg(msg)
	default:
		if next, cmd, handled := m.handleRuntimeMsg(msg); handled {
			return next, cmd
		}
		return m.handleTextareaMsg(msg)
	}
}

func (m Model) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if next, cmd, handled := m.handleOverlayKeyMsg(msg); handled {
		return next, cmd
	}

	if msg.Type == tea.KeyCtrlC {
		if m.quitPending {
			return m, tea.Quit
		}
		m.quitPending = true
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return quitResetMsg{} })
	}
	m.quitPending = false

	if next, cmd, handled := m.handleCommandPaletteKey(msg); handled {
		return next, cmd
	}

	return m.handleBaseKeyMsg(msg)
}

func (m Model) handleOverlayKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case m.cocreate != nil:
		return m.handleBlockingModalKey(msg, m.handleCoCreateKey)
	case m.modelConfig != nil:
		return m.handleBlockingModalKey(msg, m.handleModelConfigKey)
	case m.help != nil:
		return m.handleBlockingModalKey(msg, m.handleHelpKey)
	case m.modelSwitch != nil:
		return m.handleBlockingModalKey(msg, m.handleModelSwitchKey)
	case m.report != nil:
		return m.handleBlockingModalKey(msg, m.handleReportKey)
	case m.importer != nil:
		return m.handleBlockingModalKey(msg, m.handleImportKey)
	case m.simulator != nil:
		return m.handleBlockingModalKey(msg, m.handleSimulationKey)
	default:
		return m, nil, false
	}
}

func (m Model) handleBlockingModalKey(msg tea.KeyMsg, next func(tea.KeyMsg) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd, bool) {
	if msg.Type == tea.KeyCtrlC {
		if m.quitPending {
			return m, tea.Quit, true
		}
		m.quitPending = true
		return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return quitResetMsg{} }), true
	}
	m.quitPending = false
	// Phím tắt toàn cục xuyên modal: khi modal mở vẫn phải chuyển được báo chuột, nếu
	// không trong các modal khóa màn hình như đồng sáng tạo/help/report người dùng
	// không thể kéo chọn copy gốc.
	if msg.Type == tea.KeyCtrlR {
		next, cmd := m.toggleMouseReporting()
		return next, cmd, true
	}
	model, cmd := next(msg)
	return model, cmd, true
}

// toggleMouseReporting công tắc báo chuột. Bật → tắt cho người dùng kéo chọn copy gốc;
// tắt → bật khôi phục click đổi tiêu điểm / con lăn. Đường base và đường blocking modal
// dùng chung.
func (m Model) toggleMouseReporting() (Model, tea.Cmd) {
	// Trang chào (modeNew) vốn không bật báo chuột, kéo chọn gốc đã copy được; ở đây
	// bỏ qua Ctrl+R, tránh bật nhầm báo chuột lại phá copy gốc. Báo chuột do enterRunning
	// bật khi vào bàn làm việc.
	if m.mode == modeNew {
		return m, nil
	}
	m.mouseOff = !m.mouseOff
	if m.mouseOff {
		return m, tea.DisableMouse
	}
	return m, tea.EnableMouseCellMotion
}

// donePlaceholder hoàn thành tác phẩm
const donePlaceholder = "Truyện đã hoàn thành · Có thể nhập yêu cầu sửa đổi (vd: \"viết lại chương 3\"), /reopen để viết tiếp tập mới, hoặc /export để xuất truyện"

// enterRunning vào bàn sáng tác: bật báo chuột (bàn làm việc cần click đổi bảng /
// con lăn / kéo thanh bên). Lệnh trả về cần bên gọi Batch vào giá trị trả cuối.
func (m *Model) enterRunning() tea.Cmd {
	m.mode = modeRunning
	m.mouseOff = false
	return tea.EnableMouseCellMotion
}

func (m Model) handleCommandPaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if !m.compActive {
		return m, nil, false
	}

	switch msg.Type {
	case tea.KeyEsc:
		m.clearCommandPalette()
		return m, nil, true
	case tea.KeyUp:
		if m.compIdx > 0 {
			m.compIdx--
		}
		return m, nil, true
	case tea.KeyDown:
		if m.compIdx < len(m.compItems)-1 {
			m.compIdx++
		}
		return m, nil, true
	case tea.KeyTab:
		m.acceptCommandCompletion()
		return m, nil, true
	case tea.KeyEnter:
		item, ok := m.acceptCommandCompletion()
		if !ok {
			return m, nil, true
		}
		if item.AutoExecute {
			m.textarea.Reset()
			next, cmd := m.handleSlashCommand(slashCommand{name: item.Name})
			return next, cmd, true
		}
		return m, nil, true
	default:
		return m, nil, false
	}
}

func (m Model) handleBaseKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Phòng thủ tiết lưu: paste \n ở terminal không hỗ trợ bracketed paste sẽ thoái hóa
	// thành KeyEnter liên tục; người thật bấm Enter cách ký tự trước thường > 100ms,
	// < 50ms gần như chắc là mảnh sót của dòng paste.
	// Chỉ ghi KeyRunes (dòng ký tự) — phím chức năng (↑↓/Tab/Ctrl-x) không được làm bẩn
	// tiết lưu, nếu không người dùng lật lịch sử chọn xong bấm Enter ngay sẽ bị nuốt nhầm.
	if msg.Type == tea.KeyRunes {
		m.lastKeyAt = time.Now()
	}
	switch msg.Type {
	case tea.KeyEscape:
		if m.mode == modeRunning && m.snapshot.IsRunning {
			return m, abortRuntime(m.runtime)
		}
		m.textarea.Reset()
		m.historyIdx = len(m.inputHistory)
		m.historyDraft = ""
		m.refitTextareaHeight()
		m.clearCommandPalette()
		return m, nil
	case tea.KeyCtrlL:
		m.resetOutputPanels()
		return m, nil
	case tea.KeyCtrlU:
		// Xóa ô nhập hiện tại; đồng thời thoát trạng thái duyệt lịch sử.
		m.textarea.Reset()
		m.historyIdx = len(m.inputHistory)
		m.historyDraft = ""
		m.refitTextareaHeight()
		m.clearCommandPalette()
		return m, nil
	case tea.KeyCtrlR:
		return m.toggleMouseReporting()
	case tea.KeyTab:
		if m.mode == modeNew {
			if m.cocreate != nil {
				return m, nil
			}
			if m.startupMode == startupModeQuick {
				m.startupMode = startupModeCoCreate
			} else {
				m.startupMode = startupModeQuick
			}
			m.textarea.Placeholder = placeholderForNewMode(m.startupMode)
			return m, nil
		}
		m.focusPane = (m.focusPane + 1) % focusPaneCount
		return m, nil
	case tea.KeyEnter:
		// Alt+Enter là xuống dòng chủ động, để textarea.Update tiếp quản (KeyMap.InsertNewline đã gán phím này).
		if msg.Alt {
			break
		}
		// Khoảng cách với lần gõ khác Enter trước quá ngắn → coi là mảnh sót \n của dòng paste:
		// thay bằng dấu cách giữ khoảng cách trực quan, cùng ngữ nghĩa với đường
		// cleanHumanKeyRunes ("abc\ndef" → "abc def").
		// Phòng thủ môi trường terminal bracketed paste hỏng (SSH cũ/một số cấu hình tmux).
		if !m.lastKeyAt.IsZero() && time.Since(m.lastKeyAt) < 50*time.Millisecond {
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
			m.refitTextareaHeight()
			return m, cmd
		}
		return m.handleEnterKey()
	case tea.KeyUp:
		// Đầu vào nhiều dòng: để textarea tiếp quản di chuyển con trỏ trong dòng (rơi vào
		// textarea.Update sau switch)
		if m.textareaIsMultiline() {
			break
		}
		// Một dòng: ưu tiên lật lịch sử, khi không còn lịch sử dùng được thì lùi về cuộn luồng sự kiện
		if m.tryHistoryUp() {
			return m, nil
		}
		return m.handleVerticalScrollKey(msg, true)
	case tea.KeyDown:
		if m.textareaIsMultiline() {
			break
		}
		if m.tryHistoryDown() {
			return m, nil
		}
		return m.handleVerticalScrollKey(msg, false)
	case tea.KeyPgUp:
		return m.handleVerticalScrollKey(msg, true)
	case tea.KeyPgDown:
		return m.handleVerticalScrollKey(msg, false)
	case tea.KeyEnd:
		switch m.focusPane {
		case focusStream:
			m.streamScroll = true
			m.streamVP.GotoBottom()
		case focusDetail:
			m.detailVP.GotoBottom()
		case focusState:
			m.stateVP.GotoBottom()
		default:
			m.autoScroll = true
			m.viewport.GotoBottom()
		}
		return m, nil
	}

	if msg.Type == tea.KeyRunes && (containsSGRFragment(string(msg.Runes)) || isCSILeak(msg.Runes)) {
		return m, nil
	}
	var ok bool
	if msg, ok = cleanHumanKeyRunes(msg); !ok {
		return m, nil
	}

	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	m.refitTextareaHeight()
	m.updateCommandPalette()
	return m, cmd
}

func (m Model) handleEnterKey() (tea.Model, tea.Cmd) {
	text := utils.CleanInputLine(m.textarea.Value())
	if text == "" {
		return m, nil
	}
	m.clearCommandPalette()
	if cmd, ok := parseSlashCommand(text); ok {
		m.pushInputHistory(text)
		m.textarea.Reset()
		m.refitTextareaHeight()
		return m.handleSlashCommand(cmd)
	}

	m.pushInputHistory(text)
	m.textarea.Reset()
	m.refitTextareaHeight()
	switch m.mode {
	case modeNew:
		m.err = nil
		if m.startupMode == startupModeQuick {
			prompt, err := startup.PrepareQuick(text)
			if err != nil {
				m.err = err
				return m, nil
			}
			cmd := m.enterStarting(prompt)
			return m, tea.Batch(startRuntime(m.runtime, prompt), cmd)
		}
		m.cocreate = newCoCreateState(text)
		return m, m.sendCoCreate()
	case modeRunning:
		// Không hiển thị lại sự kiện USER cục bộ — lối vào Host.Continue/Steer đã emit sự kiện
		// "USER", theo kênh events chảy về TUI. Kiến trúc §2.3: tầng quan sát chỉ quan sát,
		// không sinh sự kiện.
		if !m.snapshot.IsRunning {
			return m, continueRuntime(m.runtime, text)
		}
		return m, steerRuntime(m.runtime, text)
	case modeDone:
		// Đầu vào của người dùng sau khi hoàn sách (nhu cầu viết lại/viết tiếp): đánh thức
		// vòng run mới. Continue ở trạng thái dừng đi Inject tự khôi phục, Arbiter phán định
		// can thiệp người dùng; khi viết lại chương đã viết thì Engine mở lại toàn sách và
		// đưa vào hàng đợi.
		// Chuyển lại modeRunning vào lại bàn làm việc; vòng này chạy xong doneMsg(complete)
		// sẽ đặt lại modeDone. Lệnh gạch chéo đã xử lý sớm ở trên, không qua nhánh này.
		m.mode = modeRunning
		return m, continueRuntime(m.runtime, text)
	default:
		return m, nil
	}
}

func (m Model) handleVerticalScrollKey(msg tea.KeyMsg, upward bool) (tea.Model, tea.Cmd) {
	if m.focusPane == focusStream {
		if upward {
			m.streamScroll = false
		}
		var cmd tea.Cmd
		m.streamVP, cmd = m.streamVP.Update(msg)
		if !upward && m.streamVP.AtBottom() {
			m.streamScroll = true
		}
		return m, cmd
	}
	if m.focusPane == focusDetail {
		var cmd tea.Cmd
		m.detailVP, cmd = m.detailVP.Update(msg)
		return m, cmd
	}
	if m.focusPane == focusState {
		var cmd tea.Cmd
		m.stateVP, cmd = m.stateVP.Update(msg)
		return m, cmd
	}
	if upward {
		m.autoScroll = false
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	if !upward && m.viewport.AtBottom() {
		m.autoScroll = true
	}
	return m, cmd
}

func (m Model) handleMouseMsg(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.cocreate != nil {
		// Chuột chia theo tọa độ X: nửa trái màn hình = bảng conv, nửa phải = bảng prompt.
		// Modal căn giữa và conv chiếm trái ~58%, dùng đường giữa màn hình phân biệt đủ chính xác.
		// Người dùng con lăn ở vùng conv tự ngừng follow (cho dừng ổn định ở một vị trí lịch sử).
		var cmd tea.Cmd
		if msg.X < m.width/2 {
			m.cocreate.convFollow = false
			m.cocreate.convVP, cmd = m.cocreate.convVP.Update(msg)
			if m.cocreate.convVP.AtBottom() {
				m.cocreate.convFollow = true
			}
		} else {
			m.cocreate.promptVP, cmd = m.cocreate.promptVP.Update(msg)
		}
		return m, cmd
	}
	if m.modelSwitch != nil || m.modelConfig != nil {
		return m, nil
	}
	if pane, ok := m.paneAtMouse(msg.X, msg.Y); ok {
		m.hoverPane = pane
		m.hoverActive = true
		if msg.Action == tea.MouseActionPress {
			m.focusPane = pane
		}
	} else {
		m.hoverActive = false
	}

	var cmd tea.Cmd
	if m.focusPane == focusStream {
		m.streamVP, cmd = m.streamVP.Update(msg)
		if msg.Action == tea.MouseActionPress {
			m.streamScroll = m.streamVP.AtBottom()
		}
		return m, cmd
	}
	if m.focusPane == focusDetail {
		m.detailVP, cmd = m.detailVP.Update(msg)
		return m, cmd
	}
	if m.focusPane == focusState {
		m.stateVP, cmd = m.stateVP.Update(msg)
		return m, cmd
	}
	m.viewport, cmd = m.viewport.Update(msg)
	if msg.Action == tea.MouseActionPress {
		m.autoScroll = m.viewport.AtBottom()
	}
	return m, cmd
}

func (m Model) handleRuntimeMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case eventMsg:
		hadRunningEvent := m.hasRunningEvent()
		ev := host.Event(msg)
		m.applyEventProjection(ev)
		m.refreshEventViewport()
		cmd := listenEvents(m.runtime)
		if !hadRunningEvent && m.hasRunningEvent() && !m.toolTicking {
			m.toolTicking = true
			cmd = tea.Batch(cmd, tickToolSpinner())
		}
		return m, cmd, true
	case bootstrapMsg:
		// Có tác phẩm hay không quyết định điểm rơi giao diện, khôi phục thành công chỉ quyết
		// định engine có chạy không. Khi nâng cấp dữ liệu, ngân sách hay cổng soát soát
		// thất bại vẫn ở lại bàn làm việc hiển thị sách cũ, không được lùi về trang chào.
		if (msg.existing || msg.resumed) && m.mode == modeNew && !msg.completed {
			enableMouse := m.enterRunning()
			m.resizeTextarea()
			m.textarea.Placeholder = defaultSteerPlaceholder()
			if msg.err != nil {
				m.err = msg.err
			}
			return m, tea.Batch(fetchSnapshot(m.runtime), enableMouse), true
		}
		// Sách đã hoàn: rơi vào bàn làm việc trạng thái hoàn tất (enterRunning bật chuột rồi
		// đổi modeDone), không rơi trang chào — trang chào với sách sẵn có chỉ không nhắc gì,
		// người dùng sẽ tưởng sách mất; /reopen, /export, nhập viết lại đều ở bàn làm việc.
		if msg.completed && m.mode == modeNew {
			enableMouse := m.enterRunning()
			m.mode = modeDone
			m.resizeTextarea()
			m.textarea.Placeholder = donePlaceholder
			if msg.err != nil {
				m.err = msg.err
			}
			return m, tea.Batch(fetchSnapshot(m.runtime), enableMouse, m.textarea.Focus()), true
		}
		// /reopen v.v. khôi phục trong phiên từ trạng thái hoàn tất vào lại bàn sáng tác.
		if msg.resumed && m.mode == modeDone {
			enableMouse := m.enterRunning()
			m.resizeTextarea()
			m.textarea.Placeholder = defaultSteerPlaceholder()
			return m, tea.Batch(fetchSnapshot(m.runtime), enableMouse), true
		}
		if msg.err != nil {
			m.err = msg.err
		}
		return m, fetchSnapshot(m.runtime), true
	case snapshotMsg:
		next := host.UISnapshot(msg)
		detailChanged := !sameDetailSnapshot(m.snapshot, next)
		runningChanged := m.snapshot.IsRunning != next.IsRunning
		m.snapshot = next
		m.syncRuntimePlaceholder()
		m.refreshEventViewport()
		if runningChanged {
			m.refreshStreamViewport()
		}
		if detailChanged {
			m.refreshDetailViewport()
		}
		m.refreshStateViewport()
		return m, tickSnapshot(m.runtime), true
	case doneMsg:
		m.snapshot.IsRunning = false
		m.refreshEventViewport()
		m.refreshStreamViewport()
		m.refreshStateViewport()
		if msg.complete {
			m.abortPending = false
			m.mode = modeDone
			// Trạng thái hoàn tất không khóa ô nhập: ngừng tự viết tiếp, nhưng người dùng vẫn
			// nhập được yêu cầu viết lại (đầu vào modeDone đánh thức vòng run mới qua Continue,
			// Arbiter phán viết lại hay tiếp tục sáng tác; /export, /model v.v. cũng cần dùng
			// được, ô nhập phải giữ tiêu điểm (issue #27, #38).
			m.textarea.Placeholder = donePlaceholder
			return m, tea.Batch(fetchSnapshot(m.runtime), listenDone(m.runtime), m.textarea.Focus()), true
		}
		if m.abortPending {
			m.abortPending = false
			m.snapshot.RuntimeState = "paused"
			m.syncRuntimePlaceholder()
		} else {
			m.textarea.Placeholder = "Sáng tác bị gián đoạn, nhập nội dung bất kỳ để tiếp tục"
		}
		return m, tea.Batch(fetchSnapshot(m.runtime), listenDone(m.runtime)), true
	case abortResultMsg:
		if msg.stopped {
			m.abortPending = true
			m.textarea.Placeholder = "Đang tạm dừng sáng tác..."
		}
		return m, nil, true
	case reportLoadedMsg:
		if m.report == nil || msg.reqID != m.report.reqID {
			return m, nil, true
		}
		boxW, _ := reportModalSize(m.width, m.height)
		m.report.load(msg.report, paddedModalContentWidth(boxW), msg.exportPath, msg.exportErr, msg.finishedAt)
		return m, nil, true
	case importEventMsg:
		if m.importer == nil || msg.reqID != m.importer.reqID {
			return m, nil, true
		}
		boxW, _ := reportModalSize(m.width, m.height)
		m.importer.appendEvent(msg.ev, paddedModalContentWidth(boxW))
		if msg.ev.Stage == imp.StageError {
			return m, nil, true
		}
		if msg.ev.Stage == imp.StageDone {
			if msg.ev.Continued {
				m.importer = nil
				enableMouse := m.enterRunning()
				m.resizeTextarea()
				m.textarea.Placeholder = defaultSteerPlaceholder()
				return m, tea.Batch(enableMouse, m.textarea.Focus()), true
			}
			return m, nil, true
		}
		return m, listenImportEvent(msg.reqID, msg.ch), true
	case importClosedMsg:
		if m.importer == nil || msg.reqID != m.importer.reqID || m.importer.done {
			return m, nil, true
		}
		m.importer.paused = true
		boxW, _ := reportModalSize(m.width, m.height)
		m.importer.refresh(paddedModalContentWidth(boxW))
		return m, nil, true
	case simEventMsg:
		if m.simulator == nil || msg.reqID != m.simulator.reqID {
			return m, nil, true
		}
		boxW, _ := reportModalSize(m.width, m.height)
		m.simulator.appendEvent(msg.ev, paddedModalContentWidth(boxW))
		if msg.terminal() {
			return m, nil, true
		}
		return m, listenSimulationEvent(msg.reqID, msg.ch), true
	case exportDoneMsg:
		if msg.err != nil {
			m.applyEvent(host.Event{
				Time: time.Now(), Category: "ERROR", Summary: "Xuất file thất bại: " + msg.err.Error(), Level: "error",
			})
		} else if msg.result != nil {
			m.applyEvent(host.Event{
				Time: time.Now(), Category: "SYSTEM", Summary: formatExportSuccess(msg.result), Level: "success",
			})
		}
		m.refreshEventViewport()
		return m, nil, true
	case revisionDoneMsg:
		if msg.err != nil {
			m.applyEvent(host.Event{Time: time.Now(), Category: "ERROR", Summary: "Đồng bộ chương thất bại: " + msg.err.Error(), Level: "error"})
		} else if msg.checkOnly {
			summary := "Không phát hiện chương nào bị chỉnh sửa từ bên ngoài"
			if len(msg.chapters) > 0 {
				summary = fmt.Sprintf("Phát hiện chính văn chương đã bị chỉnh sửa từ bên ngoài: %v; Chạy /sync để tiếp nhận", msg.chapters)
			}
			m.applyEvent(host.Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "info"})
		} else {
			m.applyEvent(host.Event{Time: time.Now(), Category: "SYSTEM", Summary: formatRevisionResult(msg.result), Level: "success"})
		}
		m.refreshEventViewport()
		return m, tea.Batch(fetchSnapshot(m.runtime), m.textarea.Focus()), true
	case modelConfigSavedMsg:
		if m.modelConfig == nil {
			return m, nil, true
		}
		if msg.err != nil {
			m.modelConfig.saving = false
			m.modelConfig.message = msg.err.Error()
			return m, nil, true
		}
		m.modelConfig = nil
		return m, tea.Batch(fetchSnapshot(m.runtime), m.textarea.Focus()), true
	case styleSavedMsg:
		// Lưu thể loại là thao tác bên lề: giữ modal mở để tiếp tục chỉnh provider/model,
		// khác với lưu provider xong thì đóng bảng.
		if m.modelConfig == nil {
			return m, nil, true
		}
		m.modelConfig.saving = false
		if msg.err != nil {
			m.modelConfig.message = "Lưu thể loại thất bại: " + msg.err.Error()
			return m, nil, true
		}
		m.modelConfig.message = "Đã lưu thể loại: " + m.modelConfig.styleLabel()
		return m, nil, true
	case modelConfigConnectionMsg:
		if m.modelConfig == nil {
			return m, nil, true
		}
		m.modelConfig.testing = false
		m.modelConfig.testCancel = nil
		if errors.Is(msg.err, context.Canceled) {
			m.modelConfig.message = "Kiểm tra kết nối đã bị hủy"
		} else if msg.err != nil {
			m.modelConfig.message = msg.err.Error()
		} else {
			m.modelConfig.message = "Kiểm tra kết nối thành công: " + msg.model
		}
		return m, nil, true
	case startResultMsg:
		next, cmd := m.handleStartResultMsg(msg)
		return next, cmd, true
	case cocreateDeltaMsg:
		if m.cocreate == nil || msg.reqID != m.cocreate.reqID {
			return m, nil, true
		}
		m.cocreate.applyDelta(msg.kind, msg.text)
		return m, listenCoCreateDelta(m.cocreate), true
	case cocreateDoneMsg:
		next, cmd := m.handleCoCreateDoneMsg(msg)
		return next, cmd, true
	case steerResultMsg:
		if msg.err != nil {
			m.err = msg.err
			m.applyEvent(host.Event{Time: time.Now(), Category: "ERROR", Summary: msg.err.Error(), Level: "error"})
			m.refreshEventViewport()
			return m, tea.Batch(fetchSnapshot(m.runtime), m.textarea.Focus()), true
		}
		return m, tea.Batch(fetchSnapshot(m.runtime), listenDone(m.runtime)), true
	case continueResultMsg:
		if msg.err != nil {
			m.err = msg.err
			m.applyEvent(host.Event{
				Time: time.Now(), Category: "ERROR", Summary: msg.err.Error(), Level: "error",
			})
			m.refreshEventViewport()
			return m, tea.Batch(fetchSnapshot(m.runtime), m.textarea.Focus()), true
		}
		m.err = nil
		m.textarea.Placeholder = defaultSteerPlaceholder()
		return m, tea.Batch(fetchSnapshot(m.runtime), listenDone(m.runtime), m.textarea.Focus()), true
	case spinnerTickMsg:
		m.spinnerIdx = (m.spinnerIdx + 1) % len(spinnerFrames)
		m.cursorIdx++
		if m.snapshot.IsRunning {
			// Thanh trên, gợi ý hoạt động và con trỏ stream dùng chung hoạt ảnh tần số thấp,
			// tránh nhiều timer thường trực lặp kích View toàn màn hình.
			m.refreshEventViewport()
			m.refreshStreamViewport()
			m.streamDirty = false
		}
		if s := m.importer; s != nil && !s.done && !s.paused {
			s.frame = m.cursorIdx
			boxW, _ := reportModalSize(m.width, m.height)
			s.refresh(paddedModalContentWidth(boxW))
		}
		return m, tickSpinner(), true
	case toolSpinnerTickMsg:
		m.toolSpinnerIdx = (m.toolSpinnerIdx + 1) % len(toolSpinnerFrames)
		if m.hasRunningEvent() {
			m.refreshEventViewport()
			return m, tickToolSpinner(), true
		}
		m.toolTicking = false
		return m, nil, true
	case streamDeltaMsg:
		if len(m.streamRounds) == 0 {
			m.streamRounds = append(m.streamRounds, "")
		}
		m.streamRounds[len(m.streamRounds)-1] += string(msg)
		// Không refresh ngay; delta đầu khởi động một cửa sổ gộp 16ms, các delta sau dùng
		// lại timer đó.
		m.streamDirty = true
		cmd := listenStream(m.runtime)
		if !m.flushPending {
			m.flushPending = true
			cmd = tea.Batch(cmd, tickStreamFlush())
		}
		return m, cmd, true
	case streamClearMsg:
		// Ranh giới round: đẩy delta tích lũy ra trước, round mới mới thẳng hàng về mặt trực quan
		if m.flushStreamIfDirty() && m.streamScroll {
			m.streamVP.GotoBottom()
		}
		if len(m.streamRounds) == 0 {
			m.streamRounds = append(m.streamRounds, "")
		} else if strings.TrimSpace(m.streamRounds[len(m.streamRounds)-1]) != "" {
			m.streamRounds = append(m.streamRounds, "")
		}
		m.trimStreamRounds()
		m.streamRound = len(m.streamRounds)
		m.refreshStreamViewport()
		if m.streamScroll {
			m.streamVP.GotoBottom()
		}
		return m, listenStream(m.runtime), true
	case streamFlushTickMsg:
		m.flushPending = false
		if m.flushStreamIfDirty() && m.streamScroll {
			m.streamVP.GotoBottom()
		}
		return m, nil, true
	case quitResetMsg:
		m.quitPending = false
		return m, nil, true
	default:
		return m, nil, false
	}
}

func sameDetailSnapshot(a, b host.UISnapshot) bool {
	return a.Synopsis == b.Synopsis &&
		a.Premise == b.Premise &&
		a.Layered == b.Layered &&
		a.CurrentVolumeArc == b.CurrentVolumeArc &&
		a.NextVolumeTitle == b.NextVolumeTitle &&
		a.CompassDirection == b.CompassDirection &&
		a.CompassScale == b.CompassScale &&
		a.SupportingCount == b.SupportingCount &&
		a.CompletedCount == b.CompletedCount &&
		a.InProgressChapter == b.InProgressChapter &&
		a.LastCommitSummary == b.LastCommitSummary &&
		a.LastReviewSummary == b.LastReviewSummary &&
		slices.Equal(a.Outline, b.Outline) &&
		slices.Equal(a.Characters, b.Characters) &&
		slices.Equal(a.RecentSupporting, b.RecentSupporting) &&
		slices.Equal(a.RecentSummaries, b.RecentSummaries)
}

func (m Model) handleStartResultMsg(msg startResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		wasStarting := m.starting
		m.starting = false
		if m.mode != modeNew {
			m.applyEvent(host.Event{
				Time: time.Now(), Category: "ERROR", Summary: msg.err.Error(), Level: "error",
			})
			m.refreshEventViewport()
		}
		if m.cocreate != nil {
			m.cocreate.awaiting = false
			m.textarea.Placeholder = placeholderForCoCreate(m.cocreate)
			return m, tea.Batch(fetchSnapshot(m.runtime), m.textarea.Focus())
		}
		if wasStarting {
			// Đã vào bàn làm việc sau Enter; lỗi LLM giai đoạn khởi động hiện ngay trên bàn
			// làm việc hiện tại, không lùi về trang chào nữa.
			m.mode = modeRunning
			m.snapshot.IsRunning = false
			m.snapshot.RuntimeState = "idle"
			m.textarea.Placeholder = "Khởi tạo thất bại, vui lòng kiểm tra cấu hình hoặc dùng /model để đổi model"
			m.refreshStreamViewport()
			m.refreshStateViewport()
			return m, m.textarea.Focus()
		}
		if m.mode == modeNew {
			m.textarea.Placeholder = placeholderForNewMode(m.startupMode)
			return m, tea.Batch(fetchSnapshot(m.runtime), m.textarea.Focus())
		}
		return m, fetchSnapshot(m.runtime)
	}
	m.starting = false

	if m.mode == modeNew {
		m.cocreate = nil
		enableMouse := m.enterRunning()
		m.resizeTextarea()
		m.textarea.Placeholder = defaultSteerPlaceholder()
		return m, tea.Batch(fetchSnapshot(m.runtime), m.textarea.Focus(), enableMouse)
	}

	return m, fetchSnapshot(m.runtime)
}

func (m *Model) enterStarting(rawPrompt string) tea.Cmd {
	m.cocreate = nil
	m.err = nil
	m.starting = true
	m.snapshot.IsRunning = true
	m.snapshot.RuntimeState = "running"
	enableMouse := m.enterRunning()
	m.resetOutputPanels()
	m.resizeTextarea()
	m.textarea.Placeholder = "Đang khởi tạo sáng tác..."
	m.applyStartupPromptEvent(rawPrompt)
	m.applyEvent(host.Event{
		Time: time.Now(), Category: "SYSTEM", Summary: "Đang khởi tạo sáng tác", Level: "info",
	})
	m.refreshEventViewport()
	m.refreshStreamViewport()
	m.refreshStateViewport()
	return tea.Batch(m.textarea.Focus(), enableMouse)
}

func (m *Model) applyStartupPromptEvent(rawPrompt string) {
	text := utils.CleanInputLine(rawPrompt)
	if text == "" {
		return
	}
	m.applyEvent(host.Event{
		Time:     time.Now(),
		Category: "USER",
		Summary:  "Yêu cầu sáng tác: " + truncate(text, maxPromptEventCols),
		Detail:   text,
		Level:    "info",
	})
}

func (m Model) handleCoCreateDoneMsg(msg cocreateDoneMsg) (tea.Model, tea.Cmd) {
	if m.cocreate == nil || msg.reqID != m.cocreate.reqID {
		return m, nil
	}
	if msg.err != nil {
		m.err = msg.err
		m.cocreate.awaiting = false
		m.textarea.Placeholder = placeholderForCoCreate(m.cocreate)
		return m, m.textarea.Focus()
	}
	m.err = nil
	m.cocreate.apply(msg.reply)
	m.textarea.Placeholder = placeholderForCoCreate(m.cocreate)
	return m, m.textarea.Focus()
}

func (m Model) handleTextareaMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	m.refitTextareaHeight()
	m.updateCommandPalette()
	return m, cmd
}

// applyEvent ghi lại một sự kiện do TUI sinh cục bộ và cập nhật phép chiếu. Sự kiện
// của Host đã ghi nhật ký ở nơi sinh; đường subscribe sự kiện phải gọi trực tiếp
// applyEventProjection, tránh ghi trùng.
func (m *Model) applyEvent(ev host.Event) {
	host.LogEvent(ev)
	m.applyEventProjection(ev)
}

// applyEventProjection áp một sự kiện vào m.events:
//   - có ID và đã tồn tại → cập nhật tại chỗ (gộp trường trạng thái kết thúc, giữ
//     Time / Summary của lần đầu)
//   - sự kiện mới → nối thêm, khi cần ghi vào eventIndex
//   - vượt maxEvents thì cắt trượt và dựng lại chỉ mục
func (m *Model) applyEventProjection(ev host.Event) {
	if ev.ID != "" {
		if idx, ok := m.eventIndex[ev.ID]; ok && idx >= 0 && idx < len(m.events) {
			existing := &m.events[idx]
			if !ev.FinishedAt.IsZero() {
				existing.FinishedAt = ev.FinishedAt
			}
			if ev.Duration > 0 {
				existing.Duration = ev.Duration
			}
			if ev.Failed {
				existing.Failed = true
			}
			if ev.Level != "" {
				existing.Level = ev.Level
			}
			if ev.Detail != "" {
				existing.Detail = ev.Detail
			}
			if ev.Kind != "" {
				existing.Kind = ev.Kind
			}
			// Summary khác rỗng thì cho phép đè (trạng thái kết thúc có thể mang thông tin bổ
			// sung); nếu không giữ của lần đầu
			if ev.Summary != "" {
				existing.Summary = ev.Summary
			}
			// Sự kiện retry cùng ID cập nhật xuyên attempt, thời điểm chốt mới phải theo kịp,
			// đếm ngược mới reset theo
			if !ev.RetryAt.IsZero() {
				existing.RetryAt = ev.RetryAt
			}
			return
		}
	}

	m.events = append(m.events, ev)
	if ev.ID != "" {
		m.eventIndex[ev.ID] = len(m.events) - 1
	}
	if len(m.events) > maxEvents {
		drop := len(m.events) - maxEvents
		m.events = m.events[drop:]
		m.rebuildEventIndex()
	}
}

// trimStreamRounds cắt streamRounds về maxStreamRounds đoạn; vượt thì bỏ từ đầu.
// Thời điểm gọi: sau mỗi lần streamClear mở vòng mới.
func (m *Model) trimStreamRounds() {
	if len(m.streamRounds) <= maxStreamRounds {
		return
	}
	drop := len(m.streamRounds) - maxStreamRounds
	m.streamRounds = m.streamRounds[drop:]
}

func (m *Model) rebuildEventIndex() {
	m.eventIndex = make(map[string]int, len(m.events))
	for i, e := range m.events {
		if e.ID != "" {
			m.eventIndex[e.ID] = i
		}
	}
}

func (m *Model) resetOutputPanels() {
	m.events = nil
	m.eventIndex = make(map[string]int)
	m.viewport.SetContent("")
	m.viewport.GotoTop()
	m.streamBuf.Reset()
	m.streamRounds = nil
	m.streamVP.SetContent("")
	m.streamVP.GotoTop()
	m.streamRound = 0
}
