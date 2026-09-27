// Package llmcontract là tầng hợp đồng và thực thi thống nhất cho các lượt trả về
// cấu trúc trực tiếp: Contract tĩnh là nguồn chân lý duy nhất của cấu trúc,
// Execute gộp chung chọn năng lực, chuẩn bị prompt, thử lại yêu cầu, giải mã
// Schema/DTO và tự phục hồi bằng phản hồi.
// Giao thức được chốt trước khi gửi yêu cầu; yêu cầu native bị từ chối hoặc vi phạm
// hợp đồng sẽ được bộc lộ nguyên trạng, cấm lặng lẽ bỏ schema rồi gửi lại.
package llmcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/llm"
)

// Contract là hợp đồng tĩnh cho một lượt trả về cấu trúc trực tiếp, nằm sát định nghĩa DTO ở từng biên.
type Contract struct {
	Name        string
	Description string
	Schema      map[string]any
}

// Mode là giao thức cấu trúc được dùng cho lượt gọi này.
type Mode string

const (
	ModeNativeJSONSchema Mode = "native_json_schema"
	ModePromptContract   Mode = "prompt_contract"
)

// Source là nguồn căn cứ cho phán đoán năng lực.
type Source string

const (
	SourceConfig  Source = "config"  // người dùng khai báo tường minh trong ModelConfig.json_schema
	SourceAdapter Source = "adapter" // bảng năng lực cấp mô hình của provider adapter
	SourceUnknown Source = "unknown" // không khai báo và năng lực không rõ, thận trọng đi prompt contract
)

// Resolution là kết quả chọn giao thức được chốt trước khi gửi yêu cầu, phục vụ rẽ nhánh và ghi log cho bên gọi.
type Resolution struct {
	Mode     Mode
	Source   Source
	Strict   bool // native thì có kèm strict hay không
	Provider string
	Model    string
}

// jsonSchemaOverrider được hiện thực bởi wrapper mô hình mang theo phủ quyết ba trạng
// thái của config (bootstrap.SwappableModel và các lớp bọc chuyển tiếp nó).
type jsonSchemaOverrider interface {
	JSONSchemaOverride() *bool
}

type modelInfoProvider interface {
	Info() llm.ModelInfo
}

// ModelFacts là ảnh chụp cùng một thời điểm, đủ cho một lượt phân giải năng lực.
// Wrapper hỗ trợ hoán đổi nóng hiện thực giao diện này, để Resolve không phải đọc
// riêng lẻ năng lực, phủ quyết cấu hình và định danh mô hình rồi trộn lẫn trạng thái
// nằm giữa hai lần chuyển đổi.
type ModelFacts struct {
	Capabilities       llm.Capabilities
	Info               llm.ModelInfo
	JSONSchemaOverride *bool
}

type modelFactsProvider interface {
	StructuredOutputFacts() ModelFacts
}

// Resolve mỗi lượt gọi đều đọc fact mô hình hiện tại (sau hoán đổi nóng, lượt gọi kế
// tiếp dùng giá trị mới): phủ quyết ba trạng thái của config ưu tiên trước, kế đến
// năng lực cấp mô hình của adapter, không rõ thì luôn đi prompt contract.
func Resolve(model any) Resolution {
	res := Resolution{Mode: ModePromptContract, Source: SourceUnknown}

	var caps llm.Capabilities
	var info llm.ModelInfo
	var override *bool
	if fp, ok := model.(modelFactsProvider); ok {
		facts := fp.StructuredOutputFacts()
		caps, info, override = facts.Capabilities, facts.Info, facts.JSONSchemaOverride
	} else {
		if cp, ok := model.(llm.CapabilityProvider); ok {
			caps = cp.Capabilities()
		}
		if ip, ok := model.(modelInfoProvider); ok {
			info = ip.Info()
		}
		if o, ok := model.(jsonSchemaOverrider); ok {
			override = o.JSONSchemaOverride()
		}
	}
	res.Provider, res.Model = caps.Provider, caps.Model
	if res.Provider == "" {
		res.Provider = info.Provider
	}
	if res.Model == "" {
		res.Model = info.Name
	}

	if override != nil {
		res.Source = SourceConfig
		if *override {
			res.Mode = ModeNativeJSONSchema
			// Người dùng khai báo endpoint tuân thủ hợp đồng Structured Outputs thì mặc định
			// strict; chỉ khi adapter khẳng định không hỗ trợ strict mới chỉ gửi schema mà
			// không gửi strict.
			res.Strict = caps.Structured.Strict != llm.SupportNo
		}
		return res
	}

	switch caps.Structured.JSONSchema {
	case llm.SupportYes:
		res.Mode = ModeNativeJSONSchema
		res.Source = SourceAdapter
		res.Strict = caps.Structured.Strict == llm.SupportYes
	case llm.SupportNo:
		res.Source = SourceAdapter
	}
	return res
}

// Plan phân giải giao thức và sinh tuỳ chọn gọi ở chế độ native; chế độ prompt contract trả về opts nil.
func Plan(model any, c Contract) ([]agentcore.CallOption, Resolution) {
	res := Resolve(model)
	if res.Mode != ModeNativeJSONSchema {
		return nil, res
	}
	return []agentcore.CallOption{
		agentcore.WithJSONSchema(c.Name, c.Description, c.Schema, res.Strict),
	}, res
}

// PreparePrompt giữ đúng một bản prompt ngữ nghĩa nghiệp vụ: chế độ native trả về
// nguyên văn; chế độ prompt contract tự sinh hậu tố định dạng từ chính Schema đó.
// Bên gọi không phải duy trì bộ mẫu thứ hai, thay đổi trường cũng không làm prompt
// và response_format lệch nhau.
func PreparePrompt(base string, c Contract, res Resolution) (string, error) {
	if res.Mode != ModePromptContract {
		return base, nil
	}
	schemaJSON, err := json.Marshal(c.Schema)
	if err != nil {
		return "", fmt.Errorf("llmcontract: marshal %s prompt schema: %w", c.Name, err)
	}
	contract := "## Hợp đồng đầu ra\n\n" +
		"Chỉ xuất một đối tượng JSON phù hợp JSON Schema dưới đây, không kèm giải thích, khung Markdown hoặc chính các thẻ.\n\n" +
		"<output-json-schema>\n" + string(schemaJSON) + "\n</output-json-schema>"
	if strings.TrimSpace(base) == "" {
		return contract, nil
	}
	return strings.TrimSpace(base) + "\n\n" + contract, nil
}

// Nullable mở rộng type của một schema thành union có thể null (["<t>","null"]),
// dùng để diễn đạt "mọi trường đều required, ngữ nghĩa tuỳ chọn biểu thị bằng null"
// ở chế độ strict. Trả về bản sao, không sửa map truyền vào.
func Nullable(s map[string]any) map[string]any {
	out := maps.Clone(s)
	if t, ok := out["type"].(string); ok {
		out["type"] = []string{t, "null"}
	}
	switch values := out["enum"].(type) {
	case []string:
		enum := make([]any, 0, len(values)+1)
		for _, value := range values {
			enum = append(enum, value)
		}
		out["enum"] = append(enum, nil)
	case []any:
		enum := slices.Clone(values)
		for _, value := range enum {
			if value == nil {
				return out
			}
		}
		out["enum"] = append(enum, nil)
	}
	return out
}

// ValidateStrictReady đệ quy kiểm tra schema thoả điều kiện cấu trúc của tập con
// strict của OpenAI: thuộc tính của mọi object đều phải nằm trong required (ngữ
// nghĩa tuỳ chọn biểu thị bằng union null). litellm làm cùng phép kiểm tra này lúc
// gửi yêu cầu và tự bổ sung additionalProperties:false; kiểm thử hợp đồng dùng hàm
// này để khẳng định trước (RFC §11.1), không để vấn đề cấu trúc tồn đến lúc chạy.
func ValidateStrictReady(s map[string]any) error {
	return validateStrictReady(s, "$")
}

func validateStrictReady(s map[string]any, path string) error {
	if typeIncludes(s["type"], "object") {
		props, _ := s["properties"].(map[string]any)
		required, _ := s["required"].([]string)
		for name, sub := range props {
			if !slices.Contains(required, name) {
				return fmt.Errorf("%s.%s chưa nằm trong required (strict đòi hỏi mọi thuộc tính đều required)", path, name)
			}
			if subMap, ok := sub.(map[string]any); ok {
				if err := validateStrictReady(subMap, path+"."+name); err != nil {
					return err
				}
			}
		}
	}
	if items, ok := s["items"].(map[string]any); ok {
		return validateStrictReady(items, path+"[]")
	}
	return nil
}

func typeIncludes(t any, want string) bool {
	switch v := t.(type) {
	case string:
		return v == want
	case []string:
		return slices.Contains(v, want)
	}
	return false
}

// Fingerprint trả về 12 ký tự hex đầu của sha256 JSON chuẩn hoá của schema, dùng
// để liên kết log; encoding/json sắp xếp khoá map nên cùng một hợp đồng vốn dĩ ổn định.
func (c Contract) Fingerprint() string {
	data, err := json.Marshal(c.Schema)
	if err != nil {
		return "unmarshalable"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}
