// Package arbiter là tầng phán định ngữ nghĩa: LLM-as-function thức tỉnh theo nhu cầu.
//
// Hai mặt phẳng đối xứng (docs/engine-arbiter.md §II):
//
//	Mặt phẳng xác định:  flow.LoadState   → flow.Route     → Instruction
//	Mặt phẳng ngữ nghĩa: arbiter.Collect* → arbiter.Decide* → XxxDecision
//
// Kỷ luật: Collect tập trung IO (đọc đủ sự kiện từ store); Decide không có IO ngoài yêu
// cầu mô hình do trình thực thi thống nhất quản lý, có thể phát lại offline bằng facts
// lịch sử; thực thi thuộc về Engine. Mỗi kịch bản một cặp hàm + kiểu Decision riêng,
// hành động lệch kịch bản không thể diễn đạt ở mức kiểu; tính hợp lệ còn lại do Validate
// của từng kiểu từ chối —
// Đầu ra Arbiter cũng như mọi đầu ra LLM đều không đáng tin, kiểm tra sự kiện là cánh
// cửa cuối cùng.
package arbiter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// decideMaxTokens là giới hạn đầu ra cho một lần phán định; JSON phán định rất nhỏ,
// phần lớn dành cho ngân sách suy nghĩ của mô hình Reasoning
// (cùng lý với userrules.normalizeMaxTokens).
const decideMaxTokens = 8192

// decide giao hợp đồng kịch bản và kiểm tra nghiệp vụ cho trình thực thi cấu trúc thống nhất.
// Không IO ngoài lời gọi mô hình.
func decide[T any](ctx context.Context, model agentcore.ChatModel, contract llmcontract.Contract, systemPrompt, payload string, validate func(*T) error) (T, error) {
	out, err := llmcontract.Execute(ctx, model, llmcontract.Request[T]{
		Contract:     contract,
		SystemPrompt: systemPrompt,
		Payload:      payload,
		Options:      []agentcore.CallOption{agentcore.WithMaxTokens(decideMaxTokens)},
		Validate:     validate,
		Agent:        "arbiter",
		Hooks: llmcontract.Hooks{
			Resolved: func(res llmcontract.Resolution) {
				slog.Debug("chọn giao thức phán định", "module", "arbiter",
					"contract", contract.Name, "structured_mode", res.Mode,
					"capability_source", res.Source, "provider", res.Provider,
					"model", res.Model, "schema_fingerprint", contract.Fingerprint())
			},
			Correction: func(ev llmcontract.Correction) {
				slog.Warn("tự chữa đầu ra phán định", "module", "arbiter", "attempt", ev.Attempt,
					"layer", ev.Layer, "structured_mode", ev.Mode, "err", ev.Err)
			},
		},
	})
	if err != nil {
		return out, fmt.Errorf("arbiter: %w", err)
	}
	return out, nil
}

// DispatchOp là hành động giao việc dùng chung cho các kịch bản.
type DispatchOp struct {
	Agent string `json:"agent"`
	Task  string `json:"task"`
}

// workerNames là đích giao việc hợp lệ (khớp với đăng ký trong agents.BuildWorkers).
// Slice có thứ tự: vừa làm schema enum (thứ tự cố định giữ fingerprint ổn định) vừa
// làm whitelist kiểm tra.
var workerNames = []string{"architect_long", "architect_short", "writer", "editor"}

func (d *DispatchOp) validate() error {
	if d == nil {
		return nil
	}
	if !slices.Contains(workerNames, d.Agent) {
		return fmt.Errorf("dispatch.agent không hợp lệ: %q", d.Agent)
	}
	if strings.TrimSpace(d.Task) == "" {
		return fmt.Errorf("dispatch.task không được để trống")
	}
	return nil
}

// dispatchSchema là vị trí schema khả dĩ null của DispatchOp: chỉ hành động cần giao việc
// mới ra đối tượng, các trường hợp còn lại là null (chế độ strict mọi trường required,
// ngữ nghĩa tùy chọn diễn đạt bằng null).
func dispatchSchema(desc string) map[string]any {
	return llmcontract.Nullable(schema.Object(
		schema.Property("agent", schema.Enum(desc, workerNames...)).Required(),
		schema.Property("task", schema.String("mô tả nhiệm vụ đầy đủ giao cho worker này")).Required(),
	))
}

// marshalPayload tuần tự hóa gói sự kiện; thất bại tức lỗi chương trình, phải bộc lộ —
// ngầm ngụy tạo sự kiện rỗng sẽ khiến mô hình phán đoán sai trên đầu vào giả.
func marshalPayload(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("arbiter: tuần tự hóa gói sự kiện thất bại: %w", err)
	}
	return string(data), nil
}
