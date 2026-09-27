package imp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/litellm"
)

// flakyModel trả lỗi có thể thử lại trong fails lần đầu, sau đó theo phản hồi của mockModel.
type flakyModel struct {
	mockModel
	fails int
}

func (f *flakyModel) Generate(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	if f.fails > 0 {
		f.fails--
		return nil, fastRetryErr{}
	}
	return f.mockModel.Generate(ctx, msgs, tools, opts...)
}

// fastRetryErr có thể thử lại và backoff cực ngắn (RetryAfter trúng RetryHinter), bảo đảm test nhanh.
type fastRetryErr struct{}

func (fastRetryErr) Error() string             { return "rate limited" }
func (fastRetryErr) Retryable() bool           { return true }
func (fastRetryErr) RetryAfter() time.Duration { return time.Millisecond }

// TestCallStructuredNotifiesRetries canh giữ tính thấy được của thử lại: backoff yêu cầu và hỏi
// lại sau kiểm tra đều phải hiển thị lại, nếu không backoff luỹ thừa có thể im lặng nhiều phút,
// người dùng tưởng nhập bị treo (vấn đề chụp màn hình: 3 phút im lặng mới báo lỗi). Backoff yêu cầu
// còn phải mang mốc hết hạn retryAt khác 0 — đếm ngược của UI dựa vào nó; hỏi lại sau kiểm tra xảy
// ra tức thời, retryAt bằng 0.
func TestCallStructuredNotifiesRetries(t *testing.T) {
	m := &flakyModel{mockModel: mockModel{responses: []string{"不是 JSON", `{"boundaries":[]}`}}, fails: 2}
	var notes []string
	var retries, reasks int
	prof := callProfile{notify: func(s string, retryAt time.Time) {
		notes = append(notes, s)
		if !retryAt.IsZero() {
			retries++
		}
		if strings.Contains(s, "hỏi lại") || strings.Contains(s, "gửi lại") {
			reasks++
		}
	}}
	if _, err := callStructured[boundaryBatch](context.Background(), m, segmentContract, "sys", "p", 100, prof, nil); err != nil {
		t.Fatalf("cuối cùng phải thành công: %v", err)
	}
	if retries != 2 || reasks != 1 {
		t.Fatalf("phải hiển thị lại 2 lần backoff yêu cầu kèm mốc hết hạn + 1 lần hỏi lại sau kiểm tra, được %d/%d: %v", retries, reasks, notes)
	}
}

// TestBriefErrIncludesAdapterFacts canh giữ tính chẩn đoán được của hiển thị lại lỗi: message của
// gateway có thể chỉ có một câu "Provider returned error", hiển thị lại phải bổ các sự thật có cấu
// trúc mà litellm mang (phân loại/trạng thái HTTP/provider/mô hình), và sự thật đặt trước — khi bị
// cắt thì ưu tiên giữ chúng; lỗi không phải adapter giữ nguyên.
func TestBriefErrIncludesAdapterFacts(t *testing.T) {
	le := &litellm.LiteLLMError{
		Type: litellm.ErrorTypeProvider, StatusCode: 502,
		Provider: "openai", Model: "gpt-x", Message: "Provider returned error",
	}
	got := briefErr(fmt.Errorf("bao ngoài: %w", le))
	for _, want := range []string{"lỗi dịch vụ thượng nguồn", "HTTP 502", "openai", "gpt-x", "Provider returned error"} {
		if !strings.Contains(got, want) {
			t.Fatalf("hiển thị lại phải chứa %q, được %q", want, got)
		}
	}
	if !strings.HasPrefix(got, "lỗi dịch vụ thượng nguồn") {
		t.Fatalf("sự thật có cấu trúc phải đặt trước, được %q", got)
	}
	if got := briefErr(errors.New("lỗi thường")); got != "lỗi thường" {
		t.Fatalf("lỗi không phải adapter phải giữ nguyên, được %q", got)
	}
}

// TestCallStructuredCancelIsNotSemanticFailure canh giữ ngữ nghĩa hủy: người dùng hủy (Esc) không
// phải thất bại ngữ nghĩa, không được bọc thành errSemantic kiểu "N lần thử" — điều đó sẽ làm lệch
// hướng điều tra và ghi thêm một artifact failures/ gây hiểu lầm.
func TestCallStructuredCancelIsNotSemanticFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := &mockModel{responses: []string{"垃圾输出"}}
	_, err := callStructured[boundaryBatch](ctx, m, segmentContract, "sys", "p", 100, callProfile{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("phải trả context.Canceled, được %v", err)
	}
	var se *errSemantic
	if errors.As(err, &se) {
		t.Fatal("hủy không được bọc thành thất bại ngữ nghĩa")
	}
}

// TestCallStructuredCarriesRawOnSemanticFailure canh giữ §14.2: khi vi phạm hợp đồng tầng đầu
// ra, lỗi phải mang phản hồi gốc, để runner tập trung ghi vào artifact thất bại failures/.
func TestCallStructuredCarriesRawOnSemanticFailure(t *testing.T) {
	m := &nativeImportModel{mockModel: &mockModel{responses: []string{"垃圾输出 not json"}}}
	_, err := callStructured[boundaryBatch](context.Background(), m, segmentContract, "sys", "payload", 100, callProfile{}, nil)
	var se *errSemantic
	if !errors.As(err, &se) {
		t.Fatalf("phải trả errSemantic, được %T: %v", err, err)
	}
	if se.Raw != "垃圾输出 not json" || !strings.Contains(se.Error(), "vi phạm hợp đồng schema gốc") {
		t.Fatalf("Raw phải mang phản hồi gốc lần cuối, được %q", se.Raw)
	}
}

func TestCallStructuredCarriesRawOnProtocolFailure(t *testing.T) {
	m := &nativeImportModel{mockModel: &mockModel{
		responses: []string{"upstream malformed output"},
		stops:     []agentcore.StopReason{agentcore.StopReasonError},
	}}
	_, err := callStructured[boundaryBatch](context.Background(), m, segmentContract, "sys", "payload", 100, callProfile{}, nil)
	var se *errSemantic
	if !errors.As(err, &se) || se.Raw != "upstream malformed output" || !strings.Contains(se.Error(), "stop_reason=error") {
		t.Fatalf("lỗi giao thức phải mang phản hồi gốc, được %T: %v", err, err)
	}
}
