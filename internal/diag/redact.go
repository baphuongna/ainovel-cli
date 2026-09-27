package diag

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SkelEvent là bộ khung hành vi của một tin nhắn phiên sau khi đã làm mờ: giữ lại
// tín hiệu cấu trúc (vai trò / công cụ / lỗi / vân tay lặp lại), mọi văn bản tự do
// (chính văn, prompt, suy nghĩ) đều bị che hết. Đây là lớp chiếu khắt khe hơn
// store.compactMessage — lớp sau nén theo thể tích (>4KB), còn ở đây không nhìn thể tích,
// văn bản nào cũng không được lọt ra ngoài gói.
type SkelEvent struct {
	Agent    string     // phiên nguồn: writer-ch07 / architect-arc02 …
	Role     string     // assistant / tool / user
	Tools    []SkelTool // các lệnh gọi công cụ trong tin nhắn này
	ErrClass string     // role=tool và is_error: dòng đầu của lỗi (chuỗi lỗi framework, không chứa chính văn)
	TextSha  string     // hash ngắn của chính văn đã che; cùng sha = lặp đi lặp lại tạo ra cùng một đoạn (tín hiệu vòng lặp)
	Redacted int        // số khối văn bản/suy nghĩ đã che trong tin này (dùng tự kiểm tra việc làm mờ)
}

// SkelTool là phép chiếu đã làm mờ của một lệnh gọi công cụ.
type SkelTool struct {
	Name     string            // tên công cụ (tín hiệu cấu trúc, không chứa chính văn)
	Args     map[string]string // key → giá trị vô hướng gốc / chuỗi ngắn kèm trích dẫn / "<redacted len sha>"
	Invalid  bool              // ArgsInvalid: tham số mô hình gửi tới không thể phân tích (tín hiệu #34)
	ParseErr string            // ArgsParseError: lý do phân tích thất bại
}

// redactMessage chiếu một agentcore.Message thành bộ khung hành vi.
func redactMessage(agent string, m agentcore.Message) SkelEvent {
	ev := SkelEvent{Agent: agent, Role: string(m.Role)}
	isErr, _ := m.Metadata["is_error"].(bool)

	var text strings.Builder
	for _, b := range m.Content {
		switch b.Type {
		case agentcore.ContentText:
			// Kết quả lỗi của tool giữ lại dòng đầu: đây là chuỗi lỗi của chính framework (như InputValidationError),
			// không chứa chính văn, và là chìa khóa định vị vòng lặp. Phần văn bản còn lại đều đưa vào bể che.
			if m.Role == agentcore.RoleTool && isErr && ev.ErrClass == "" {
				// Chuỗi lỗi có thể nhúng hình thái thông tin đăng nhập (như URL ?key=… bị phản hồi lại của Gemini),
				// che thống nhất trước khi xuất (review F5).
				ev.ErrClass = scrubSecrets(firstLine(b.Text, 160))
				continue
			}
			if strings.TrimSpace(b.Text) != "" {
				text.WriteString(b.Text)
				ev.Redacted++
			}
		case agentcore.ContentThinking:
			if strings.TrimSpace(b.Thinking) != "" {
				text.WriteString(b.Thinking)
				ev.Redacted++
			}
		case agentcore.ContentToolCall:
			if b.ToolCall != nil {
				ev.Tools = append(ev.Tools, redactToolCall(b.ToolCall))
			}
		}
	}
	if t := text.String(); t != "" {
		ev.TextSha = shortHash(t)
	}
	return ev
}

// redactToolCall chiếu một lệnh gọi công cụ: tên công cụ + tham số (giá trị đã làm mờ) + cờ phân tích bất thường.
func redactToolCall(tc *agentcore.ToolCall) SkelTool {
	return SkelTool{
		Name:     tc.Name,
		Args:     redactArgs(tc.Args),
		Invalid:  tc.ArgsInvalid,
		ParseErr: tc.ArgsParseError,
	}
}

// redactArgs chiếu đối tượng tham số công cụ thành key → giá trị đã làm mờ. Tham số không phải đối tượng trả về nil
// (ArgsInvalid/ParseErr đã được ghi chú riêng trong SkelTool).
func redactArgs(raw json.RawMessage) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = projectValue(v)
	}
	return out
}

// projectValue chiếu một giá trị tham số theo kiểu JSON:
//   - Vô hướng (số / bool / null): giá trị gốc chính là tín hiệu cấu trúc, giữ lại (chapter: 7)
//   - Chuỗi ngắn dạng định danh: giữ kèm trích dẫn, lộ rõ kiểu (chapter: "7" ← tín hiệu số bị chuỗi hóa của #34)
//   - Chuỗi chứa chữ Hán / khoảng trắng / văn bản dài, đối tượng, mảng: che thành <redacted …> (chính văn không lọt ra gói)
//   - Đã là chỗ đặt [session_compact: …]: an toàn và có thông tin, giữ nguyên
func projectValue(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return ""
	}
	switch s[0] {
	case '"':
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return redactPlaceholder(s)
		}
		if strings.HasPrefix(str, store.CompactTag) {
			return str
		}
		// Chỉ giữ giá trị ngắn "giống định danh/số/enum" (chapter:"7", type:"premise", agent:"writer");
		// bất kỳ chuỗi chứa chữ Hán, khoảng trắng hay ký hiệu khác đều coi là chính văn, che hết;
		// token ngắn có hình thái giống thông tin đăng nhập (sk-… v.v.) cũng bị che — hình thức định danh ASCII có thể đánh lừa kiểm tra cấu trúc (review F5).
		if utf8.RuneCountInString(str) <= 32 && isStructuralToken(str) && !looksLikeSecretToken(str) {
			return strconv.Quote(str)
		}
		return redactPlaceholder(str)
	case '{':
		return fmt.Sprintf("<redacted object len=%d>", len(raw))
	case '[':
		return fmt.Sprintf("<redacted array len=%d>", len(raw))
	default:
		return s
	}
}

// secretScrubRe khớp các hình thái thông tin đăng nhập thường gặp trong chuỗi lỗi: sk-…, Bearer …, key=…,
// api_key: … v.v. Chỉ dùng để che dự phòng trước khi xuất, không theo đuổi tính đầy đủ
// (bắn nhầm cũng vô hại — thứ bị thay thế chỉ là mảnh trong tóm tắt lỗi).
var secretScrubRe = regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_-]{4,}|AIza[A-Za-z0-9_-]{10,}|Bearer\s+[A-Za-z0-9._~+/=-]{4,}|(?:api[_-]?key|apikey|key|token|secret|password)["']?\s*[:=]\s*["']?[A-Za-z0-9._~+/=-]{4,})`)

// scrubSecrets thay các mảnh giống thông tin đăng nhập trong văn bản bằng [REDACTED].
// Đồng thời quét lại sau một lần giải mã URL một tầng (hình thái %3Fkey%3D…, review V-2 MINOR5):
// chỉ trả về bản giải mã + che khi sau khi giải mã mới khớp mẫu thông tin đăng nhập, tránh sửa nhầm văn bản thường.
func scrubSecrets(s string) string {
	out := secretScrubRe.ReplaceAllString(s, "[REDACTED]")
	if dec, err := url.PathUnescape(s); err == nil && dec != s {
		if scrubbed := secretScrubRe.ReplaceAllString(dec, "[REDACTED]"); scrubbed != dec {
			return scrubbed
		}
	}
	return out
}

// secretTokenPrefixes là đặc trưng tiền tố của token thông tin đăng nhập thường gặp (so sánh chữ thường).
var secretTokenPrefixes = []string{
	"sk-",                                         // phong cách OpenAI / OpenRouter / DeepSeek
	"ghp_", "gho_", "ghu_", "ghr_", "github_pat_", // GitHub
	"xoxb-", "xoxp-", // Slack
	"akia",   // AWS access key id
	"glpat-", // GitLab
}

// looksLikeSecretToken kiểm tra token ngắn có hình thái giống thông tin đăng nhập hay không, ngăn nó lọt vào gói xuất dưới danh nghĩa "tín hiệu cấu trúc".
func looksLikeSecretToken(s string) bool {
	if len(s) < 8 {
		return false
	}
	lower := strings.ToLower(s)
	for _, p := range secretTokenPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// isStructuralToken kiểm tra chuỗi có "giống định danh" hay không — thuần ASCII gồm chữ / số / `_-.:/`,
// không khoảng trắng, không chữ Hán. Dùng để phân biệt tín hiệu cấu trúc (giữ lại) với mảnh chính văn (che).
func isStructuralToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.' || r == ':' || r == '/':
		default:
			return false
		}
	}
	return true
}

func redactPlaceholder(s string) string {
	return fmt.Sprintf("<redacted len=%d sha=%s>", utf8.RuneCountInString(s), shortHash(s))
}

// shortHash lấy hash ngắn của văn bản; chỉ dùng để đoán "cùng một đoạn văn bản có lặp lại hay không", không dùng cho mục đích mật mã.
func shortHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%08x", h.Sum32())
}

// firstLine lấy dòng đầu và cắt theo rune, dùng cho tóm tắt chuỗi lỗi.
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max]) + "…"
	}
	return s
}
