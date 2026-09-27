package host

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/voocel/agentcore"
)

// handleToolUpdate xử lý chuyển tiếp tiến độ của Worker (ProgressPayload): dòng TOOL, thân văn streaming,
// thinking, retry, context. Engine bơm vào qua observer.workerProgress.
func (o *observer) handleToolUpdate(ev agentcore.Event) {
	if ev.Progress == nil {
		return
	}
	// toolMu phủ toàn bộ đường xử lý progress: luồng Worker Engine và luồng can thiệp Arbiter
	// phía Host đến đây đồng thời (xem chi tiết ở comment observer.toolMu), khóa bảo đảm map/biến
	// vô hướng nhất quán và thứ tự sự kiện trong cùng một callback progress nguyên tử hóa.
	o.toolMu.Lock()
	defer o.toolMu.Unlock()
	switch ev.Progress.Kind {
	case agentcore.ProgressToolDelta:
		if ev.Progress.Delta != "" {
			o.handleSubagentDelta(ev.Progress)
		}
	case agentcore.ProgressToolStart:
		// Lời gọi tool bên trong Worker (như writer → draft_chapter).
		// Lưu ý: dòng TOOL có thể đã được phát sớm ở giai đoạn nhận diện streaming bởi handleSubagentDelta.
		// Ở đây: nếu đã phát → chỉ cập nhật summary (args lúc này đầy đủ, hiển thị được "tool(chương N)"); nếu chưa → phát bình thường.
		if ev.Progress.Agent == "" || ev.Progress.Tool == "" {
			break
		}
		toolName := displayToolName(ev.Progress.Tool, ev.Progress.Args)
		if _, ok := o.toolStarts[ev.Progress.Agent]; ok {
			o.updateToolCallSummary(ev.Progress.Agent, ev.Progress.Tool, toolName)
			o.updateAgent(ev.Progress.Agent, func(a *agentState) {
				a.state = "working"
				a.tool = ev.Progress.Tool
				a.summary = fmt.Sprintf("%s → %s", ev.Progress.Agent, toolName)
			})
			break
		}
		// Chưa phát sớm → quy trình bình thường
		// (model có tool args không streaming sẽ không kích hoạt ensureSubagentToolStarted,
		// fallback header phải bù trên đường này, nếu không tool không có extractor như read_chapter
		// sẽ không có ✻ header trên panel streaming, dính sát đoạn thinking phía trước.)
		id := nextEventID()
		o.toolStarts[ev.Progress.Agent] = &activeCall{id: id, start: time.Now(), summary: toolName, depth: 1}
		o.emitAndLog(Event{
			ID:       id,
			Time:     time.Now(),
			Category: "TOOL",
			Agent:    ev.Progress.Agent,
			Summary:  toolName,
			Level:    "info",
			Depth:    1,
		})
		o.updateAgent(ev.Progress.Agent, func(a *agentState) {
			a.state = "working"
			a.tool = ev.Progress.Tool
			a.summary = fmt.Sprintf("%s → %s", ev.Progress.Agent, toolName)
		})
		o.emitFallbackStreamHeader(ev.Progress.Tool)
	case agentcore.ProgressToolEnd:
		delete(o.streamExtractors, ev.Progress.Agent)
		if ev.Progress.Agent == "" {
			return
		}
		call, ok := o.toolStarts[ev.Progress.Agent]
		if !ok {
			return
		}
		delete(o.toolStarts, ev.Progress.Agent)
		// Cập nhật sự kiện cùng ID: TUI định vị dòng TOOL gốc theo ID, điền lại FinishedAt / Duration.
		// Kèm cả Summary / Depth, bảo đảm replay runtime queue phục hồi được dòng đầy đủ.
		finishEv := Event{
			ID:         call.id,
			Time:       call.start,
			FinishedAt: time.Now(),
			Category:   "TOOL",
			Agent:      ev.Progress.Agent,
			Summary:    call.summary,
			Level:      "info",
			Depth:      call.depth,
			Duration:   time.Since(call.start),
		}
		o.emitEv(finishEv)
		o.persistEvent(finishEv)
	case agentcore.ProgressThinking:
		o.handleThinkingProgress(ev)
	case agentcore.ProgressRetry:
		// Chỉ hiển thị thời gian chờ thực tế mà upstream báo rõ, tránh ước lượng local lệch nhịp backoff thật.
		// Summary không nhúng độ trễ tĩnh — UI đếm ngược từng giây theo RetryAt; Detail/log giữ snapshot độ trễ lúc phát.
		delay := retryProgressDelay(ev.Progress)
		retryEv := Event{
			ID:       o.retryEventID(ev.Progress.Agent, ev.Progress.Attempt),
			Time:     time.Now(),
			Category: "SYSTEM",
			Agent:    ev.Progress.Agent,
			Summary:  retryPrefix(ev.Progress.Attempt, ev.Progress.MaxRetries, 0) + truncate(ev.Progress.Message, 80),
			Detail:   retryPrefix(ev.Progress.Attempt, ev.Progress.MaxRetries, delay) + ev.Progress.Message,
			Kind:     errorKind(nil, ev.Progress.Message),
			Level:    "warn",
			Depth:    1,
		}
		if delay > 0 {
			retryEv.RetryAt = retryEv.Time.Add(delay)
		}
		o.emitEv(retryEv)
		o.persistEvent(retryEv)
	case agentcore.ProgressToolError:
		delete(o.streamExtractors, ev.Progress.Agent)
		msg := ev.Progress.Message
		if msg == "" {
			msg = "unknown error"
		}
		// Nếu có dòng TOOL đang thực hiện, đánh dấu thất bại tại chỗ và đặt lỗi đầy đủ vào Detail.
		// Cùng một lần thất bại chỉ sinh một sự kiện cấp ERROR, tránh trạng thái TOOL thất bại và ERROR chi tiết
		// kèm theo bị đọc nhầm thành hai sự cố độc lập trong tui.log.
		if call, ok := o.toolStarts[ev.Progress.Agent]; ok {
			delete(o.toolStarts, ev.Progress.Agent)
			detail := fmt.Sprintf("%s lỗi: %s", ev.Progress.Tool, msg)
			finishEv := Event{
				ID:         call.id,
				Time:       call.start,
				FinishedAt: time.Now(),
				Failed:     true,
				Category:   "TOOL",
				Agent:      ev.Progress.Agent,
				Summary:    fmt.Sprintf("%s lỗi: %s", call.summary, truncate(msg, 100)),
				Detail:     detail,
				Kind:       errorKind(nil, msg),
				Level:      "error",
				Depth:      call.depth,
				Duration:   time.Since(call.start),
			}
			o.emitEv(finishEv)
			o.persistEvent(finishEv)
			return
		}
		// Rất hiếm: dòng progress thiếu start thì không cập nhật tại chỗ được, giữ ERROR độc lập để lộ sự cố.
		errEv := Event{
			Time:     time.Now(),
			Category: "ERROR",
			Agent:    ev.Progress.Agent,
			Summary:  fmt.Sprintf("%s lỗi: %s", ev.Progress.Tool, truncate(msg, 100)),
			Detail:   fmt.Sprintf("%s lỗi: %s", ev.Progress.Tool, msg),
			Kind:     errorKind(nil, msg),
			Level:    "error",
			Depth:    1,
		}
		o.emitEv(errEv)
		o.persistEvent(errEv)
	case agentcore.ProgressContext:
		o.handleContextProgress(ev)
	}
}

func retryProgressDelay(p *agentcore.ProgressPayload) time.Duration {
	if p == nil {
		return 0
	}
	if len(p.Meta) > 0 {
		var meta struct {
			DelayMS int64 `json:"retry_delay_ms"`
		}
		if json.Unmarshal(p.Meta, &meta) == nil && meta.DelayMS > 0 {
			return time.Duration(meta.DelayMS) * time.Millisecond
		}
	}
	return 0
}

func dispatchSummary(agent, task string) string {
	if agent == "" {
		agent = "subagent"
	}
	if task == "" {
		return agent
	}
	firstLine := strings.TrimSpace(strings.SplitN(task, "\n", 2)[0])
	if firstLine == "" {
		return agent
	}
	return agent + "（" + truncate(firstLine, 30) + "）"
}

func dispatchDetail(task, reason string) string {
	var parts []string
	if strings.TrimSpace(reason) != "" {
		parts = append(parts, "Lý do phân phát: "+reason)
	}
	if strings.TrimSpace(task) != "" {
		parts = append(parts, "Nhiệm vụ đầy đủ:\n"+task)
	}
	return strings.Join(parts, "\n")
}

func (o *observer) updateToolCallSummary(agent, tool, summary string) {
	if agent == "" || summary == "" {
		return
	}
	call, ok := o.toolStarts[agent]
	if !ok || call.summary == summary {
		return
	}
	call.summary = summary
	o.emitEv(Event{
		ID:       call.id,
		Time:     call.start,
		Category: "TOOL",
		Agent:    agent,
		Summary:  summary,
		Level:    "info",
		Depth:    call.depth,
	})
	o.updateAgent(agent, func(a *agentState) {
		a.state = "working"
		a.tool = tool
		a.summary = fmt.Sprintf("%s → %s", agent, summary)
	})
}

func (o *observer) updateToolCallSummaryFromDelta(agent, tool, delta string) {
	key := streamArgKey(agent, tool)
	prefix := o.streamArgPrefixes[key] + delta
	if len(prefix) > 512 {
		prefix = prefix[:512]
	}
	o.streamArgPrefixes[key] = prefix

	summary := streamedToolLabel(tool, prefix)
	if summary == "" {
		return
	}
	if o.streamArgLabels[key] == summary {
		return
	}
	o.streamArgLabels[key] = summary
	o.updateToolCallSummary(agent, tool, summary)
}

func streamArgKey(agent, tool string) string {
	return agent + "\x00" + tool
}

func streamedToolLabel(tool, delta string) string {
	if tool != "save_foundation" || delta == "" {
		return ""
	}
	typ := firstJSONStringField(delta, "type")
	if typ == "" {
		return ""
	}
	return fmt.Sprintf("%s[%s]", tool, typ)
}

func firstJSONStringField(raw, field string) string {
	needle := `"` + field + `"`
	idx := strings.Index(raw, needle)
	if idx < 0 {
		return ""
	}
	rest := raw[idx+len(needle):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return ""
	}
	rest = strings.TrimLeft(rest[colon+1:], " \t\r\n")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	var value strings.Builder
	escape := false
	for i := 1; i < len(rest); i++ {
		c := rest[i]
		if escape {
			value.WriteByte(c)
			escape = false
			continue
		}
		switch c {
		case '\\':
			escape = true
		case '"':
			return value.String()
		default:
			value.WriteByte(c)
		}
	}
	return ""
}

func (o *observer) emitCallFinish(call *activeCall, category, agentName string, callErr error) {
	if call == nil {
		return
	}
	failed := callErr != nil
	level := "success"
	if failed {
		level = "error"
	}
	summary := call.summary
	detail := ""
	kind := ""
	if failed {
		detail = callErr.Error()
		kind = errorKind(callErr, detail)
		summary = fmt.Sprintf("%s lỗi: %s", call.summary, truncate(detail, 100))
	}
	finishEv := Event{
		ID:         call.id,
		Time:       call.start,
		FinishedAt: time.Now(),
		Failed:     failed,
		Category:   category,
		Agent:      agentName,
		Summary:    summary,
		Detail:     detail,
		Kind:       kind,
		Level:      level,
		Depth:      call.depth,
		Duration:   time.Since(call.start),
	}
	o.emitEv(finishEv)
	o.persistEvent(finishEv)
}

func displayToolName(tool string, args json.RawMessage) string {
	if len(args) == 0 {
		return tool
	}
	switch tool {
	case "save_foundation":
		var p struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(args, &p) == nil && p.Type != "" {
			return fmt.Sprintf("%s[%s]", tool, p.Type)
		}
	case "commit_chapter", "plan_chapter", "draft_chapter", "check_consistency":
		var p struct {
			Chapter int `json:"chapter"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			return fmt.Sprintf("%s(chương %d)", tool, p.Chapter)
		}
	case "save_review":
		var p struct {
			Chapter int    `json:"chapter"`
			Scope   string `json:"scope"`
			Verdict string `json:"verdict"`
		}
		if json.Unmarshal(args, &p) == nil {
			label := ""
			switch p.Scope {
			case "arc":
				label = "cung hiện tại"
			case "global":
				label = "toàn cục"
			default:
				if p.Chapter > 0 {
					label = fmt.Sprintf("chương %d", p.Chapter)
				}
			}
			if label == "" {
				return tool
			}
			if p.Verdict != "" {
				return fmt.Sprintf("%s(%s·%s)", tool, label, p.Verdict)
			}
			return fmt.Sprintf("%s(%s)", tool, label)
		}
	case "novel_context":
		var p struct {
			Chapter int `json:"chapter"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			return fmt.Sprintf("%s(chương %d)", tool, p.Chapter)
		}
	case "read_chapter":
		var p struct {
			Chapter   int    `json:"chapter"`
			Source    string `json:"source"`
			Character string `json:"character"`
		}
		if json.Unmarshal(args, &p) == nil && p.Chapter > 0 {
			suffix := ""
			if p.Character != "" {
				suffix = "·" + p.Character + " đối thoại"
			} else if p.Source == "draft" {
				suffix = "·bản nháp"
			}
			return fmt.Sprintf("%s(chương %d%s)", tool, p.Chapter, suffix)
		}
	}
	return tool
}
