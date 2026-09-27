package bootstrap

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/voocel/agentcore/llm"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/models"
	"github.com/voocel/ainovel-cli/internal/notify"
	"github.com/voocel/ainovel-cli/internal/styles"
	"github.com/voocel/ainovel-cli/internal/utils"
)

// DefaultContextWindow là kích thước cửa sổ buộc phải (fallback) khi mô hình chưa
// đăng ký trong registry.
const DefaultContextWindow = 200000

// CompactRatio ngưỡng tương đối kích hoạt nén ngữ cảnh: nén khi tokens >= window * CompactRatio.
// 0.85 là giá trị kinh nghiệm, chừa 15% đầu cho "prompt vòng sau + kết quả công cụ lớn",
// đồng thời để mô hình cửa sổ lớn cũng chủ động nén ở 85%, tránh phải đợi ăn sạch cửa sổ
// danh nghĩa 1M mới nén (vùng suy giảm chú ý).
//
// Tỉ lệ nén không mở cho người dùng cấu hình; người dùng chỉ cấu hình context_window
// thật của từng mô hình.
const CompactRatio = 0.85

// MinCompactReserve là cận dưới của ReserveTokens. Mô hình cửa sổ nhỏ (như qwen3:8b
// cục bộ 32k) tính reserve theo tỉ lệ 0.15 chỉ được 4800, trong khi một lần phản hồi
// công cụ commit_chapter đã có thể nhét 5-8k, một chương chính văn 8-15k — sẽ xảy ra
// "nén xong lập tức lại vượt". Cận 8000 buộc phải bảo đảm kịch bản xấu nhất vẫn còn
// nửa vòng đệm.
const MinCompactReserve = 8000

// CompactReserveTokens tính ngược ReserveTokens theo CompactRatio và áp floor MinCompactReserve:
//
//	threshold = window - reserve = window * CompactRatio
//	reserve   = max(MinCompactReserve, window * (1 - CompactRatio))
//
// Dùng cho EngineConfig.ReserveTokens của agentcore.context.Engine.
func CompactReserveTokens(window int) int {
	if window <= 0 {
		return 0
	}
	reserve := window - int(float64(window)*CompactRatio)
	if reserve < MinCompactReserve {
		return MinCompactReserve
	}
	return reserve
}

// ProviderConfig định nghĩa thông tin xác thực của một nhà cung cấp LLM.
type ProviderConfig struct {
	Type    string        `json:"type,omitempty"`     // loại giao thức API (openai/anthropic/gemini), chỉ định khi dùng proxy tùy chỉnh
	API     string        `json:"api,omitempty"`      // endpoint giao thức OpenAI: chat (mặc định) / responses
	APIKey  string        `json:"api_key,omitempty"`  // API Key
	BaseURL string        `json:"base_url,omitempty"` // API Base URL
	Models  []ModelConfig `json:"models,omitempty"`   // danh sách mô hình tùy chọn, để hiển thị khi chuyển đổi trong TUI
	// ExtraBody là các tham số bổ sung truyền nguyên văn cho mỗi yêu cầu của provider
	// này (như temperature/top_p/min_p/presence_penalty, hoặc khóa riêng của hãng như
	// chat_template_kwargs bật think trên nvidia). Đầu OpenAI-compatible nhập nguyên
	// văn vào thân yêu cầu (tức quy ước extra_body); giá trị do người dùng tự chịu trách nhiệm.
	ExtraBody map[string]any `json:"extra_body,omitempty"`
	// Extra truyền cho cấu hình cấp provider (litellm.ProviderConfig.Extra), dùng cho
	// các tùy chọn tầng client/transport như HTTP headers, user_agent, anthropic_beta.
	Extra map[string]any `json:"extra,omitempty"`
	// StreamIdleTimeout là watchdog rảnh rỗi khi phát luồng: quá thời gian này mà
	// không nhận được chunk nào thì ngắt luồng (chuỗi Go duration, như "900s" / "15m").
	// Bỏ trống mặc định 5m — cận trên hợp lý cho dịch vụ đám mây; đầu tự dựng suy luận
	// chậm như LocalAI/ollama có thể quá 5 phút mới có khối đầu, chỉ cần nới lỏng theo
	// từng provider, không kéo theo việc phát hiện treo của các kênh khác (#79).
	StreamIdleTimeout string `json:"stream_idle_timeout,omitempty"`
}

// ModelConfig mô tả mô hình có thể chuyển đổi dưới một provider cùng cửa sổ ngữ cảnh
// tùy chọn của nó. Để tương thích cấu hình cũ, có thể đọc từ chuỗi JSON ("model-name")
// hoặc từ đối tượng; khi ghi lại luôn chuẩn hóa về dạng đối tượng.
type ModelConfig struct {
	Name          string `json:"name"`
	ContextWindow int    `json:"context_window,omitempty"`
	// JSONSchema là khai báo ba trạng thái cho xuất có cấu trúc gốc (response_format
	// json_schema): chưa cấu hình = phán đoán theo năng lực mức mô hình của provider
	// adapter; true = người dùng khai báo endpoint/mô hình này hỗ trợ (khi yêu cầu bị
	// từ chối thì bộc lộ nguyên trạng, không hạ cấp âm thầm); false = cưỡng chế đi
	// prompt contract. Năng lực của proxy tùy chỉnh và cổng tổng hợp lấy theo khai báo
	// của người dùng, chương trình không dò đo.
	JSONSchema *bool `json:"json_schema,omitempty"`
}

func (m *ModelConfig) UnmarshalJSON(data []byte) error {
	var legacy string
	if err := json.Unmarshal(data, &legacy); err == nil {
		m.Name = legacy
		m.ContextWindow = 0
		m.JSONSchema = nil
		return nil
	}
	type modelConfigAlias ModelConfig
	var decoded modelConfigAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("model config must be a string or object: %w", err)
	}
	*m = ModelConfig(decoded)
	return nil
}

// ModelConfig trả về cấu hình tường minh của mô hình chỉ định.
func (pc ProviderConfig) ModelConfig(name string) (ModelConfig, bool) {
	name = strings.TrimSpace(name)
	for _, model := range pc.Models {
		if strings.TrimSpace(model.Name) == name {
			return model, true
		}
	}
	return ModelConfig{}, false
}

// ModelJSONSchema trả về khai báo ba trạng thái json_schema của mô hình; khi không
// nằm trong models hoặc chưa cấu hình thì trả nil (phán đoán theo năng lực adapter).
func (c Config) ModelJSONSchema(provider, model string) *bool {
	if pc, ok := c.Providers[provider]; ok {
		if mc, ok := pc.ModelConfig(model); ok {
			return mc.JSONSchema
		}
	}
	return nil
}

// defaultStreamIdleTimeout: trong kịch bản xuất dài + ctx dài, provider có nhận
// thức suy luận (mimo / deepseek-r1...) nếu phía server không phát reasoning delta
// theo luồng thì cả đoạn SSE im lặng. Watchdog mặc định của litellm là 2 phút, với
// chương viết 8000 chữ thường giết oan; 5 phút phủ gần hết ca thực đo (xem thống kê
// thời gian suy luận plan→draft trong tasks/todo.md).
const defaultStreamIdleTimeout = 5 * time.Minute

// StreamIdleTimeoutValue phân tích thời gian chờ rảnh rỗi luồng của provider này;
// bỏ trống thì rơi về giá trị mặc định.
func (pc ProviderConfig) StreamIdleTimeoutValue() (time.Duration, error) {
	s := strings.TrimSpace(pc.StreamIdleTimeout)
	if s == "" {
		return defaultStreamIdleTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use Go duration like \"900s\" / \"15m\")", s)
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive, got %q", s)
	}
	return d, nil
}

// RequiresAPIKey trả về provider này có bắt buộc cấu hình api_key tường minh hay không.
// Quy ước:
// 1. ollama / bedrock cho phép không có key;
// 2. cấu hình chỉ định Type tường minh được coi là proxy tùy chỉnh, cho phép không key;
// 3. provider khác mặc định yêu cầu key, giữ kiểm tra bảo thủ với giao diện chính hãng.
func (pc ProviderConfig) RequiresAPIKey(name string) bool {
	switch name {
	case "ollama", "bedrock":
		return false
	}
	return pc.Type == ""
}

// ProviderType trả về loại giao thức API hợp lệ.
// Ưu tiên Type tường minh; nếu không thì yêu cầu tên provider đã có trong bảng
// đăng ký litellm.
func (pc ProviderConfig) ProviderType(name string) (string, error) {
	if pc.Type != "" {
		return pc.Type, nil
	}
	if llm.IsProviderRegistered(name) {
		return name, nil
	}
	return "", fmt.Errorf("provider %q thiếu type, và không nằm trong danh sách provider đã biết của litellm: %w", name, errs.ErrConfig)
}

// ModelRef biểu thị một tổ hợp provider/model.
type ModelRef struct {
	Provider string `json:"provider"` // tên provider (key trong map Providers)
	Model    string `json:"model"`    // tên mô hình (truyền nguyên trạng, không phân tích gì)
}

// RoleConfig định nghĩa lớp phủ mô hình cho một vai trò.
type RoleConfig struct {
	Provider  string     `json:"provider"`            // tên provider chính (key trong map Providers)
	Model     string     `json:"model"`               // tên mô hình chính (truyền nguyên trạng, không phân tích gì)
	Fallbacks []ModelRef `json:"fallbacks,omitempty"` // danh sách provider/model dự phòng tường minh
	// ReasoningEffort mức độ suy luận của vai trò này (off/low/medium/high/xhigh/max),
	// rỗng = kế thừa mặc định cấp cao nhất. Do agents.ParseThinkingLevel kiểm tra trước
	// khi áp dụng, giá trị vượt cấp được coi là rỗng.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// knownRoles là các tên vai trò có thể cấu hình. Arbiter hiện không mở cấu hình
// cấp vai trò, thống nhất dùng mô hình mặc định cấp cao nhất (host.arbiterModel dùng
// models.Default). import_* là núm vặn cấp mô hình của hàm ngữ nghĩa nhập
// (docs/import-pipeline.md §13.1): chưa cấu hình thì rơi về architect, sau khi cấu
// hình có thể chỉ các hàm mang tính cơ giới hơn sang cấp rẻ hơn.
var knownRoles = map[string]bool{
	"architect":         true,
	"writer":            true,
	"editor":            true,
	"import_segment":    true,
	"import_analyze":    true,
	"import_synthesize": true,
}

// Config cấu hình ứng dụng tiểu thuyết.
type Config struct {
	// Trường runtime (không tuần tự hóa ra JSON)
	OutputDir string `json:"-"` // thư mục gốc xuất

	// Cấu hình LLM mặc định
	Provider  string `json:"provider"` // provider mặc định (key trong map Providers)
	ModelName string `json:"model"`    // tên mô hình mặc định
	// ReasoningEffort mức suy luận mặc định cấp cao nhất (off/low/medium/high/xhigh/max),
	// rỗng = không phủ (theo mặc định mô hình/provider). Vai trò không cấu hình riêng
	// reasoning_effort thì rơi về giá trị này.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`

	// Kho thông tin xác thực provider
	Providers map[string]ProviderConfig `json:"providers,omitempty"`

	// Lớp phủ mô hình cấp vai trò
	Roles map[string]RoleConfig `json:"roles,omitempty"`

	// Tham số sáng tác
	Style    string `json:"style,omitempty"`
	Language string `json:"language,omitempty"` // Ngôn ngữ sáng tác tiểu thuyết: "vi" (Tiếng Việt, mặc định) hoặc "zh" (Tiếng Trung)

	// ContextWindow là cửa sổ ngữ cảnh toàn cục phiên bản cũ, giữ lại để tương thích.
	ContextWindow int `json:"context_window,omitempty"`

	// Budget chính sách ngân sách chi phí cho một cuốn sách; chỉ kích hoạt khi book_usd > 0.
	Budget BudgetConfig `json:"budget,omitzero"`

	// Notify cấu hình cảnh báo chế độ không người trực; mặc định bật (kênh system buộc phải).
	Notify NotifyConfig `json:"notify,omitzero"`
}

// BudgetConfig là tuyên bố chính sách ví tiền của người dùng cho một cuốn sách. Dừng
// máy khi vượt ngưỡng coi như người dùng tay Abort đúng khoảnh khắc đó — Host chỉ thay
// mặt thực thi, không đánh giá hành vi mô hình (biên hợp hiến kiến trúc §10).
type BudgetConfig struct {
	BookUSD   float64 `json:"book_usd,omitempty"`   // bắt buộc có mới kích hoạt; 0/mặc định = không giới hạn
	WarnRatio float64 `json:"warn_ratio,omitempty"` // mực nước cảnh báo, mặc định 0.8
	HardStop  bool    `json:"hard_stop,omitempty"`  // true = vượt ngưỡng dừng ngay; mặc định đợi tác vụ subagent hiện tại kết thúc
}

// Enabled trả về chính sách ngân sách có được kích hoạt hay không.
func (b BudgetConfig) Enabled() bool { return b.BookUSD > 0 }

// NotifyConfig cấu hình kênh cảnh báo chế độ không người trực.
type NotifyConfig struct {
	Enabled *bool    `json:"enabled,omitempty"` // mặc định true (kênh system dùng được không cần cấu hình)
	Command string   `json:"command,omitempty"` // tùy chọn, sau khi cấu hình sẽ thay thế kênh system (đẩy về điện thoại đi qua đây)
	Events  []string `json:"events,omitempty"`  // tùy chọn, lọc theo notify.Kinds; mặc định mở tất cả
}

// IsEnabled trả về cảnh báo có được kích hoạt hay không (mặc định true).
func (n NotifyConfig) IsEnabled() bool { return n.Enabled == nil || *n.Enabled }

// ValidateBase kiểm tra cấu hình cơ bản.
func (c *Config) ValidateBase() error {
	if err := validateConfigText("provider", c.Provider); err != nil {
		return err
	}
	if err := validateConfigText("model", c.ModelName); err != nil {
		return err
	}

	if c.Provider == "" {
		return fmt.Errorf("provider is required: %w", errs.ErrConfig)
	}
	if c.ModelName == "" {
		return fmt.Errorf("model is required: %w", errs.ErrConfig)
	}

	// Provider mặc định phải có thông tin xác thực
	pc, ok := c.Providers[c.Provider]
	if !ok {
		return fmt.Errorf("provider %q chưa cấu hình thông tin xác thực trong providers; nếu đã đè provider trong ./.ainovel/config.json, phải đồng thời khai báo providers.%s (gồm api_key/base_url), không thể chỉ đổi provider cấp cao nhất: %w", c.Provider, c.Provider, errs.ErrConfig)
	}
	if pc.RequiresAPIKey(c.Provider) && pc.APIKey == "" {
		return fmt.Errorf("provider %q has no api_key configured: %w", c.Provider, errs.ErrConfig)
	}
	if err := validateProviderConfigText(c.Provider, pc); err != nil {
		return err
	}
	if err := c.validateProviderAPI("default", c.Provider, pc); err != nil {
		return err
	}
	for name, provider := range c.Providers {
		if err := validateConfigText("provider name", name); err != nil {
			return err
		}
		if err := validateProviderConfigText(name, provider); err != nil {
			return err
		}
		if err := c.validateProviderAPI(fmt.Sprintf("provider %q", name), name, provider); err != nil {
			return err
		}
	}

	// Kiểm tra lớp phủ vai trò
	for role, rc := range c.Roles {
		if err := validateConfigText("role name", role); err != nil {
			return err
		}
		if err := validateConfigText(fmt.Sprintf("role %q provider", role), rc.Provider); err != nil {
			return err
		}
		if err := validateConfigText(fmt.Sprintf("role %q model", role), rc.Model); err != nil {
			return err
		}
		if !knownRoles[role] {
			return fmt.Errorf("unknown role %q in roles config (valid: architect/writer/editor/import_segment/import_analyze/import_synthesize): %w", role, errs.ErrConfig)
		}
		if rc.Provider == "" || rc.Model == "" {
			return fmt.Errorf("role %q must have both provider and model: %w", role, errs.ErrConfig)
		}
		if err := c.validateModelRef(
			fmt.Sprintf("role %q", role),
			ModelRef{Provider: rc.Provider, Model: rc.Model},
		); err != nil {
			return err
		}
		for i, fallback := range rc.Fallbacks {
			if err := validateConfigText(fmt.Sprintf("role %q fallback[%d] provider", role, i), fallback.Provider); err != nil {
				return err
			}
			if err := validateConfigText(fmt.Sprintf("role %q fallback[%d] model", role, i), fallback.Model); err != nil {
				return err
			}
			if err := c.validateModelRef(
				fmt.Sprintf("role %q fallback[%d]", role, i),
				fallback,
			); err != nil {
				return err
			}
		}
	}

	// Kiểm tra chính sách ngân sách
	if c.Budget.BookUSD < 0 {
		return fmt.Errorf("budget.book_usd must be >= 0: %w", errs.ErrConfig)
	}
	if c.Budget.Enabled() && (c.Budget.WarnRatio <= 0 || c.Budget.WarnRatio >= 1) {
		return fmt.Errorf("budget.warn_ratio must be in (0, 1): %w", errs.ErrConfig)
	}

	// Kiểm tra cấu hình cảnh báo
	if err := validateConfigText("notify.command", c.Notify.Command); err != nil {
		return err
	}
	for _, ev := range c.Notify.Events {
		if !notify.IsKnownKind(ev) {
			return fmt.Errorf("unknown notify event %q (valid: %s): %w", ev, strings.Join(notify.Kinds(), "/"), errs.ErrConfig)
		}
	}

	return nil
}

func validateProviderConfigText(name string, pc ProviderConfig) error {
	fields := []struct {
		label string
		value string
	}{
		{label: fmt.Sprintf("provider %q type", name), value: pc.Type},
		{label: fmt.Sprintf("provider %q api", name), value: pc.API},
		{label: fmt.Sprintf("provider %q api_key", name), value: pc.APIKey},
		{label: fmt.Sprintf("provider %q base_url", name), value: pc.BaseURL},
	}
	for _, field := range fields {
		if err := validateConfigText(field.label, field.value); err != nil {
			return err
		}
	}
	seenModels := make(map[string]bool, len(pc.Models))
	for i, model := range pc.Models {
		modelName := strings.TrimSpace(model.Name)
		if err := validateConfigText(fmt.Sprintf("provider %q models[%d].name", name, i), model.Name); err != nil {
			return err
		}
		if modelName == "" {
			return fmt.Errorf("provider %q models[%d].name is required: %w", name, i, errs.ErrConfig)
		}
		if seenModels[modelName] {
			return fmt.Errorf("provider %q has duplicate model %q: %w", name, modelName, errs.ErrConfig)
		}
		seenModels[modelName] = true
		if model.ContextWindow < 0 {
			return fmt.Errorf("provider %q model %q context_window must be >= 0: %w", name, modelName, errs.ErrConfig)
		}
	}
	switch pc.API {
	case "", "chat", "responses":
	default:
		return fmt.Errorf("provider %q api must be chat or responses: %w", name, errs.ErrConfig)
	}
	if _, err := pc.StreamIdleTimeoutValue(); err != nil {
		return fmt.Errorf("provider %q stream_idle_timeout: %w: %w", name, err, errs.ErrConfig)
	}
	return nil
}

func validateConfigText(name, value string) error {
	if utils.ContainsControl(value) {
		return fmt.Errorf("%s contains control character: %w", name, errs.ErrConfig)
	}
	return nil
}

// DefaultProviderConfig trả về cấu hình thông tin xác thực của provider mặc định.
func (c *Config) DefaultProviderConfig() ProviderConfig {
	if c.Providers == nil {
		return ProviderConfig{}
	}
	return c.Providers[c.Provider]
}

// FillDefaults điền giá trị mặc định.
func (c *Config) FillDefaults() {
	if c.OutputDir == "" {
		c.OutputDir = filepath.Join("output", "novel")
	}
	if c.Providers == nil {
		c.Providers = make(map[string]ProviderConfig)
	}
	if c.Roles == nil {
		c.Roles = make(map[string]RoleConfig)
	}
	if c.Style == "" {
		c.Style = "default"
	}
	if c.Language == "" {
		c.Language = "vi"
	} else {
		c.Language = strings.ToLower(strings.TrimSpace(c.Language))
	}
	if c.Budget.Enabled() && c.Budget.WarnRatio == 0 {
		c.Budget.WarnRatio = 0.8
	}
}

// NormalizedLanguage trả về ngôn ngữ nội dung tiểu thuyết đã chuẩn hóa ("vi" hoặc "zh").
func (c Config) NormalizedLanguage() string {
	lang := strings.ToLower(strings.TrimSpace(c.Language))
	if lang == "zh" || lang == "chinese" || lang == "cn" {
		return "zh"
	}
	return "vi"
}

// ApplyGenreStyle suy style đặc thù từ chuỗi thể loại tự do (user_rules.genre, prompt khởi động…)
// khi cấu hình chưa chọn style đặc thù, xem R3 trong docs/plans/style-genre-heading-fix.md.
//
// Chỉ áp khi Style hiện tại rỗng hoặc "default" — lựa chọn tường minh của người dùng
// (fantasy/romance/wuxia…) luôn thắng, giữ tương thích ngược với config cũ. Suy từ
// styles.ResolveStyleFromGenre nên khớp bỏ hoa thường và dấu ("Tiên Hiệp" == "tien hiep").
// Trả về true khi cfg.Style đã được đổi sang style đặc thù; không suy ra được thì giữ nguyên.
func (c *Config) ApplyGenreStyle(genre string) bool {
	if c.Style != "" && c.Style != "default" {
		return false
	}
	key, ok := styles.ResolveStyleFromGenre(genre)
	if !ok || key == "default" || key == "" {
		return false
	}
	c.Style = key
	return true
}

// ContextWindowSource đánh dấu nguồn của giá trị cửa sổ, dùng cho nhật ký/chẩn đoán.
type ContextWindowSource string

const (
	CtxWindowModelConfig ContextWindowSource = "model_config" // mục mô hình của provider chỉ định tường minh
	CtxWindowConfig      ContextWindowSource = "config"       // context_window cấp cao nhất bản cũ chỉ định tường minh
	CtxWindowRegistry    ContextWindowSource = "registry"     // trúng đường cơ sở OpenRouter
	CtxWindowDefault     ContextWindowSource = "default"      // buộc phải (proxy tùy chỉnh/mô hình không rõ)
)

// ResolveContextWindow phân tích cửa sổ hiệu dụng dùng cho nén ngữ cảnh, theo ưu tiên:
//  1. providers.<provider>.models[].context_window
//  2. ContextWindow cấp cao nhất bản cũ (tương thích cấu hình sẵn có)
//  3. models.DefaultRegistry tra theo tên mô hình (đường cơ sở OpenRouter + làm mới 24h)
//  4. buộc phải DefaultContextWindow (proxy tùy chỉnh / mô hình không rõ)
//
// Chú ý: giá trị trả về chỉ dùng tính ngưỡng nén, không thu nhỏ độ dài yêu cầu thật
// có thể gửi qua LLM API.
func (c Config) ResolveContextWindow(provider, modelName string) (int, ContextWindowSource) {
	if pc, ok := c.Providers[strings.TrimSpace(provider)]; ok {
		if model, found := pc.ModelConfig(modelName); found && model.ContextWindow > 0 {
			return model.ContextWindow, CtxWindowModelConfig
		}
	}
	if c.ContextWindow > 0 {
		return c.ContextWindow, CtxWindowConfig
	}
	if rw := models.DefaultRegistry().ResolveContextWindow(modelName); rw > 0 {
		return rw, CtxWindowRegistry
	}
	return DefaultContextWindow, CtxWindowDefault
}

// ResolveReasoningEffort trả về chuỗi gốc mức suy luận có hiệu lực của một vai trò
// (off/low/medium/high/xhigh/max hoặc rỗng). Ưu tiên: cấp vai trò
// Roles[role].ReasoningEffort → mặc định cấp cao nhất ReasoningEffort → "" (không
// phủ, theo mặc định mô hình/provider). Khi role rỗng hoặc "default" lấy thẳng mặc
// định cấp cao nhất. Tính hợp lệ của giá trị do agents.ParseThinkingLevel kiểm soát.
func (c Config) ResolveReasoningEffort(role string) string {
	if role != "" && role != "default" {
		if rc, ok := c.Roles[role]; ok && rc.ReasoningEffort != "" {
			return rc.ReasoningEffort
		}
	}
	return c.ReasoningEffort
}

// LogContextWindowChoice in ra quyết định cửa sổ của một vai trò. Khi source=default
// phát Warn nhắc mô hình này không trúng registry (OpenRouter cũng chưa thu thập),
// nén ngữ cảnh sau đó sẽ kích hoạt theo cửa sổ buộc phải — nếu cửa sổ thật của mô hình
// lớn hơn, có thể chỉ định tường minh context_window trong tệp cấu hình, tránh bị nén
// sớm, mất lịch sử.
func LogContextWindowChoice(role, model string, window int, source ContextWindowSource) {
	attrs := []any{"module", "context", "role", role, "model", model, "window", window, "source", source}
	switch source {
	case CtxWindowModelConfig:
		slog.Info("cửa sổ ngữ cảnh (từ cấu hình mô hình của provider)", attrs...)
	case CtxWindowDefault:
		slog.Warn("mô hình chưa nhận diện, dùng cửa sổ buộc phải (có thể chỉ định tường minh tại providers.<name>.models[].context_window)", attrs...)
	case CtxWindowConfig:
		slog.Info("cửa sổ ngữ cảnh (từ context_window trong tệp cấu hình)", attrs...)
	default:
		slog.Info("cửa sổ ngữ cảnh", attrs...)
	}
}

// CandidateModels trả về danh sách mô hình có thể chuyển đổi dưới một provider.
// Ưu tiên models được provider khai báo tường minh; đồng thời bổ sung các mô hình của
// provider này từng xuất hiện trong cấu hình hiện tại.
func (c Config) CandidateModels(provider string) []string {
	if provider == "" {
		return nil
	}

	seen := make(map[string]bool)
	models := make([]string, 0, 4)
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		models = append(models, model)
	}

	if pc, ok := c.Providers[provider]; ok {
		for _, model := range pc.Models {
			add(model.Name)
		}
	}
	if c.Provider == provider {
		add(c.ModelName)
	}
	for _, rc := range c.Roles {
		if rc.Provider == provider {
			add(rc.Model)
		}
		for _, fallback := range rc.Fallbacks {
			if fallback.Provider == provider {
				add(fallback.Model)
			}
		}
	}
	return models
}

func (c Config) validateModelRef(owner string, ref ModelRef) error {
	if ref.Provider == "" || ref.Model == "" {
		return fmt.Errorf("%s must have both provider and model: %w", owner, errs.ErrConfig)
	}

	pc, ok := c.Providers[ref.Provider]
	if !ok {
		return fmt.Errorf("%s references provider %q which is not configured: %w", owner, ref.Provider, errs.ErrConfig)
	}
	if pc.RequiresAPIKey(ref.Provider) && pc.APIKey == "" {
		return fmt.Errorf("%s references provider %q which has no api_key: %w", owner, ref.Provider, errs.ErrConfig)
	}
	if err := c.validateProviderAPI(owner, ref.Provider, pc); err != nil {
		return err
	}
	return nil
}

func (c Config) validateProviderAPI(owner, providerName string, pc ProviderConfig) error {
	if pc.API == "" {
		return nil
	}
	providerType, err := pc.ProviderType(providerName)
	if err != nil {
		return fmt.Errorf("%s provider %q cấu hình api không phân tích được loại giao thức: %w", owner, providerName, err)
	}
	if strings.ToLower(strings.TrimSpace(providerType)) != "openai" {
		return fmt.Errorf("%s provider %q api chỉ hỗ trợ provider giao thức OpenAI: %w", owner, providerName, errs.ErrConfig)
	}
	return nil
}
