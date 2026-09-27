package utils

import "strings"

// JSONFieldExtractor trích xuất giá trị chuỗi của trường chỉ định từ các mảnh JSON stream.
//
// Khi LLM stream tạo tool call, tham số có thể đến từng phần (OpenAI/Anthropic)
// hoặc một lần (Gemini). Trình trích xuất này dùng máy trạng thái quét từng ký tự,
// phát hiện key đích rồi trích giá trị chuỗi, xử lý escape JSON.
type JSONFieldExtractor struct {
	key      string // khớp đích, vd `"content"` hoặc `"task"`
	state    extractState
	matchPos int
	escape   bool
	buf      strings.Builder
}

type extractState int

const (
	stateScan    extractState = iota // quét, tìm key đích
	stateColon                       // đã khớp key, chờ dấu hai chấm và dấu ngoặc mở
	stateExtract                     // đang trích giá trị chuỗi
)

func NewFieldExtractor(fieldName string) *JSONFieldExtractor {
	return &JSONFieldExtractor{key: `"` + fieldName + `"`}
}

// Feed xử lý một delta, trả về văn bản trích được (có thể rỗng).
func (e *JSONFieldExtractor) Feed(delta string) string {
	e.buf.Reset()
	for _, r := range delta {
		switch e.state {
		case stateScan:
			e.feedScan(r)
		case stateColon:
			e.feedColon(r)
		case stateExtract:
			e.feedExtract(r)
		}
	}
	return e.buf.String()
}

func (e *JSONFieldExtractor) feedScan(r rune) {
	if e.matchPos < len(e.key) && byte(r) == e.key[e.matchPos] {
		e.matchPos++
		if e.matchPos == len(e.key) {
			e.state = stateColon
			e.matchPos = 0
		}
		return
	}
	e.matchPos = 0
	if byte(r) == e.key[0] {
		e.matchPos = 1
	}
}

func (e *JSONFieldExtractor) feedColon(r rune) {
	switch r {
	case ':', ' ', '\t':
		// bỏ qua
	case '"':
		e.state = stateExtract
		e.escape = false
	default:
		e.state = stateScan
		e.matchPos = 0
		if byte(r) == e.key[0] {
			e.matchPos = 1
		}
	}
}

func (e *JSONFieldExtractor) feedExtract(r rune) {
	if e.escape {
		e.escape = false
		switch r {
		case 'n':
			e.buf.WriteByte('\n')
		case 't':
			e.buf.WriteByte('\t')
		case 'r':
			e.buf.WriteByte('\r')
		case '"', '\\', '/':
			e.buf.WriteRune(r)
		default:
			e.buf.WriteByte('\\')
			e.buf.WriteRune(r)
		}
		return
	}
	switch r {
	case '\\':
		e.escape = true
	case '"':
		e.state = stateScan
		e.matchPos = 0
	default:
		e.buf.WriteRune(r)
	}
}

// Reset trạng thái (gọi khi vòng tin nhắn LLM mới).
func (e *JSONFieldExtractor) Reset() {
	e.state = stateScan
	e.matchPos = 0
	e.escape = false
}

// ThinkingSep là dấu phân cách giữa văn bản suy nghĩ và nội dung chính.
// StreamFilter chèn dấu này trước đoạn suy nghĩ, TUI dùng nó để chuyển kiểu hiển thị.
const ThinkingSep = "\x02"

// StreamFilter phân biệt phản hồi văn bản và lời gọi tool JSON của SubAgent.
// Phản hồi văn bản được đánh dấu là nội dung suy nghĩ (prefix ThinkingSep);
// lời gọi tool JSON chỉ trích trường được chỉ định.
//
// Tiêu chí phân biệt: gặp { thì vào chế độ JSON (theo dõi độ sâu dấu ngoặc),
// độ sâu về 0 thì quay lại chế độ văn bản.
type StreamFilter struct {
	fieldExt   *JSONFieldExtractor
	mode       filterMode
	braceDepth int
	inString   bool // trong chuỗi JSON (dấu ngoặc nhọn không đếm)
	escJSON    bool // escape trong chuỗi JSON
	thinking   bool // đang ở đoạn văn bản suy nghĩ
	buf        strings.Builder
}

type filterMode int

const (
	filterText filterMode = iota // phản hồi văn bản, truyền thẳng
	filterJSON                   // lời gọi tool JSON, trích trường đích
)

func NewStreamFilter(fieldName string) *StreamFilter {
	return &StreamFilter{fieldExt: NewFieldExtractor(fieldName)}
}

// Feed xử lý một delta, trả về văn bản hiển thị được.
// Phản hồi văn bản xuất thẳng; trường đích trong JSON được trích xuất; cấu trúc JSON còn lại bỏ.
func (f *StreamFilter) Feed(delta string) string {
	f.buf.Reset()
	for _, r := range delta {
		switch f.mode {
		case filterText:
			if r == '{' {
				f.thinking = false
				f.mode = filterJSON
				f.braceDepth = 1
				f.inString = false
				f.escJSON = false
				f.fieldExt.Reset()
				f.feedExtractor(r)
			} else {
				if !f.thinking {
					f.thinking = true
					f.buf.WriteString(ThinkingSep)
				}
				f.buf.WriteRune(r)
			}
		case filterJSON:
			f.feedExtractor(r)
			f.trackBraces(r)
		}
	}
	return f.buf.String()
}

// feedExtractor đẩy từng ký tự cho fieldExt, kết quả trích ghi vào buf.
func (f *StreamFilter) feedExtractor(r rune) {
	if text := f.fieldExt.Feed(string(r)); text != "" {
		f.buf.WriteString(text)
	}
}

// trackBraces theo dõi độ sâu dấu ngoặc nhọn JSON, về 0 thì chuyển về chế độ văn bản.
func (f *StreamFilter) trackBraces(r rune) {
	if f.escJSON {
		f.escJSON = false
		return
	}
	if f.inString {
		switch r {
		case '\\':
			f.escJSON = true
		case '"':
			f.inString = false
		}
		return
	}
	switch r {
	case '"':
		f.inString = true
	case '{':
		f.braceDepth++
	case '}':
		f.braceDepth--
		if f.braceDepth <= 0 {
			f.mode = filterText
		}
	}
}

// Reset trạng thái.
func (f *StreamFilter) Reset() {
	f.mode = filterText
	f.braceDepth = 0
	f.inString = false
	f.escJSON = false
	f.thinking = false
	f.fieldExt.Reset()
}
