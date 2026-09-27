package startup

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/host"
)

// CoCreateSession chứa trạng thái phi UI của chế độ đồng sáng tạo.
type CoCreateSession struct {
	history        []host.CoCreateMessage
	draftPrompt    string
	ready          bool
	streamReply    string
	streamThinking string
	suggestions    []string
}

func NewCoCreateSession(initial string) *CoCreateSession {
	return &CoCreateSession{
		history: []host.CoCreateMessage{
			{Role: "user", Content: strings.TrimSpace(initial)},
		},
	}
}

func (s *CoCreateSession) History() []host.CoCreateMessage {
	if s == nil {
		return nil
	}
	return append([]host.CoCreateMessage(nil), s.history...)
}

func (s *CoCreateSession) ApplyReply(reply host.CoCreateReply) {
	if s == nil {
		return
	}
	s.streamReply = ""
	s.streamThinking = ""
	// Trong history, assistant lưu đủ ba đoạn Raw (gồm [DRAFT]), vòng sau mô hình mới
	// thấy được bản nháp mình viết vòng trước và tích lũy cập nhật trên đó; chỉ lưu
	// Message thì [DRAFT] hoàn toàn không vào ngữ cảnh, mỗi vòng mô hình chỉ đúc kết
	// lại từ hội thoại, chi tiết giai đoạn sớm dễ mất. Trên đường hạ cấp, Raw == Message,
	// tương đương.
	text := strings.TrimSpace(reply.Raw)
	if text == "" {
		text = strings.TrimSpace(reply.Message)
	}
	if text != "" {
		s.history = append(s.history, host.CoCreateMessage{Role: "assistant", Content: text})
	}
	// Chỉ khi Prompt khác rỗng mới ghi đè draft: đường hạ cấp của parse trả về Prompt="",
	// lúc đó phải giữ draft vòng trước, nếu không "chỉ thị sáng tác hiện tại" người dùng
	// đã tích lũy sẽ bị câu trả lời bị cắt cụt xóa sạch.
	if prompt := strings.TrimSpace(reply.Prompt); prompt != "" {
		s.draftPrompt = prompt
	}
	s.ready = reply.Ready
	// suggestions ghi đè trực tiếp (kể cả ghi đè bằng rỗng): lời gợi ý mỗi vòng chỉ có
	// ý nghĩa với thời điểm đó.
	s.suggestions = append(s.suggestions[:0], reply.Suggestions...)
}

func (s *CoCreateSession) AppendUser(text string) {
	if s == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	// Người dùng đã quyết định câu tiếp theo nói gì, suggestions lập tức vô hiệu, tránh
	// khi AI chưa trả lời thì gợi ý cũ còn treo trên ô nhập gây hiểu lầm.
	s.suggestions = nil
	s.history = append(s.history, host.CoCreateMessage{Role: "user", Content: text})
}

// ApplyDelta tiếp nhận tích lũy luồng; kind="thinking" ghi vào luồng suy luận,
// "reply" ghi vào bản xem trước trả lời. Hai luồng tích lũy riêng, UI có thể tô màu
// theo khối, để người dùng ngay trong giai đoạn thinking cũng thấy LLM đang làm việc.
func (s *CoCreateSession) ApplyDelta(kind, text string) {
	if s == nil {
		return
	}
	text = strings.TrimSpace(text)
	switch kind {
	case host.CoCreateProgressThinking:
		s.streamThinking = text
	case host.CoCreateProgressReply:
		s.streamReply = text
	}
}

func (s *CoCreateSession) StreamReply() string {
	if s == nil {
		return ""
	}
	return s.streamReply
}

func (s *CoCreateSession) StreamThinking() string {
	if s == nil {
		return ""
	}
	return s.streamThinking
}

func (s *CoCreateSession) DraftPrompt() string {
	if s == nil {
		return ""
	}
	return s.draftPrompt
}

func (s *CoCreateSession) Suggestions() []string {
	if s == nil {
		return nil
	}
	return s.suggestions
}

func (s *CoCreateSession) Ready() bool {
	if s == nil {
		return false
	}
	return s.ready
}

func (s *CoCreateSession) CanStart() bool {
	return strings.TrimSpace(s.DraftPrompt()) != ""
}

func (s *CoCreateSession) InitialInput() string {
	if s == nil || len(s.history) == 0 {
		return ""
	}
	return strings.TrimSpace(s.history[0].Content)
}

func (s *CoCreateSession) BuildPrompt() (string, error) {
	if s == nil || !s.CanStart() {
		return "", fmt.Errorf("cocreate draft prompt is required")
	}
	return s.DraftPrompt(), nil
}
