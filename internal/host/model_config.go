package host

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

type APIKeyAction string

const (
	APIKeyKeep    APIKeyAction = "keep"
	APIKeyReplace APIKeyAction = "replace"
	APIKeyClear   APIKeyAction = "clear"
)

// ProviderSnapshot là cấu hình provider đã redact dùng cho TUI.
type ProviderSnapshot struct {
	Name           string
	Type           string
	API            string
	BaseURL        string
	Models         []bootstrap.ModelConfig
	HasAPIKey      bool
	APIKeyHint     string
	RequiresAPIKey bool
}

type ModelConfigurationSnapshot struct {
	Providers       []ProviderSnapshot
	DefaultProvider string
	DefaultModel    string
	ConfigPath      string
	References      map[string][]string
}

func (s ModelConfigurationSnapshot) ReferencesFor(provider, model string) []string {
	return append([]string(nil), s.References[modelReferenceKey(provider, model)]...)
}

// ModelConfigurationDraft là bản nháp cấu hình một provider do /config gửi cho Host.
// Chỉ mô tả định nghĩa của provider đó (protocol/chứng nhiệm/thư viện model), không chứa "hiện đang dùng cái nào" — việc chuyển thuộc về /model.
type ModelConfigurationDraft struct {
	Provider     string
	Type         string
	API          string
	BaseURL      string
	Models       []bootstrap.ModelConfig
	Renames      []ModelRename
	APIKeyAction APIKeyAction
	APIKey       string
}

// ModelRename mô tả sự thay đổi ID của cùng một cấu hình model. Nó không phải phỏng đoán "xóa cũ thêm mới",
// Host chỉ chuyển đổi tham chiếu default, role và fallback khi TUI rõ ràng nộp quan hệ này.
type ModelRename struct {
	From string
	To   string
}

type ConfiguredModel struct {
	Name          string
	ContextWindow int
	ContextSource bootstrap.ContextWindowSource
}

func modelReferenceKey(provider, model string) string {
	return strings.TrimSpace(provider) + "\x00" + strings.TrimSpace(model)
}

// MaskAPIKey chỉ giữ đầu đuôi đủ nhận diện chứng nhiệm; chứng nhiệm ngắn thì ẩn hết.
// TUI chỉ nhận kết quả này, tuyệt đối không giữ API Key đầy đủ trong cấu hình.
func MaskAPIKey(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) < 16 {
		return "******"
	}
	return string(runes[:4]) + "******" + string(runes[len(runes)-4:])
}

// ModelConfiguration trả cấu hình đã redact, đích ghi được và tham chiếu model, tuyệt đối không phơi API Key hiện có.
func (h *Host) ModelConfiguration() ModelConfigurationSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	providers := make([]ProviderSnapshot, 0, len(h.cfg.Providers))
	for name, pc := range h.cfg.Providers {
		providers = append(providers, ProviderSnapshot{
			Name: name, Type: pc.Type, API: pc.API, BaseURL: pc.BaseURL,
			Models:    modelConfigurations(h.cfg, name, pc),
			HasAPIKey: pc.APIKey != "", APIKeyHint: MaskAPIKey(pc.APIKey),
			RequiresAPIKey: pc.RequiresAPIKey(name),
		})
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })

	refs := make(map[string][]string)
	refs[modelReferenceKey(h.cfg.Provider, h.cfg.ModelName)] = append(
		refs[modelReferenceKey(h.cfg.Provider, h.cfg.ModelName)], "default")
	for role, rc := range h.cfg.Roles {
		key := modelReferenceKey(rc.Provider, rc.Model)
		refs[key] = append(refs[key], role)
		for i, fallback := range rc.Fallbacks {
			key = modelReferenceKey(fallback.Provider, fallback.Model)
			refs[key] = append(refs[key], fmt.Sprintf("%s fallback[%d]", role, i))
		}
	}
	for key := range refs {
		sort.Strings(refs[key])
	}

	return ModelConfigurationSnapshot{
		Providers: providers, DefaultProvider: h.cfg.Provider, DefaultModel: h.cfg.ModelName,
		ConfigPath: h.configPath, References: refs,
	}
}

func modelConfigurations(cfg bootstrap.Config, provider string, pc bootstrap.ProviderConfig) []bootstrap.ModelConfig {
	models := make([]bootstrap.ModelConfig, 0)
	for _, modelName := range cfg.CandidateModels(provider) {
		model, ok := pc.ModelConfig(modelName)
		if !ok {
			model = bootstrap.ModelConfig{Name: modelName}
		}
		models = append(models, model)
	}
	return models
}

func (h *Host) ConfiguredModelOptions(provider string) []ConfiguredModel {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := h.cfg.CandidateModels(provider)
	out := make([]ConfiguredModel, 0, len(names))
	for _, name := range names {
		window, source := h.cfg.ResolveContextWindow(provider, name)
		out = append(out, ConfiguredModel{Name: name, ContextWindow: window, ContextSource: source})
	}
	return out
}

type preparedProviderDraft struct {
	draft     ModelConfigurationDraft
	candidate bootstrap.Config
	provider  bootstrap.ProviderConfig
	oldModels []bootstrap.ModelConfig
}

// prepareProviderDraftLocked chuẩn hóa bản nháp TUI và hợp nhất vào bản sao cấu hình, đường lưu và kiểm tra kết nối dùng chung một chuỗi kiểm tra.
func (h *Host) prepareProviderDraftLocked(draft ModelConfigurationDraft) (preparedProviderDraft, error) {
	draft.Provider = strings.TrimSpace(draft.Provider)
	draft.Type = strings.ToLower(strings.TrimSpace(draft.Type))
	draft.API = strings.ToLower(strings.TrimSpace(draft.API))
	draft.BaseURL = strings.TrimSpace(draft.BaseURL)
	draft.APIKey = strings.TrimSpace(draft.APIKey)
	if draft.Provider == "" {
		return preparedProviderDraft{}, fmt.Errorf("provider không được để trống")
	}
	if len(draft.Models) == 0 {
		return preparedProviderDraft{}, fmt.Errorf("vui lòng cấu hình ít nhất một model")
	}

	candidate := bootstrap.CloneConfig(h.cfg)
	pc := candidate.Providers[draft.Provider]
	oldModels := modelConfigurations(candidate, draft.Provider, pc)
	pc.Type = draft.Type
	pc.API = draft.API
	pc.BaseURL = draft.BaseURL
	configuredModels := make([]bootstrap.ModelConfig, 0, len(draft.Models))
	seen := make(map[string]bool, len(draft.Models))
	for _, model := range draft.Models {
		model.Name = strings.TrimSpace(model.Name)
		if model.Name == "" {
			return preparedProviderDraft{}, fmt.Errorf("tên model không được để trống")
		}
		if model.ContextWindow < 0 {
			return preparedProviderDraft{}, fmt.Errorf("cửa sổ ngữ cảnh của model %q không được âm", model.Name)
		}
		if seen[model.Name] {
			return preparedProviderDraft{}, fmt.Errorf("model %q bị trùng", model.Name)
		}
		seen[model.Name] = true
		configuredModels = append(configuredModels, model)
	}
	pc.Models = configuredModels

	switch draft.APIKeyAction {
	case "", APIKeyKeep:
		// Giữ nguyên giá trị hiện có trong cấu hình ứng viên; khi thêm provider mới đương nhiên là rỗng.
	case APIKeyReplace:
		pc.APIKey = draft.APIKey
	case APIKeyClear:
		pc.APIKey = ""
	default:
		return preparedProviderDraft{}, fmt.Errorf("thao tác API Key không rõ %q", draft.APIKeyAction)
	}
	if pc.RequiresAPIKey(draft.Provider) && pc.APIKey == "" {
		return preparedProviderDraft{}, fmt.Errorf("Provider %q phải cấu hình API Key", draft.Provider)
	}

	if candidate.Providers == nil {
		candidate.Providers = make(map[string]bootstrap.ProviderConfig)
	}
	candidate.Providers[draft.Provider] = pc
	return preparedProviderDraft{draft: draft, candidate: candidate, provider: pc, oldModels: oldModels}, nil
}

// ConfigureModels kiểm tra, persist và áp dụng nóng thư viện model của một provider.
func (h *Host) ConfigureModels(draft ModelConfigurationDraft) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	preparedDraft, err := h.prepareProviderDraftLocked(draft)
	if err != nil {
		return err
	}
	draft = preparedDraft.draft
	candidate := preparedDraft.candidate
	pc := preparedDraft.provider
	renames, err := validateModelRenames(draft.Renames, preparedDraft.oldModels, pc.Models)
	if err != nil {
		return err
	}
	renameModelReferences(&candidate, draft.Provider, renames)

	newNames := make(map[string]bool, len(pc.Models))
	for _, model := range pc.Models {
		newNames[model.Name] = true
	}
	// Trước khi xóa model phải kiểm tra tham chiếu: model bị mặc định tầng trên hoặc bất kỳ role/fallback
	// trỏ tới không được xóa, để người dùng sang /model chuyển trước — /config không còn chuyển thay mặc định.
	for _, old := range preparedDraft.oldModels {
		if newNames[old.Name] {
			continue
		}
		if _, renamed := renames[old.Name]; renamed {
			continue
		}
		if refs := h.modelReferencesLocked(draft.Provider, old.Name); len(refs) > 0 {
			return fmt.Errorf("model %q vẫn được %s tham chiếu, vui lòng chuyển ở /model trước khi xóa", old.Name, strings.Join(refs, ", "))
		}
	}

	// Chỉnh sửa thường không đổi "hiện đang dùng cái nào"; đổi tên tường minh chỉ chuyển đổi danh tính tham chiếu của cùng một model.
	if err := candidate.ValidateBase(); err != nil {
		return err
	}
	prepared, err := bootstrap.NewModelSet(candidate)
	if err != nil {
		return fmt.Errorf("tạo client model thất bại: %w", err)
	}

	if h.configPath == "" {
		return fmt.Errorf("không định vị được đường dẫn file cấu hình")
	}
	if err := h.saveModelConfigurationLocked(candidate, draft.Provider, pc, len(renames) > 0); err != nil {
		return fmt.Errorf("lưu cấu hình thất bại: %w", err)
	}

	h.models.ApplyPrepared(prepared)
	h.cfg = candidate
	// Sau khi client model được dựng lại thì phát lại mức suy luận: applyThinkingLocked kẹp giá trị hiệu lực
	// theo năng lực model mới của từng role, ý định mức suy luận trong lưu trữ giữ nguyên.
	h.applyThinkingLocked("default")
	summary := fmt.Sprintf("Cấu hình Provider đã lưu: %s → %s", draft.Provider, h.configPath)
	if draft.Provider != h.cfg.Provider {
		summary += "; dùng /model để chuyển"
	}
	h.emitEvent(Event{
		Time: time.Now(), Category: "SYSTEM", Level: "info",
		Summary: summary,
	})
	return nil
}

func validateModelRenames(requested []ModelRename, oldModels, newModels []bootstrap.ModelConfig) (map[string]string, error) {
	oldNames := make(map[string]bool, len(oldModels))
	newNames := make(map[string]bool, len(newModels))
	for _, model := range oldModels {
		oldNames[model.Name] = true
	}
	for _, model := range newModels {
		newNames[model.Name] = true
	}
	renames := make(map[string]string, len(requested))
	targets := make(map[string]bool, len(requested))
	for _, rename := range requested {
		from := strings.TrimSpace(rename.From)
		to := strings.TrimSpace(rename.To)
		if from == "" || to == "" {
			return nil, fmt.Errorf("tên cũ và tên mới khi đổi tên model không được để trống")
		}
		if from == to {
			continue
		}
		if !oldNames[from] {
			return nil, fmt.Errorf("không thể đổi tên model không tồn tại %q", from)
		}
		if !newNames[to] {
			return nil, fmt.Errorf("model đích đổi tên %q không có trong danh sách model hiện tại", to)
		}
		if _, exists := renames[from]; exists {
			return nil, fmt.Errorf("model %q bị đổi tên trùng lặp", from)
		}
		if targets[to] {
			return nil, fmt.Errorf("nhiều model không thể đồng thời đổi tên thành %q", to)
		}
		renames[from] = to
		targets[to] = true
	}
	return renames, nil
}

func renameModelReferences(cfg *bootstrap.Config, provider string, renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	if cfg.Provider == provider {
		if renamed, ok := renames[cfg.ModelName]; ok {
			cfg.ModelName = renamed
		}
	}
	for role, roleConfig := range cfg.Roles {
		changed := false
		if roleConfig.Provider == provider {
			if renamed, ok := renames[roleConfig.Model]; ok {
				roleConfig.Model = renamed
				changed = true
			}
		}
		for i := range roleConfig.Fallbacks {
			fallback := &roleConfig.Fallbacks[i]
			if fallback.Provider != provider {
				continue
			}
			if renamed, ok := renames[fallback.Model]; ok {
				fallback.Model = renamed
				changed = true
			}
		}
		if changed {
			cfg.Roles[role] = roleConfig
		}
	}
}

func (h *Host) saveModelConfigurationLocked(candidate bootstrap.Config, provider string, pc bootstrap.ProviderConfig, renamed bool) error {
	if renamed {
		// Tham chiếu và định nghĩa provider phải ghi đĩa trong cùng một lần thay file, nếu không tiến trình khởi động lại có thể chỉ thấy một nửa.
		// /model cũng dùng SaveConfig ghi lại cấu hình hiệu lực; đổi tên theo cùng ngữ nghĩa đó.
		return bootstrap.SaveConfig(h.configPath, candidate)
	}
	return bootstrap.SaveProviderConfig(h.configPath, provider, pc)
}

// TestModelConnection dựng một client model thật theo bản nháp hiện tại và gửi yêu cầu tối tiểu.
// Nó không lưu cấu hình, không đổi model runtime, và không downgrade sang Provider khác khi thất bại.
func (h *Host) TestModelConnection(ctx context.Context, draft ModelConfigurationDraft, modelName string) error {
	h.mu.Lock()
	preparedDraft, err := h.prepareProviderDraftLocked(draft)
	h.mu.Unlock()
	if err != nil {
		return err
	}

	modelName = strings.TrimSpace(modelName)
	found := false
	for _, model := range preparedDraft.provider.Models {
		if model.Name == modelName {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("model kiểm tra kết nối %q không có trong danh sách model hiện tại", modelName)
	}

	testConfig := preparedDraft.candidate
	testConfig.Provider = preparedDraft.draft.Provider
	testConfig.ModelName = modelName
	testConfig.Roles = nil
	if err := testConfig.ValidateBase(); err != nil {
		return err
	}
	models, err := bootstrap.NewModelSet(testConfig)
	if err != nil {
		return fmt.Errorf("tạo client model kiểm tra thất bại: %w", err)
	}
	if _, err := models.Default.Generate(ctx, []agentcore.Message{agentcore.UserMsg("Reply OK.")}, nil); err != nil {
		return fmt.Errorf("kiểm tra kết nối thất bại (%s/%s): %w", preparedDraft.draft.Provider, modelName, err)
	}
	return nil
}

func (h *Host) modelReferencesLocked(provider, model string) []string {
	var refs []string
	if h.cfg.Provider == provider && h.cfg.ModelName == model {
		refs = append(refs, "default")
	}
	for role, rc := range h.cfg.Roles {
		if rc.Provider == provider && rc.Model == model {
			refs = append(refs, role)
		}
		for i, fallback := range rc.Fallbacks {
			if fallback.Provider == provider && fallback.Model == model {
				refs = append(refs, fmt.Sprintf("%s fallback[%d]", role, i))
			}
		}
	}
	sort.Strings(refs)
	return refs
}
