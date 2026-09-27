package host

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
)

// Đồng sáng tạo khởi động nguội: làm rõ nhu cầu từ số 0, tạo ra chỉ lệnh sáng tác cho cả cuốn sách.
const coCreateSystemPrompt = `Bạn là trợ lý đồng sáng tác tiểu thuyết. Nhiệm vụ của bạn không phải là bắt đầu viết ngay, mà là qua nhiều vòng đối thoại ngắn giúp người dùng làm rõ nhu cầu sáng tác, và liên tục chắp thành một chỉ lệnh sáng tác tiếng Việt có thể giao thẳng cho engine sáng tác.

Mỗi vòng trả lời xuất строго theo định dạng XML dưới đây, gồm bốn thẻ, xuất hiện lần lượt, thẻ nào cũng phải có cặp mở/đóng đúng:

<reply>
Phản hồi tự nhiên tiếng Việt cho người dùng xem: trước hết đáp lại nội dung người dùng nhập, sau đó đặt tối đa 1 đến 2 câu hỏi then chốt nhất hiện tại. Nếu thông tin đã đủ để bắt đầu sáng tác, báo người dùng có thể nhấn Ctrl+S để bắt đầu.
</reply>

<draft>
Bản nháp chỉ lệnh sáng tác đầy đủ hiện tại, dùng Markdown: bắt đầu thẳng từ đề mục cấp hai, ví dụ "## Chủ đề", "## Yếu tố then chốt", "## Thông tin cần làm rõ"; liệt kê ý bằng gạch đầu dòng. Mỗi vòng đều phải **cập nhật cộng dồn** trên kết luận sẵn có, hấp thụ ý định mới nhất của người dùng; kể cả vòng này không có nội dung mới cũng phải viết lại nguyên vẹn bản nháp đầy đủ — không được lược bớt, không được viết các chỗ đứng kiểu "(giữ như vòng trước)".
</draft>
` + coCreateProtocolTail

// Đồng sáng tạo theo giai đoạn: tiểu thuyết đã viết được một phần, lập hướng đi cho "giai đoạn tiếp theo". Bên gọi cần
// nối tóm tắt trạng thái truyện hiện tại vào sau prompt này (đoạn "## Trạng thái truyện hiện tại"), để model lập kế hoạch trên nền nội dung đã viết.
const stageCoCreateSystemPrompt = `Bạn là trợ lý "đồng sáng tác theo giai đoạn" của một cuốn tiểu thuyết. Cuốn tiểu thuyết này đã viết được một phần (tiến độ xem ở "trạng thái truyện hiện tại" bên dưới). Người dùng tạm dừng lại, muốn cùng bạn bàn hướng đi của "giai đoạn tiếp theo", rồi tiếp tục sáng tác.

Nhiệm vụ của bạn không phải viết tiếp phần thân, mà là qua nhiều vòng đối thoại ngắn giúp người dùng nghĩ rõ đoạn tiếp theo (vài chương tới / cung kế tiếp / quyển kế tiếp) sẽ đi về đâu, và liên tục chắp thành một "brief hướng đi tiếp theo", để engine sáng tác dựa vào đó mà triển khai.

Luật sắt: mọi đề xuất phải nhất quán với cốt truyện, nhân vật, phục bút đã xảy ra trong "trạng thái truyện hiện tại", tuyệt đối không lật đổ hay bỏ qua nội dung đã viết; chỉ lập kế hoạch "đoạn sau đi thế nào", không thiết kế lại cả cuốn sách.

Mỗi vòng trả lời xuất строго theo định dạng XML dưới đây, gồm bốn thẻ, xuất hiện lần lượt, thẻ nào cũng phải có cặp mở/đóng đúng:

<reply>
Phản hồi tự nhiên tiếng Việt cho người dùng xem: trước hết đáp lại nội dung người dùng nhập, sau đó đặt tối đa 1 đến 2 câu hỏi then chốt nhất hiện tại. Nếu hướng đi tiếp theo đã đủ rõ, báo người dùng có thể nhấn Ctrl+S giao hướng đi cho engine sáng tác, tiếp tục sáng tác.
</reply>

<draft>
"Brief hướng đi tiếp theo" đầy đủ hiện tại, dùng Markdown: bắt đầu thẳng từ đề mục cấp hai, ví dụ "## Hướng đi tiếp theo", "## Bước ngoặt then chốt", "## Phục bút cần thu", "## Nhịp và dung lượng"; liệt kê ý bằng gạch đầu dòng. Mỗi vòng đều phải **cập nhật cộng dồn** trên kết luận sẵn có, hấp thụ ý định mới nhất của người dùng; kể cả vòng này không có nội dung mới cũng phải viết lại nguyên vẹn brief đầy đủ — không được lược bớt, không được viết các chỗ đứng kiểu "(giữ như vòng trước)".
</draft>
` + coCreateProtocolTail

// coCreateProtocolTail là đuôi protocol xuất dùng chung cho hai chế độ đồng sáng tác (<ready> / <suggestions> + đặc tả xuất).
// Hai chế độ chỉ khác nhau ở ngữ cảnh mở màn và ngữ nghĩa <draft>, protocol hoàn toàn giống nhau.
const coCreateProtocolTail = `
<ready>false</ready>

<suggestions>
1-3 câu "người dùng có thể muốn nói tiếp", mỗi câu một dòng bắt đầu bằng "- ". Đây là dẫn dắt khi người dùng bí ý,
bấm phím số để điền vào ô nhập, người dùng có thể chỉnh sửa rồi mới gửi.

Yêu cầu:
- Viết theo giọng người dùng, như lời người dùng nói với bạn, đừng viết thành câu hỏi ngược của trợ lý.
- Mỗi câu không quá 25 chữ, đa dạng mẫu câu, tránh nhàm chán.
- Đưa xu hướng / lựa chọn / ý định bổ sung, không thay người dùng viết trọn thiết lập bằng một câu.
</suggestions>

Đặc tả xuất:
- Phải dùng bốn thẻ XML: <reply> / <draft> / <ready> / <suggestions>, thẻ nào cũng phải mở/đóng trọn vẹn.
- Tên thẻ chỉ được chữ thường tiếng Anh, không được đổi thành <REPLY> / <REWRITE> / <trả lời> hay bất kỳ biến thể nào.
- Ngoài thẻ không thêm bất kỳ giải thích, suy nghĩ hay rào code nào.
- Trong <draft> cho phép Markdown nhiều dòng, viết xuống dòng thẳng, không cần thoát ký tự nào.
- <ready> chỉ ghi true hoặc false. Thông tin đã đủ thì điền true.
- Khi <ready>true</ready> thì <suggestions> có thể rỗng (giữ thẻ rỗng <suggestions></suggestions> là được).`

// CoCreateProgressKind nhận diện loại nội dung của callback streaming.
const (
	CoCreateProgressThinking = "thinking"
	CoCreateProgressReply    = "reply"
)

// Xuất bốn thẻ XML. Phong cách XML bền hơn marker ngoặc vuông — dữ liệu huấn luyện của Claude/GPT có
// rất nhiều dạng <thinking>...</thinking>, model gần như không bao giờ đổi <reply> thành <REWRITE>
// hay biến thể khác; thẻ đóng cũng giúp cắt giữa dòng streaming chính xác hơn (không phụ thuộc việc tìm marker kế tiếp để cắt đuôi).
const (
	tagReply       = "reply"
	tagDraft       = "draft"
	tagReady       = "ready"
	tagSuggestions = "suggestions"
)

func coCreateStream(ctx context.Context, models *bootstrap.ModelSet, sessions *store.SessionStore, sysPrompt string, history []CoCreateMessage, onProgress func(kind, text string), record func(agentName, task string, msg agentcore.AgentMessage)) (reply CoCreateReply, err error) {
	if len(history) == 0 {
		return CoCreateReply{}, fmt.Errorf("cocreate history is empty")
	}

	// Role "thinking" là model phục vụ phỏng vấn đồng sáng tác (mặc định rơi về Default,
	// cấu hình được qua roles.thinking). Trước đây đường này không ghi sổ usage — chi phí
	// phỏng vấn vô hình với meta/usage.json và ngân sách; nay ghi qua callback record.
	model := models.ForRole("thinking")

	msgs := []agentcore.Message{agentcore.SystemMsg(sysPrompt)}
	for _, item := range history {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(item.Role)) {
		case "assistant":
			msgs = append(msgs, assistantMsg(content))
		default:
			msgs = append(msgs, agentcore.UserMsg(content))
		}
	}

	var raw, thinking strings.Builder

	// Để chẩn đoán các lỗi thỉnh thoảng gặp như "cocreate empty response" cần thấy model thực tế trả về gì.
	// Mỗi vòng ghi đĩa toàn bộ vào <output>/meta/sessions/cocreate.jsonl, cùng vị trí với log session sáng tác chính.
	start := time.Now()
	defer func() {
		if sessions == nil {
			return
		}
		if logErr := sessions.LogCoCreate(coCreateLogEntry{
			Time:         time.Now(),
			DurationMS:   time.Since(start).Milliseconds(),
			InputHistory: history,
			RawResponse:  raw.String(),
			RawLen:       len([]rune(raw.String())),
			Thinking:     thinking.String(),
			ParsedReply:  reply.Message,
			ParsedDraft:  reply.Prompt,
			ParsedReady:  reply.Ready,
			ParsedSugs:   reply.Suggestions,
			Error:        errString(err),
		}); logErr != nil {
			slog.Warn("Ghi đĩa log phiên đồng sáng tác thất bại", "module", "cocreate", "err", logErr)
		}
	}()

	streamCh, err := model.GenerateStream(ctx, msgs, nil, agentcore.WithMaxTokens(2048))
	if err != nil {
		return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", err)
	}

	var streamed bool
	for ev := range streamCh {
		switch ev.Type {
		case agentcore.StreamEventThinkingDelta:
			thinking.WriteString(ev.Delta)
			if onProgress != nil {
				onProgress(CoCreateProgressThinking, thinking.String())
			}
		case agentcore.StreamEventTextDelta:
			streamed = true
			raw.WriteString(ev.Delta)
			if onProgress != nil {
				onProgress(CoCreateProgressReply, extractReplyPreview(raw.String()))
			}
		case agentcore.StreamEventDone:
			if record != nil {
				record("cocreate", "", ev.Message)
			}
			if !streamed {
				raw.WriteString(ev.Message.TextContent())
			}
		case agentcore.StreamEventError:
			if ev.Err != nil {
				return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", ev.Err)
			}
			return CoCreateReply{}, fmt.Errorf("cocreate generate failed")
		}
	}

	// Channel fallback: model suy luận (R1/GLM-Z1/QwQ v.v.) thỉnh thoảng viết trọn câu trả lời vào
	// reasoning_content xong không chuyển lại kênh final answer, khiến raw rỗng nhưng thinking chứa
	// đủ bốn đoạn. Thực tế xem meta/sessions/cocreate.jsonl — lấy thinking làm raw để parse luôn,
	// tầng protocol đã có xử lý downgrade (không có marker [REPLY] thì nguyên đoạn coi là reply), cứu xong trải nghiệm UI không khác gì.
	rawText := raw.String()
	if strings.TrimSpace(rawText) == "" {
		if t := strings.TrimSpace(thinking.String()); t != "" {
			rawText = t
		}
	}
	reply, err = parseCoCreateResponse(rawText)
	return reply, err
}

// coCreateLogEntry là cấu trúc một dòng ghi vào meta/sessions/cocreate.jsonl.
// Tên trường bám theo thói quen truy vấn trực tiếp jsonl (snake_case), tiện lọc bằng jq.
type coCreateLogEntry struct {
	Time         time.Time         `json:"time"`
	DurationMS   int64             `json:"duration_ms"`
	InputHistory []CoCreateMessage `json:"input_history"`
	RawResponse  string            `json:"raw_response"`
	RawLen       int               `json:"raw_len"`
	Thinking     string            `json:"thinking,omitempty"`
	ParsedReply  string            `json:"parsed_reply"`
	ParsedDraft  string            `json:"parsed_draft"`
	ParsedReady  bool              `json:"parsed_ready"`
	ParsedSugs   []string          `json:"parsed_sugs,omitempty"`
	Error        string            `json:"error,omitempty"`
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func assistantMsg(text string) agentcore.Message {
	return agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(text)},
		Timestamp: time.Now(),
	}
}

// parseCoCreateResponse parse đầu ra theo thẻ XML. Nếu model không tuân protocol (nói tự nhiên thẳng),
// nguyên đoạn hiển thị như reply, draft để rỗng để session giữ lại vòng trước.
func parseCoCreateResponse(raw string) (CoCreateReply, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CoCreateReply{}, fmt.Errorf("cocreate empty response")
	}

	reply, draft, ready, suggestions := splitCoCreateMarkers(raw)
	if reply == "" {
		// Model không tuân protocol XML: nguyên đoạn coi là reply.
		return CoCreateReply{Message: raw, Prompt: "", Ready: false, Raw: raw}, nil
	}
	return CoCreateReply{
		Message:     reply,
		Prompt:      draft,
		Ready:       ready,
		Suggestions: suggestions,
		Raw:         raw,
	}, nil
}

// splitCoCreateMarkers cắt văn bản theo bốn thẻ XML.
// Thẻ có thể thiếu (giữa dòng streaming hoặc model bỏ sót), phần thiếu tương ứng rỗng / false / nil.
// Thiếu thẻ đóng thì extractTagContent lấy tới cuối chuỗi, vẫn cố parse.
func splitCoCreateMarkers(s string) (reply, draft string, ready bool, suggestions []string) {
	reply = extractTagContent(s, tagReply)
	draft = extractTagContent(s, tagDraft)
	readyStr := strings.ToLower(extractTagContent(s, tagReady))
	ready = readyStr == "true" || readyStr == "yes"
	suggestions = parseSuggestions(extractTagContent(s, tagSuggestions))
	return
}

// extractTagContent moi phần văn bản giữa <tag>...</tag> từ s.
// Ba kịch bản sự cố thỉnh thoảng gặp được xử lý fallback, tránh đi thẳng downgrade mất trường:
//  1. Có mở không có đóng (giữa dòng streaming) → cắt tới trước thẻ mở đã biết kế tiếp
//  2. Không có mở mà có đóng (model typo, ví dụ <suggestions> viết thành <uggestions>) → bắt đầu từ vị trí
//     kết thúc của thẻ đóng đã biết hoàn chỉnh gần nhất, tới trước </tag>
//  3. reply hoàn toàn không có thẻ mở (model mở đầu bằng tự nhiên, cuối dán </reply>) → từ đầu tới </reply>
func extractTagContent(s, tag string) string {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	oIdx := strings.Index(s, open)
	if oIdx >= 0 {
		rest := s[oIdx+len(open):]
		if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
			return strings.TrimSpace(rest[:cIdx])
		}
		// Có mở không có đóng → cắt tới trước thẻ mở đã biết kế tiếp
		for _, other := range []string{"<reply>", "<draft>", "<ready>", "<suggestions>"} {
			if other == open {
				continue
			}
			if idx := strings.Index(rest, other); idx >= 0 {
				rest = rest[:idx]
			}
		}
		return strings.TrimSpace(rest)
	}

	// Không có mở mà có đóng → bắt đầu từ vị trí kết thúc thẻ đóng đã biết hoàn chỉnh gần nhất, tới </tag>.
	if cIdx := strings.Index(s, closeTag); cIdx >= 0 {
		prefix := s[:cIdx]
		start := 0
		for _, t := range []string{"</reply>", "</draft>", "</ready>", "</suggestions>"} {
			if t == closeTag {
				continue
			}
			if i := strings.LastIndex(prefix, t); i >= 0 {
				if end := i + len(t); end > start {
					start = end
				}
			}
		}
		return strings.TrimSpace(prefix[start:])
	}
	return ""
}

// parseSuggestions moi từng dòng trong đoạn <suggestions>, bỏ các tiền tố danh sách "- " / "* " / "1. ".
// Giữ tối đa 3 câu; dòng trống, quá ngắn (<2 ký tự), nguyên dòng giống thẻ XML (tàn dư fallback thẻ
// mở typo, ví dụ <uggestions>) bị bỏ.
func parseSuggestions(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Nguyên dòng giống thẻ XML → bỏ qua (chống tàn dư thẻ mở typo làm bẩn)
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
			continue
		}
		// Bóc tiền tố danh sách
		switch {
		case strings.HasPrefix(line, "- "):
			line = strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "* "):
			line = strings.TrimSpace(line[2:])
		case isOrderedSuggestion(line):
			line = stripOrderedPrefix(line)
		}
		if len([]rune(line)) < 2 {
			continue
		}
		out = append(out, line)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

// isOrderedSuggestion xét đầu dòng có dạng "1. " / "12. " (số + dấu chấm + khoảng trắng) hay không.
func isOrderedSuggestion(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
}

func stripOrderedPrefix(line string) string {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(line) {
		return line
	}
	return strings.TrimSpace(line[i+2:])
}

// extractReplyPreview preview streaming: khi raw còn đang dài ra, đưa cho UI một đoạn hiển thị được.
// Tìm nội dung sau <reply>, cắt tới </reply> hoặc trước thẻ mở <draft> kế tiếp.
// Model tuân thủ một nửa (thiếu thẻ mở <reply>) thì từ đầu tới </reply> hoặc <draft> đều tính là reply.
func extractReplyPreview(raw string) string {
	trimmed := strings.TrimSpace(raw)
	open := "<" + tagReply + ">"
	closeTag := "</" + tagReply + ">"
	draftOpen := "<" + tagDraft + ">"

	rest := trimmed
	if rIdx := strings.Index(trimmed, open); rIdx >= 0 {
		rest = trimmed[rIdx+len(open):]
	}
	if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
		return strings.TrimSpace(rest[:cIdx])
	}
	if dIdx := strings.Index(rest, draftOpen); dIdx >= 0 {
		rest = rest[:dIdx]
	}
	return strings.TrimSpace(rest)
}
