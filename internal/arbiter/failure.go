package arbiter

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// FailureFacts là gói sự kiện dùng chung cho hai kịch bản worker_failure / deadlock:
// Engine đã phân loại mang tính xác định trước (thử lại/lỗi tham số v.v. không đến đây),
// những gì tới Arbiter đều là phần sót lại kiểu "mã xác định không đưa ra nổi lối thoát".
type FailureFacts struct {
	Kind          string   `json:"kind"` // worker_failure | deadlock
	Agent         string   `json:"agent,omitempty"`
	Task          string   `json:"task,omitempty"`
	Error         string   `json:"error,omitempty"` // worker_failure: văn bản lỗi
	ErrorKind     string   `json:"error_kind,omitempty"`
	Repeats       int      `json:"repeats,omitempty"` // deadlock: số lần cùng chỉ thị đã được giao
	Phase         string   `json:"phase,omitempty"`
	NextChapter   int      `json:"next_chapter,omitempty"`
	PendingQueue  []int    `json:"pending_rewrites,omitempty"`
	FoundationGap []string `json:"foundation_missing,omitempty"`
	FactWarnings  []string `json:"fact_warnings,omitempty"`
}

// FailureDecision phán định thất bại/bế tắc.
type FailureDecision struct {
	Action   string      `json:"action"` // retry | reroute | abort
	Dispatch *DispatchOp `json:"dispatch,omitempty"`
	Reason   string      `json:"reason"`
}

func (d *FailureDecision) ValidateAgainst(f FailureFacts) error {
	if strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("reason không được để trống")
	}
	switch d.Action {
	case "retry", "abort":
		return nil
	case "reroute":
		if d.Dispatch == nil {
			return fmt.Errorf("reroute phải kèm dispatch")
		}
		if err := d.Dispatch.validate(); err != nil {
			return err
		}
		return validateDispatchAgainst(d.Dispatch, f.Phase)
	default:
		return fmt.Errorf("action không hợp lệ: %q (chọn retry / reroute / abort)", d.Action)
	}
}

// failureContract nằm sát FailureDecision: action là enum đóng, dispatch là đối tượng khả
// dĩ null (chỉ khác null khi reroute); tổ hợp liên trường vẫn do ValidateAgainst kiểm tra
// theo sự kiện.
var failureContract = llmcontract.Contract{
	Name:        "arbiter_failure",
	Description: "Phán định thất bại/bế tắc: đưa ra lối thoát",
	Schema: schema.Object(
		schema.Property("action", schema.Enum("lối thoát", "retry", "reroute", "abort")).Required(),
		schema.Property("dispatch", dispatchSchema("đích giao việc (chỉ đưa ra khi reroute, nếu không là null)")).Required(),
		schema.Property("reason", schema.String("lý do phán định")).Required(),
	),
}

// DecideFailure tham vấn thất bại/bế tắc. Ngữ nghĩa thất bại: trả về error → Engine xử lý
// theo hướng thận trọng nhất (tạm dừng + notify), tuyệt đối không tham vấn vô hạn.
func DecideFailure(ctx context.Context, model agentcore.ChatModel, systemPrompt string, facts FailureFacts) (FailureDecision, error) {
	payload, err := marshalPayload(facts)
	if err != nil {
		return FailureDecision{}, err
	}
	return decide(ctx, model, failureContract, systemPrompt, payload, func(d *FailureDecision) error {
		return d.ValidateAgainst(facts)
	})
}
