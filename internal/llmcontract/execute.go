package llmcontract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/llmretry"
)

// FailureKind phân biệt ranh giới thất bại không thể sửa bằng chính lượt phản hồi cấu trúc đó.
type FailureKind string

const (
	FailureRequest  FailureKind = "request"
	FailureProtocol FailureKind = "protocol"
	FailureLength   FailureKind = "length"
	FailureSafety   FailureKind = "safety"
	FailureContract FailureKind = "contract"
)

// Failure giữ loại thất bại và đầu ra gốc của mô hình, để bên gọi quyết định ghi log, artifact và cách trình bày UI.
type Failure struct {
	Kind     FailureKind
	Contract string
	Raw      string
	Err      error
}

func (e *Failure) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return e.Contract
	}
	if e.Contract == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %v", e.Contract, e.Err)
}

func (e *Failure) Unwrap() error { return e.Err }

// Correction mô tả một lỗi đầu ra mà mô hình có thể sửa. Attempt là số thứ tự lượt gọi vừa thất bại.
type Correction struct {
	Attempt int
	Layer   string
	Mode    Mode
	Raw     string
	Err     error
}

// Hooks chỉ lo khả năng quan sát, không thay đổi ngữ nghĩa thực thi.
type Hooks struct {
	Resolved     func(Resolution)
	RequestRetry func(llmretry.Event)
	Correction   func(Correction)
}

// Request định nghĩa một lượt trả về cấu trúc trực tiếp. Contract là nguồn chân lý
// duy nhất của cấu trúc, Validate chỉ xử lý ràng buộc nghiệp vụ mà JSON Schema
// không diễn đạt được.
type Request[T any] struct {
	Contract     Contract
	SystemPrompt string
	Payload      string
	Options      []agentcore.CallOption
	Validate     func(*T) error
	Agent        string
	Hooks        Hooks
}

const promptCorrection = "Đầu ra phía trên không phù hợp JSON Schema. Hãy sửa theo lỗi và chỉ xuất đối tượng JSON đầy đủ, không kèm giải thích hay khung Markdown."
const semanticCorrection = "JSON phía trên hợp lệ về cấu trúc nhưng giá trị trường chưa vượt qua kiểm tra nghiệp vụ. Hãy sửa theo lỗi và xuất lại đối tượng JSON đầy đủ."

// Execute gộp chung chọn giao thức, chuẩn bị prompt, thử lại yêu cầu, phân loại lý
// do dừng, giải mã Schema/DTO và tự phục hồi bằng phản hồi nghiệp vụ. Lỗi định dạng
// hay Schema ở chế độ prompt và lỗi nghiệp vụ ở cả hai chế độ sẽ được liên tục phản
// hồi cho mô hình cho tới khi thành công hoặc context kết thúc; vi phạm hợp đồng
// native được bộc lộ ngay lập tức.
func Execute[T any](ctx context.Context, model llmretry.Generator, req Request[T]) (T, error) {
	var zero T
	if model == nil {
		return zero, &Failure{Kind: FailureProtocol, Contract: req.Contract.Name, Err: errors.New("mô hình chưa được cấu hình")}
	}

	schemaOptions, resolution := Plan(model, req.Contract)
	systemPrompt, err := PreparePrompt(req.SystemPrompt, req.Contract, resolution)
	if err != nil {
		return zero, &Failure{Kind: FailureContract, Contract: req.Contract.Name, Err: fmt.Errorf("chuẩn bị hợp đồng đầu ra: %w", err)}
	}
	if req.Hooks.Resolved != nil {
		req.Hooks.Resolved(resolution)
	}

	messages := []agentcore.Message{
		agentcore.SystemMsg(systemPrompt),
		agentcore.UserMsg(req.Payload),
	}
	options := append(schemaOptions, req.Options...)
	native := resolution.Mode == ModeNativeJSONSchema

	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		resp, err := llmretry.Generate(ctx, model, llmretry.Config{
			Agent:   req.Agent,
			OnRetry: req.Hooks.RequestRetry,
		}, messages, options...)
		if err != nil {
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			return zero, &Failure{Kind: FailureRequest, Contract: req.Contract.Name, Err: err}
		}
		if resp == nil {
			return zero, &Failure{Kind: FailureProtocol, Contract: req.Contract.Name, Err: errors.New("mô hình trả về phản hồi rỗng")}
		}

		raw := resp.Message.TextContent()
		switch resp.Message.StopReason {
		case agentcore.StopReasonLength:
			return zero, &Failure{Kind: FailureLength, Contract: req.Contract.Name, Raw: raw, Err: errors.New("đầu ra mô hình bị cắt cụt vì vượt độ dài (stop_reason=length)")}
		case agentcore.StopReasonSafety:
			return zero, &Failure{Kind: FailureSafety, Contract: req.Contract.Name, Raw: raw, Err: errors.New("mô hình từ chối trả lời hoặc kích hoạt bộ lọc nội dung (stop_reason=safety)")}
		case agentcore.StopReasonError:
			return zero, &Failure{Kind: FailureProtocol, Contract: req.Contract.Name, Raw: raw, Err: errors.New("mô hình kết thúc với trạng thái lỗi (stop_reason=error)")}
		case agentcore.StopReasonToolUse:
			return zero, &Failure{Kind: FailureProtocol, Contract: req.Contract.Name, Raw: raw, Err: errors.New("lượt gọi cấu trúc ngoài ý muốn lại trả về lệnh gọi công cụ (stop_reason=tool_use)")}
		case agentcore.StopReasonAborted:
			return zero, &Failure{Kind: FailureProtocol, Contract: req.Contract.Name, Raw: raw, Err: errors.New("lượt gọi mô hình bị huỷ giữa chừng (stop_reason=aborted)")}
		}

		body := strings.TrimSpace(raw)
		if native {
			if body == "" {
				return zero, &Failure{Kind: FailureContract, Contract: req.Contract.Name, Raw: raw, Err: errors.New("schema gốc trả về nội dung rỗng")}
			}
		} else {
			body = ExtractJSONObject(raw)
		}

		layer := "schema"
		var cause error
		if body == "" {
			layer, cause = "decode", errors.New("không tìm thấy đối tượng JSON trong đầu ra")
		} else if err := ValidateJSON(req.Contract.Schema, []byte(body)); err != nil {
			cause = err
		} else {
			var out T
			if err := json.Unmarshal([]byte(body), &out); err != nil {
				// Schema đã thông qua mà DTO không giải mã được, chứng tỏ hợp đồng tĩnh và kiểu
				// Go không khớp nhau; tiếp tục yêu cầu mô hình viết lại cũng không sửa được
				// lỗi của mã nguồn.
				return zero, &Failure{Kind: FailureContract, Contract: req.Contract.Name, Raw: raw, Err: fmt.Errorf("schema không khớp DTO: %w", err)}
			}
			if req.Validate == nil {
				return out, nil
			}
			if err := req.Validate(&out); err == nil {
				return out, nil
			} else {
				layer, cause = "semantic", err
			}
		}

		if native && layer != "semantic" {
			return zero, &Failure{Kind: FailureContract, Contract: req.Contract.Name, Raw: raw, Err: fmt.Errorf("vi phạm hợp đồng schema gốc: %w", cause)}
		}
		correction := Correction{Attempt: attempt, Layer: layer, Mode: resolution.Mode, Raw: raw, Err: cause}
		if req.Hooks.Correction != nil {
			req.Hooks.Correction(correction)
		}
		hint := promptCorrection
		if layer == "semantic" {
			hint = semanticCorrection
		}
		messages = append(messages,
			agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(raw)}},
			agentcore.UserMsg(hint+"\nLỗi: "+cause.Error()),
		)
	}
}

// ExtractJSONObject trả về đối tượng JSON cân bằng đầu tiên trong văn bản, dấu ngoặc nhọn nằm trong chuỗi không tính vào tầng.
func ExtractJSONObject(raw string) string {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return ""
	}
	depth, inString, escaped := 0, false, false
	for i := start; i < len(raw); i++ {
		switch c := raw[i]; {
		case inString && escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && c == '{':
			depth++
		case !inString && c == '}':
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	return ""
}
