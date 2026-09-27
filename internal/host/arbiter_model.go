package host

import (
	"context"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// usageTrackedModel nối theo dõi lượng dùng vào gọi model: token/chi phí phải vào ngân sách và hệ thống
// usage, nếu không hạn mức ngân sách mù với chi tiêu, lượng dùng UI sai. Danh tính ghi sổ dùng agentName
// truyền vào — nhập về architect, phán định về arbiter (UsageTracker tính giá role lạ theo bảng giá Default).
type usageTrackedModel struct {
	inner     agentcore.ChatModel
	agentName string
	record    func(agentName, task string, msg agentcore.AgentMessage)
}

func newUsageTrackedModel(inner agentcore.ChatModel, agentName string, record func(string, string, agentcore.AgentMessage)) agentcore.ChatModel {
	if record == nil {
		return inner
	}
	tracked := &usageTrackedModel{inner: inner, agentName: agentName, record: record}
	if capabilities, ok := inner.(llm.CapabilityProvider); ok {
		return &capabilityUsageTrackedModel{usageTrackedModel: tracked, capabilities: capabilities}
	}
	return tracked
}

// capabilityUsageTrackedModel giữ giao diện năng lực tùy chọn của model nền. Wrapper không được
// xóa "không hỗ trợ thinking" thành "năng lực không rõ", nếu không tầng trên sẽ sinh tham số provider không chấp nhận.
type capabilityUsageTrackedModel struct {
	*usageTrackedModel
	capabilities llm.CapabilityProvider
}

func (m *capabilityUsageTrackedModel) Capabilities() llm.Capabilities {
	return m.capabilities.Capabilities()
}

// JSONSchemaOverride xuyên truyền khai báo ba trạng thái config json_schema của model nền; inner không
// mang thì trả nil ("chưa cấu hình"), không ngụy tạo năng lực.
func (m *capabilityUsageTrackedModel) JSONSchemaOverride() *bool {
	if o, ok := m.usageTrackedModel.inner.(interface{ JSONSchemaOverride() *bool }); ok {
		return o.JSONSchemaOverride()
	}
	return nil
}

func (m *capabilityUsageTrackedModel) StructuredOutputFacts() llmcontract.ModelFacts {
	if provider, ok := m.usageTrackedModel.inner.(interface {
		StructuredOutputFacts() llmcontract.ModelFacts
	}); ok {
		return provider.StructuredOutputFacts()
	}
	return llmcontract.ModelFacts{
		Capabilities:       m.Capabilities(),
		JSONSchemaOverride: m.JSONSchemaOverride(),
	}
}

func (m *usageTrackedModel) Generate(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	resp, err := m.inner.Generate(ctx, msgs, tools, opts...)
	if err == nil && resp != nil {
		m.record(m.agentName, "", resp.Message)
	}
	return resp, err
}

func (m *usageTrackedModel) GenerateStream(ctx context.Context, msgs []agentcore.Message, tools []agentcore.ToolSpec, opts ...agentcore.CallOption) (<-chan agentcore.StreamEvent, error) {
	// Arbiter chỉ đi Generate; đường streaming xuyên truyền (nếu sau này chuyển sang streaming, usage do bên tiêu thụ bù ghi).
	return m.inner.GenerateStream(ctx, msgs, tools, opts...)
}

func (m *usageTrackedModel) SupportsTools() bool { return m.inner.SupportsTools() }
