package host

import (
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// handleSubagentDelta phân luồng văn bản và tham số gọi tool của subagent:
// - DeltaText chảy ra trực tiếp như markdown
// - DeltaToolCall chỉ trích trường của các tool nội dung dài đã biết (như draft_chapter.content) rồi chảy ra; tham số JSON của tool khác vứt hết
func (o *observer) handleSubagentDelta(p *agentcore.ProgressPayload) {
	if p.DeltaKind != agentcore.DeltaToolCall {
		o.emitStreamDelta(p.Delta, false)
		return
	}
	if p.Tool == "" {
		return // tên tool chưa sẵn sàng, để delta kế tiếp thử lại
	}

	// Khi nhận ra tên tool bằng streaming, phát sớm sự kiện TOOL đang thực hiện, để spinner phủ cả giai đoạn LLM sinh nội dung
	// (nếu không "đang thực hiện" của tool như draft_chapter chỉ hiện trong vài chục mili-giây Execute thật).
	// Khi ProgressToolStart thật tới nơi và thấy toolStarts đã có bản ghi, chỉ bổ sung summary.
	o.ensureSubagentToolStarted(p.Agent, p.Tool)
	o.updateToolCallSummaryFromDelta(p.Agent, p.Tool, p.Delta)

	cur, ok := o.streamExtractors[p.Agent]
	// Sau khi args của cùng một lần gọi tool đã đóng (gặp } tầng ngoài cùng), vẫn có thể nhận trailing delta:
	// một số provider (đo thực tế deepseek-v4-flash) tách một lần args thành nhiều chunk,
	// chunk cuối sau `}` còn kèm khoảng trắng hoặc ký tự lặp lại. Nếu lúc này xử lý theo "khớp tên tool +
	// Done là dựng lại", extractor mới sẽ emit thêm một lần ✻ header và lấy đoạn token đuôi
	// coi như args mới để parse. Các delta này là đuôi thừa, vứt đi là được.
	if ok && cur.tool == p.Tool && cur.ext.Done() {
		return
	}
	// Tên tool đổi hoặc chưa từng dựng: dựng mới.
	if !ok || cur.tool != p.Tool {
		ext := newToolExtractor(p.Tool)
		if ext == nil {
			delete(o.streamExtractors, p.Agent)
			return
		}
		cur = &agentExtractor{tool: p.Tool, ext: ext}
		o.streamExtractors[p.Agent] = cur
	}
	if emitted := cur.ext.Feed(p.Delta); emitted != "" {
		if !cur.emittedAny {
			cur.emittedAny = true
			// streamClear để ✻ header của extractor rơi vào điểm đầu round mới, kết hợp
			// với kiểm tra HasPrefix("✻") của renderStreamContent đi đường renderAgentBlock
			// highlight; nếu chỉ dùng ensureStreamParagraphBreak chèn dòng trống mà không mở round, ✻ vẫn bị
			// phần thinking/thân văn phía trước ôm lấy, rơi xuống renderChapterBlock bị màu mặc định vẽ đè.
			o.streamClear()
			// streamClear đã chủ động xóa sạch streamExtractors. cur hiện tại còn phải Feed tiếp
			// delta của lần gọi tool này, phải đăng ký lại ngay; nếu không đoạn delta kế
			// tới sẽ dựng extractor mới, parse từ giữa args (phải tới `{` của object lồng
			// mới vào psBeforeKey), rồi coi timeline_events.time / foreshadow_updates.id
			// như trường tầng ngoài, TUI xuất hiện ✻ header trùng lặp.
			o.streamExtractors[p.Agent] = cur
		}
		o.emitStreamDelta(emitted, false)
	}
}

func (o *observer) emitStreamDelta(delta string, thinking bool) {
	if delta == "" {
		return
	}
	if thinking != o.streamThinking {
		o.emitD(utils.ThinkingSep)
		o.streamThinking = thinking
	}
	o.emitD(delta)
	o.streamHasContent = true
	o.streamLastByte = delta[len(delta)-1]
}

// ensureSubagentToolStarted: khi lần đầu nhìn thấy tool_call qua streaming, đăng ký sớm cho agent đó
// một lần gọi TOOL đang thực hiện, để spinner của luồng sự kiện phủ cả giai đoạn "LLM sinh streaming tham số
// tool_call" (thường chiếm 99% tổng thời gian gọi). args lúc này chưa đầy đủ, tạm dùng riêng tên tool
// làm summary; khi ProgressToolStart thật tới sẽ bù summary đầy đủ tham số.
func (o *observer) ensureSubagentToolStarted(agent, tool string) {
	if agent == "" || tool == "" {
		return
	}
	if _, ok := o.toolStarts[agent]; ok {
		return // đã có lần gọi đang thực hiện, idempotent
	}
	o.resetStreamArgLabel(agent, tool)
	id := nextEventID()
	o.toolStarts[agent] = &activeCall{
		id:      id,
		start:   time.Now(),
		summary: tool, // tạm dùng riêng tên tool, ProgressToolStart tới có thể cập nhật thành tool(chương N)
		depth:   1,
	}
	o.emitAndLog(Event{
		ID:       id,
		Time:     time.Now(),
		Category: "TOOL",
		Agent:    agent,
		Summary:  tool,
		Level:    "info",
		Depth:    1,
	})
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = tool
	})
	o.emitFallbackStreamHeader(tool)
}

func (o *observer) resetStreamArgLabel(agent, tool string) {
	key := streamArgKey(agent, tool)
	delete(o.streamArgPrefixes, key)
	delete(o.streamArgLabels, key)
}

// emitFallbackStreamHeader bù một dòng ✻ tiêu đề vào panel streaming cho tool chưa cấu hình extractor.
// Cả hai đường đều phải gọi để đảm bảo nhất quán:
//  1. ensureSubagentToolStarted —— tham số tool streaming của subagent (DeltaToolCall)
//  2. handleToolUpdate ProgressToolStart —— tham số tool không streaming của subagent
//
// Thiếu đường nào, tiêu đề tool của model streaming và không streaming sẽ biểu hiện không nhất quán.
func (o *observer) emitFallbackStreamHeader(tool string) {
	if _, has := toolDisplays[tool]; has {
		return // có extractor, header do extractor tự xuất
	}
	o.streamClear()
	o.emitStreamDelta(streamHeaderFallback(tool)+"\n", false)
}

// streamHeaderFallback sinh văn bản header streaming cho tool chưa cấu hình extractor,
// để người dùng kể cả với tool đọc nhẹ cũng thấy "đang gọi gì".
//
// Tiền tố "✻ " là quy ước đánh dấu "khối điều phối agent" — renderStreamContent của TUI thấy tiền tố
// này sẽ đi đường renderAgentBlock để render (icon + label highlight + đường kẻ),
// nếu không sẽ rơi xuống đường khối thân văn dùng màu mặc định terminal, header trông như thân văn thường không nổi.
func streamHeaderFallback(tool string) string {
	return "✻ " + tool
}

// streamClear báo TUI mở round streamRound mới, đồng thời reset trạng thái liên quan phân tách đoạn.
// Về logic, round mới là "stream rỗng", nếu không extractor đầu tiên của lần xuất kế sẽ bù nhầm dòng trống dẫn đầu.
//
// streamThinking phải reset theo: emitStreamDelta dùng streamThinking theo dõi xuyên suốt các lần gọi
// xem đoạn trước có phải thinking hay không. Trong round mới chưa xuất nội dung nào, lần emit(thinking=false)
// kế tiếp không nên chèn ThinkingSep nữa. Nếu không fallback header (như ✻ đọc chương) sẽ bị \x02
// chiếm đầu, HasPrefix("✻") của renderStreamContent thất bại, nguyên đoạn rơi xuống đường thân văn
// rồi bị ThinkingSep cắt thành đoạn thinking, màu title bị vẽ thành màu thinking.
func (o *observer) streamClear() {
	o.emitC()
	o.streamHasContent = false
	o.streamLastByte = 0
	o.streamThinking = false
	// Trước khi subagent vòng trước kết thúc, ProgressToolEnd đã delete; ở đây chủ động xóa sạch một lần.
	if len(o.streamExtractors) > 0 {
		o.streamExtractors = make(map[string]*agentExtractor)
	}
}
