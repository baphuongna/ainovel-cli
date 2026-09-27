package arbiter

import (
	"context"
	"fmt"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// PlanStartDecision phán định khởi động: chọn kiến trúc sư và tạo văn bản nhiệm vụ (đã mở rộng nếu cần).
type PlanStartDecision struct {
	Planner string `json:"planner"` // architect_long | architect_short
	Task    string `json:"task"`    // nhiệm vụ đầy đủ giao cho kiến trúc sư (gồm yêu cầu đã mở rộng)
	Reason  string `json:"reason"`
}

func (d *PlanStartDecision) Validate() error {
	if d.Planner != "architect_long" && d.Planner != "architect_short" {
		return fmt.Errorf("planner không hợp lệ: %q (chọn architect_long / architect_short)", d.Planner)
	}
	if strings.TrimSpace(d.Task) == "" {
		return fmt.Errorf("task không được để trống")
	}
	if strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("reason không được để trống")
	}
	return nil
}

// planStartContract nằm sát PlanStartDecision: mọi trường đều required, planner là enum đóng.
var planStartContract = llmcontract.Contract{
	Name:        "arbiter_plan_start",
	Description: "Phán định khởi động: chọn kiến trúc sư và tạo văn bản nhiệm vụ đầy đủ",
	Schema: schema.Object(
		schema.Property("planner", schema.Enum("kiến trúc sư", "architect_long", "architect_short")).Required(),
		schema.Property("task", schema.String("nhiệm vụ đầy đủ giao cho kiến trúc sư (gồm yêu cầu đã mở rộng)")).Required(),
		schema.Property("reason", schema.String("lý do lựa chọn")).Required(),
	),
}

// planStartPayload là payload người dùng của plan_start (chính yêu cầu là đầu vào, không có
// trạng thái store — sách mới).
type planStartPayload struct {
	Requirement string `json:"requirement"`
	Style       string `json:"style,omitempty"`
}

// DecidePlanStart phán định khởi động: chọn kiến trúc sư theo nhu cầu người dùng; khi nhu cầu
// quá ngắn (<20 ký tự) thì tự bổ sung vào task hướng khác biệt, độc giả mục tiêu và điểm hấp
// dẫn cốt lõi, cùng ít nhất một hook phi quy ước.
// Ngữ nghĩa thất bại: trả về error → bên gọi báo lỗi rõ ràng và dừng khởi động (người dùng
// hiện diện ở giai đoạn khởi động, báo lỗi tốt hơn đoán mò).
func DecidePlanStart(ctx context.Context, model agentcore.ChatModel, systemPrompt, requirement, style string) (PlanStartDecision, error) {
	payload, err := marshalPayload(planStartPayload{Requirement: requirement, Style: style})
	if err != nil {
		return PlanStartDecision{}, err
	}
	return decide(ctx, model, planStartContract, systemPrompt, payload, (*PlanStartDecision).Validate)
}
