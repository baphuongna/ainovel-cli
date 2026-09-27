package host

import (
	"strings"
	"unicode/utf8"
)

// toolDisplays cấu hình chiến lược hiển thị của từng tool trên panel streaming. Tool không có trong bảng
// này không tham gia render streaming (observer vứt DeltaToolCall trực tiếp).
//
// Chế độ chung (nakedKey rỗng): tokenizer render args JSON mà LLM xuất thành dạng thụt lề
// "key: value", object/array lồng nhau thụt lề theo cấp, string/number/bool xuất streaming.
// Hoàn toàn tách rời schema — LLM thừa một trường thì panel thêm một dòng, không cần sửa mã.
//
// Chế độ dòng trần (nakedKey khác rỗng): chỉ đưa nguyên văn giá trị string của trường mục tiêu tầng trên cùng,
// các trường khác bỏ hết. Dành cho draft_chapter, để markdown cả chương không bị trang điểm thành "content: # …".
// header luôn bắt đầu bằng "✻ ": đây là tiền tố quy ước để TUI renderStreamContent đi đường
// renderAgentBlock (✻ vàng + label nền xanh gạch chân xanh + đường kẻ dim), đồng bộ với fallback
// header (streamHeaderFallback); đổi thành chữ thường sẽ rơi vào đường phần thân bị màu
// mặc định terminal vẽ đè, title hết nổi bật.
var toolDisplays = map[string]toolDisplay{
	"draft_chapter": {nakedKey: "content"},

	"plan_chapter":        {header: "✻ Lập dàn ý"},
	"edit_chapter":        {header: "✻ Đánh bóng"},
	"commit_chapter":      {header: "✻ Nộp chương"},
	"save_review":         {header: "✻ Xem xét"},
	"save_arc_summary":    {header: "✻ Tóm tắt cung"},
	"save_volume_summary": {header: "✻ Tóm tắt quyển"},
	"save_foundation":     {header: "✻ Thiết lập"},
	"revise_outline":      {header: "✻ Sửa dàn ý"},
	"read_chapter":        {header: "✻ Đọc chương"},
	"check_consistency":   {header: "✻ Kiểm tra nhất quán"},
	"novel_context":       {header: "✻ Tra ngữ cảnh"},
}

type toolDisplay struct {
	header   string
	nakedKey string
}

// jsonFieldExtractor là tokenizer JSON streaming. Trạng thái máy chạy từng byte, biến args của
// LLM thành văn bản dễ đọc. Một instance chỉ phục vụ một lần gọi tool, container tầng trên cùng đóng xong thì Done()=true.
type jsonFieldExtractor struct {
	cfg toolDisplay

	state pState
	stack []byte // ngăn xếp container: 'O' obj / 'A' arr

	keyBuf strings.Builder

	escape bool
	uHex   []byte

	started bool // đã emit ký tự nào chưa (dùng cho xuống dòng giữa header và key đầu tiên)

	done bool
}

type pState int

const (
	psRoot         pState = iota
	psBeforeKey           // trong obj: đợi key hoặc } kế tiếp
	psInKey               // trong obj: parse key
	psAfterKey            // trong obj: đợi :
	psBeforeValue         // đợi ký tự bắt đầu value
	psStringStream        // value string, emit streaming ký tự cooked
	psStringSkip          // value string, bỏ qua (chế độ dòng trần với trường không phải mục tiêu)
	psNumberStream        // số, emit streaming
	psNumberSkip          // số, bỏ qua
	psPrimStream          // true/false/null, emit streaming
	psPrimSkip            // true/false/null, bỏ qua
	psDone                // container tầng trên cùng đã đóng
)

func newToolExtractor(tool string) *jsonFieldExtractor {
	cfg, ok := toolDisplays[tool]
	if !ok {
		return nil
	}
	return &jsonFieldExtractor{cfg: cfg}
}

func (e *jsonFieldExtractor) Done() bool { return e.done }

func (e *jsonFieldExtractor) Feed(chunk string) string {
	if e.done || chunk == "" {
		return ""
	}
	var out strings.Builder
	for i := 0; i < len(chunk); i++ {
		e.step(chunk[i], &out)
		if e.done {
			break
		}
	}
	return out.String()
}

// ── Ngăn xếp container / thụt lề ──

func (e *jsonFieldExtractor) push(kind byte) {
	e.stack = append(e.stack, kind)
}

func (e *jsonFieldExtractor) pop() {
	if len(e.stack) == 0 {
		return
	}
	e.stack = e.stack[:len(e.stack)-1]
}

func (e *jsonFieldExtractor) parent() byte {
	if len(e.stack) == 0 {
		return 0
	}
	return e.stack[len(e.stack)-1]
}

// writeIndent ghi thụt lề hiện tại. Độ sâu = số cấp lồng = len(stack)-1 (bên trong container gốc không thụt lề).
func (e *jsonFieldExtractor) writeIndent(out *strings.Builder) {
	depth := len(e.stack) - 1
	for range depth {
		out.WriteString("  ")
	}
}

// ── Trạng thái máy ──

func (e *jsonFieldExtractor) step(c byte, out *strings.Builder) {
	switch e.state {
	case psRoot:
		switch c {
		case '{':
			e.push('O')
			e.state = psBeforeKey
		case '[':
			// Thực tế không xảy ra (tool args luôn là obj); bao dung: khi root là arr
			e.push('A')
			e.state = psBeforeValue
		}
	case psBeforeKey:
		switch c {
		case '"':
			e.keyBuf.Reset()
			e.escape = false
			e.state = psInKey
		case '}':
			e.closeContainer(out)
		case ' ', '\t', '\n', '\r', ',':
		}
	case psInKey:
		if e.escape {
			e.keyBuf.WriteByte(c)
			e.escape = false
			return
		}
		if c == '\\' {
			e.escape = true
			return
		}
		if c == '"' {
			e.emitKeyLine(out, e.keyBuf.String())
			e.state = psAfterKey
			return
		}
		e.keyBuf.WriteByte(c)
	case psAfterKey:
		if c == ':' {
			e.state = psBeforeValue
		}
	case psBeforeValue:
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' {
			return
		}
		switch c {
		case '"':
			e.beginString(out)
		case '{':
			e.beginNested('O', out)
		case '[':
			e.beginNested('A', out)
		case ']', '}':
			e.closeContainer(out)
		case 't', 'f', 'n':
			e.beginPrim(c, out)
		default:
			if c == '-' || (c >= '0' && c <= '9') {
				e.beginNumber(c, out)
			}
		}
	case psStringStream:
		e.handleStringByte(c, out, false)
	case psStringSkip:
		e.handleStringByte(c, out, true)
	case psNumberStream:
		if isNumberByte(c) {
			out.WriteByte(c)
			return
		}
		e.afterValueChar(c, out)
	case psNumberSkip:
		if isNumberByte(c) {
			return
		}
		e.afterValueChar(c, out)
	case psPrimStream:
		if c >= 'a' && c <= 'z' {
			out.WriteByte(c)
			return
		}
		e.afterValueChar(c, out)
	case psPrimSkip:
		if c >= 'a' && c <= 'z' {
			return
		}
		e.afterValueChar(c, out)
	case psDone:
	}
}

// ── Render dòng ──

// emitKeyLine được gọi khi parse xong key trong obj, viết tiền tố "<lf><indent>key:".
// Chế độ dòng trần không viết tiền tố key (key được ghi trong keyBuf để beginString xét).
func (e *jsonFieldExtractor) emitKeyLine(out *strings.Builder, key string) {
	if e.cfg.nakedKey != "" {
		return
	}
	if !e.started {
		if e.cfg.header != "" {
			out.WriteString(e.cfg.header)
			out.WriteByte('\n')
		}
		e.started = true
	} else {
		out.WriteByte('\n')
	}
	e.writeIndent(out)
	out.WriteString(key)
	out.WriteByte(':')
}

// emitArrayItem được gọi lúc bắt đầu mỗi phần tử trong arr, viết "<lf><indent>-". Phần tử
// primitive nối khoảng trắng rồi emit giá trị; phần tử struct do phần lồng sau tự xuống dòng.
func (e *jsonFieldExtractor) emitArrayItem(out *strings.Builder) {
	if e.cfg.nakedKey != "" {
		return
	}
	if !e.started {
		if e.cfg.header != "" {
			out.WriteString(e.cfg.header)
			out.WriteByte('\n')
		}
		e.started = true
	} else {
		out.WriteByte('\n')
	}
	e.writeIndent(out)
	out.WriteByte('-')
}

// ── Bắt đầu value ──

func (e *jsonFieldExtractor) beginString(out *strings.Builder) {
	if e.cfg.nakedKey != "" {
		// Dòng trần: chỉ giá trị string của key mục tiêu trong obj tầng trên cùng mới được xuất
		if e.cfg.nakedKey == e.keyBuf.String() && len(e.stack) == 1 && e.stack[0] == 'O' {
			e.state = psStringStream
		} else {
			e.state = psStringSkip
		}
		e.escape = false
		e.uHex = nil
		return
	}
	// Chung: trường obj nối "key: " (đã emit "key:", giờ bù khoảng trắng); phần tử arr nối "- "
	if e.parent() == 'A' {
		e.emitArrayItem(out)
		out.WriteByte(' ')
	} else {
		out.WriteByte(' ')
	}
	e.state = psStringStream
	e.escape = false
	e.uHex = nil
}

func (e *jsonFieldExtractor) beginNumber(first byte, out *strings.Builder) {
	if e.cfg.nakedKey != "" {
		e.state = psNumberSkip
		return
	}
	if e.parent() == 'A' {
		e.emitArrayItem(out)
		out.WriteByte(' ')
	} else {
		out.WriteByte(' ')
	}
	out.WriteByte(first)
	e.state = psNumberStream
}

func (e *jsonFieldExtractor) beginPrim(first byte, out *strings.Builder) {
	if e.cfg.nakedKey != "" {
		e.state = psPrimSkip
		return
	}
	if e.parent() == 'A' {
		e.emitArrayItem(out)
		out.WriteByte(' ')
	} else {
		out.WriteByte(' ')
	}
	out.WriteByte(first)
	e.state = psPrimStream
}

func (e *jsonFieldExtractor) beginNested(kind byte, out *strings.Builder) {
	if e.cfg.nakedKey != "" {
		// Chế độ dòng trần không mở rộng phần lồng; dùng độ sâu ngăn xếp để theo dõi tới } / ] khớp
		e.push(kind)
		if kind == 'O' {
			e.state = psBeforeKey
		} else {
			e.state = psBeforeValue
		}
		return
	}
	// Chế độ chung: khi phần tử arr là cấu trúc lồng, emit trước một dòng riêng "<indent>-"
	// (sau ":" của key obj không có khoảng trắng, để key con lồng tự xuống dòng kế tiếp)
	if e.parent() == 'A' {
		e.emitArrayItem(out)
	}
	e.push(kind)
	if kind == 'O' {
		e.state = psBeforeKey
	} else {
		e.state = psBeforeValue
	}
}

// closeContainer xử lý } hoặc ].
func (e *jsonFieldExtractor) closeContainer(out *strings.Builder) {
	e.pop()
	if len(e.stack) == 0 {
		// args rỗng (như novel_context không truyền tham số) fallback: emitKeyLine không có cơ hội xuất header,
		// bù một lần ở đây, tránh rơi vào "không tiêu đề cũng không nội dung".
		if !e.started && e.cfg.nakedKey == "" && e.cfg.header != "" {
			out.WriteString(e.cfg.header)
			out.WriteByte('\n')
			e.started = true
		}
		// Xuống dòng kết thúc để panel có ranh giới rõ với đoạn xuất kế tiếp
		if e.started {
			out.WriteByte('\n')
		}
		e.state = psDone
		e.done = true
		return
	}
	if e.parent() == 'O' {
		e.state = psBeforeKey
	} else {
		e.state = psBeforeValue
	}
}

// ── string streaming ──

func (e *jsonFieldExtractor) handleStringByte(c byte, out *strings.Builder, skipping bool) {
	if e.uHex != nil {
		e.uHex = append(e.uHex, c)
		if len(e.uHex) == 4 {
			if r, ok := parseHex4(e.uHex); ok && !skipping {
				var buf [4]byte
				n := utf8.EncodeRune(buf[:], r)
				out.Write(buf[:n])
			}
			e.uHex = nil
		}
		return
	}
	if e.escape {
		e.escape = false
		if !skipping {
			writeEscapedByte(out, c)
		}
		if c == 'u' {
			e.uHex = make([]byte, 0, 4)
		}
		return
	}
	if c == '\\' {
		e.escape = true
		return
	}
	if c == '"' {
		e.afterValueDone()
		return
	}
	if !skipping {
		out.WriteByte(c)
	}
}

func writeEscapedByte(out *strings.Builder, c byte) {
	switch c {
	case 'n':
		out.WriteByte('\n')
	case 't':
		out.WriteByte('\t')
	case 'r':
		out.WriteByte('\r')
	case '"':
		out.WriteByte('"')
	case '\\':
		out.WriteByte('\\')
	case '/':
		out.WriteByte('/')
	case 'b', 'f':
		// Backspace / form feed: bỏ qua
	case 'u':
		// Buffer uHex do bên gọi dựng; ở đây không xuất
	default:
		out.WriteByte('\\')
		out.WriteByte(c)
	}
}

// ── Kết thúc ──

// afterValueDone chuyển sang trạng thái kế tiếp sau khi string đóng (đọc tới dấu `"` cuối).
func (e *jsonFieldExtractor) afterValueDone() {
	e.escape = false
	e.uHex = nil
	if len(e.stack) == 0 {
		e.state = psDone
		e.done = true
		return
	}
	if e.parent() == 'O' {
		e.state = psBeforeKey
	} else {
		e.state = psBeforeValue
	}
}

// afterValueChar quyết định trạng thái kế tiếp theo ký tự khi "ký tự kết thúc" của number / primitive
// đã được đọc. Ký tự này có thể là , / } / ] / khoảng trắng, do hàm này chuyển tiếp phân phát.
func (e *jsonFieldExtractor) afterValueChar(c byte, out *strings.Builder) {
	switch c {
	case '}', ']':
		e.closeContainer(out)
	case ',', ' ', '\t', '\n', '\r':
		if len(e.stack) == 0 {
			e.state = psDone
			e.done = true
			return
		}
		if e.parent() == 'O' {
			e.state = psBeforeKey
		} else {
			e.state = psBeforeValue
		}
	}
}

// ── Tiện ích ──

func isNumberByte(c byte) bool {
	switch c {
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
		'-', '+', '.', 'e', 'E':
		return true
	}
	return false
}

func parseHex4(b []byte) (rune, bool) {
	var r rune
	for _, d := range b {
		var v rune
		switch {
		case d >= '0' && d <= '9':
			v = rune(d - '0')
		case d >= 'a' && d <= 'f':
			v = rune(d-'a') + 10
		case d >= 'A' && d <= 'F':
			v = rune(d-'A') + 10
		default:
			return 0, false
		}
		r = r*16 + v
	}
	return r, true
}
