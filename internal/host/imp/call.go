package imp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/llmretry"
	"github.com/voocel/litellm"
)

// callModel là dependency tối tiểu của lõi dành cho mô hình, tiện inject mock khi kiểm thử.
type callModel interface {
	Generate(ctx context.Context, messages []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error)
}

// errTruncated biểu thị mô hình dừng vì giới hạn độ dài (lỗi dung lượng). Mang theo văn bản
// gốc để bên gọi quyết định thất bại hay cứu vớt tiền tố (§9.5).
type errTruncated struct {
	Raw string
}

func (e *errTruncated) Error() string {
	return "kết quả mô hình bị cắt ngắn vì giới hạn độ dài (stop=length)"
}

// errSemantic biểu thị thất bại tầng đầu ra không thể sửa bằng cách hỏi lại, mang theo phản hồi
// gốc, để runner thống nhất ghi artifact thất bại vào failures/ (§14.2), mọi hàm ngữ nghĩa dùng chung.
type errSemantic struct {
	Raw string
	Err error
}

func (e *errSemantic) Error() string { return e.Err.Error() }
func (e *errSemantic) Unwrap() error { return e.Err }

// callProfile mang tùy chọn thinking và khả năng quan sát, suy ra từ ModelRuntime mà Host dò được.
// Giao thức có cấu trúc do callStructured tự chọn theo sự thật của mô hình và Contract tĩnh.
type callProfile struct {
	thinking agentcore.ThinkingLevel
	// notify tùy chọn: hiển thị lại cho giao diện việc backoff thử lại / hỏi lại sau kiểm tra;
	// nil thì im lặng (§14.1).
	// retryAt khác 0 = thời điểm hết hạn của lần thử lại kế, UI vẽ đếm ngược từng giây theo đó
	// (sự kiện chỉ mang mốc hết hạn, thời gian còn lại tính lúc render).
	notify func(msg string, retryAt time.Time)
	// progress tùy chọn: hiển thị lại tiến độ nội bộ của giai đoạn dài (phân tách khối N/M,
	// tóm tắt khoảng N/M); nil thì im lặng.
	// Phân tách/tổng hợp gọi mô hình lần lượt theo từng khối/khoảng bên trong hàm, một khối có thể
	// kéo dài nhiều phút, thiếu nó thì bảng hiển thị im lặng cả đoạn trông như treo (§14.1).
	progress func(current, total int, msg string)
	// log tùy chọn: log riêng của lượt nhập (logs/import.log); nil thì fallback logger mặc định.
	log *slog.Logger
}

func (p callProfile) logger() *slog.Logger {
	if p.log != nil {
		return p.log
	}
	return slog.Default()
}

// step hiển thị lại một tiến độ thường (tiến độ nội bộ của giai đoạn dài).
func (p callProfile) step(current, total int, format string, args ...any) {
	if p.progress != nil {
		p.progress(current, total, fmt.Sprintf(format, args...))
	}
}

// say hiển thị lại một trạng thái gọi dài. Thử lại có thể im lặng nhiều phút (backoff luỹ
// tiến hơn 2 phút), không hiển thị lại thì người dùng tưởng treo.
func (p callProfile) say(format string, args ...any) {
	p.sayRetry(time.Time{}, format, args...)
}

// sayRetry hiển thị lại một trạng thái kèm mốc hết hạn thử lại, để UI đếm ngược.
func (p callProfile) sayRetry(retryAt time.Time, format string, args ...any) {
	if p.notify != nil {
		p.notify(fmt.Sprintf(format, args...), retryAt)
	}
}

// snippet nén văn bản nhiều dòng thành tóm tắt ngắn một dòng để hiển thị lại: gộp khoảng trắng,
// cắt đến max rune.
func snippet(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// briefErr nén lỗi thành văn bản ngắn một dòng để hiển thị lại (chuỗi lỗi đầy đủ vẫn đi qua log
// và artifact thất bại). Sự thật có cấu trúc của adapter đặt lên trước: khi bị cắt thì ưu tiên giữ
// "loại lỗi nào, mã trạng thái gì", message của gateway có thể hy sinh.
func briefErr(err error) string {
	s := err.Error()
	if d := modelErrDetail(err); d != "" {
		s = d + ": " + s
	}
	return snippet(s, 100)
}

// errTypeLabels dịch phân loại lỗi litellm thành nhãn tiếng Việt ngắn dễ đọc.
var errTypeLabels = map[litellm.ErrorType]string{
	litellm.ErrorTypeAuth:            "xác thực thất bại",
	litellm.ErrorTypeRateLimit:       "giới hạn tần suất",
	litellm.ErrorTypeNetwork:         "lỗi mạng",
	litellm.ErrorTypeValidation:      "tham số yêu cầu bất hợp lệ",
	litellm.ErrorTypeProvider:        "lỗi dịch vụ thượng nguồn",
	litellm.ErrorTypeTimeout:         "hết thời gian chờ",
	litellm.ErrorTypeQuota:           "hết hạn mức",
	litellm.ErrorTypeModel:           "mô hình không khả dụng",
	litellm.ErrorTypeInternal:        "lỗi nội bộ",
	litellm.ErrorTypeContextOverflow: "vượt giới hạn ngữ cảnh",
	litellm.ErrorTypeOverloaded:      "dịch vụ thượng nguồn quá tải",
	litellm.ErrorTypeContentFilter:   "bị lọc nội dung chặn",
}

// modelErrDetail trích từ chuỗi lỗi các sự thật có cấu trúc của adapter (phân loại lỗi, trạng thái
// HTTP, provider, mô hình). Message của gateway thường chỉ có một câu chung chung "Provider returned
// error", chỉ dựa vào đó không thể biết là lỗi cấu hình, trục trặc thượng nguồn hay giới hạn tần suất;
// những sự thật này litellm luôn mang theo, chỉ là không vào văn bản Error(). Unwrap của adapter
// agentcore cho phép rõ ràng bên gọi biết litellm dùng errors.As lấy lỗi gốc. Lỗi không phải gọi mô
// hình trả về chuỗi rỗng.
func modelErrDetail(err error) string {
	var le *litellm.LiteLLMError
	if !errors.As(err, &le) {
		return ""
	}
	parts := make([]string, 0, 4)
	if label := errTypeLabels[le.Type]; label != "" {
		parts = append(parts, label)
	}
	if le.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", le.StatusCode))
	}
	if le.Provider != "" {
		parts = append(parts, le.Provider)
	}
	if le.Model != "" {
		parts = append(parts, le.Model)
	}
	return strings.Join(parts, ", ")
}

// callOptions lắp CallOption cho lần gọi này: luôn kèm giới hạn đầu ra; thinking tùy theo khả năng.
// thinking chỉ gửi khi không ở chế độ Auto — với mô hình không hỗ trợ thinking, gửi bất kỳ cấp nào
// (kể cả off) đều là tham số bất hợp lệ (cùng chiến lược với arbiter).
func (p callProfile) callOptions(maxTokens int) []agentcore.CallOption {
	opts := []agentcore.CallOption{agentcore.WithMaxTokens(maxTokens)}
	if p.thinking != agentcore.ThinkingAuto {
		opts = append(opts, agentcore.WithThinking(p.thinking))
	}
	return opts
}

// callStructured thích ứng executor có cấu trúc thống nhất cho tầng nhập, và ánh xạ các thất bại
// chung thành ngữ nghĩa artifact của lượt nhập.
func callStructured[T any](ctx context.Context, m callModel, contract llmcontract.Contract, systemPrompt, payload string, maxTokens int, prof callProfile, validate func(*T) error) (T, error) {
	out, err := llmcontract.Execute(ctx, m, llmcontract.Request[T]{
		Contract:     contract,
		SystemPrompt: systemPrompt,
		Payload:      payload,
		Options:      prof.callOptions(maxTokens),
		Validate:     validate,
		Agent:        "import",
		Hooks: llmcontract.Hooks{
			Resolved: func(res llmcontract.Resolution) {
				prof.logger().Debug("imp lựa chọn giao thức có cấu trúc",
					"contract", contract.Name, "structured_mode", res.Mode,
					"capability_source", res.Source, "provider", res.Provider,
					"model", res.Model, "schema_fingerprint", contract.Fingerprint())
			},
			RequestRetry: func(ev llmretry.Event) {
				prof.sayRetry(time.Now().Add(ev.Delay), "yêu cầu mô hình thất bại (%s), đang thử lại lần thứ %d", briefErr(ev.Err), ev.Attempt)
				prof.logger().Warn("imp thử lại yêu cầu mô hình", "attempt", ev.Attempt, "delay", ev.Delay, "err", ev.Err)
			},
			Correction: func(ev llmcontract.Correction) {
				prof.say("đầu ra chưa qua kiểm tra (%s), gửi lại lần thứ %d kèm phản hồi lỗi", briefErr(ev.Err), ev.Attempt+1)
				prof.logger().Warn("imp tự chữa đầu ra có cấu trúc", "attempt", ev.Attempt,
					"layer", ev.Layer, "structured_mode", ev.Mode, "err", ev.Err)
			},
		},
	})
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	var failure *llmcontract.Failure
	if !errors.As(err, &failure) {
		return out, fmt.Errorf("imp: %w", err)
	}
	switch failure.Kind {
	case llmcontract.FailureLength:
		return out, &errTruncated{Raw: failure.Raw}
	case llmcontract.FailureSafety, llmcontract.FailureContract, llmcontract.FailureProtocol:
		if failure.Raw != "" {
			return out, &errSemantic{Raw: failure.Raw, Err: fmt.Errorf("imp: %w", failure)}
		}
	case llmcontract.FailureRequest:
		if detail := modelErrDetail(failure); detail != "" {
			return out, fmt.Errorf("imp: gọi mô hình thất bại (%s): %w", detail, failure)
		}
	}
	return out, fmt.Errorf("imp: %w", failure)
}
