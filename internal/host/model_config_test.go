package host

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

func newModelConfigTestHost(t *testing.T) (*Host, string) {
	t.Helper()
	pc := bootstrap.ProviderConfig{
		Type: "openai", APIKey: "old-secret", BaseURL: "https://example.com/v1",
		Models: []bootstrap.ModelConfig{{Name: "old", ContextWindow: 128000}, {Name: "writer-model"}},
	}
	cfg := bootstrap.Config{
		Provider: "proxy", ModelName: "old", Providers: map[string]bootstrap.ProviderConfig{"proxy": pc},
		Roles: map[string]bootstrap.RoleConfig{"writer": {
			Provider: "proxy", Model: "writer-model",
			Fallbacks: []bootstrap.ModelRef{{Provider: "proxy", Model: "old"}},
		}},
	}
	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		t.Fatalf("new model set: %v", err)
	}
	// Ghi một cấu hình ban đầu: trong thực tế configPath phải trỏ tới tầng cấu hình đã tồn tại, SaveProviderConfig
	// chỉ bù khối providers, giữ phần còn lại; sau seed mới kiểm chứng thật được "lựa chọn tầng trên không bị thay đổi".
	path := filepath.Join(t.TempDir(), "config.json")
	if err := bootstrap.SaveConfig(path, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return &Host{
		cfg: cfg, models: models, events: make(chan Event, 4),
		configPath: path,
	}, path
}

// Lưu trữ mức suy luận giữ ý định gốc: sau khi đặt tường minh, đổi model không được kẹp hạ rồi ghi ngược.
func TestSetRoleThinkingPreservesIntentAcrossModelSwitch(t *testing.T) {
	h, _ := newModelConfigTestHost(t)
	if err := h.SetRoleThinking("writer", "high"); err != nil {
		t.Fatalf("set thinking: %v", err)
	}
	if got := h.cfg.Roles["writer"].ReasoningEffort; got != "high" {
		t.Fatalf("SetRoleThinking phải lưu nguyên văn high, được %q", got)
	}
	// Đổi model của writer: ý định mức đã lưu phải giữ high, việc kẹp chỉ được xảy ra trên đường phát xuống.
	if err := h.SwitchModel("writer", "proxy", "old"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if got := h.cfg.Roles["writer"].ReasoningEffort; got != "high" {
		t.Fatalf("Sau khi đổi model writer thinking bị ghi thành %q, phải vẫn là high", got)
	}
}

func TestConfigureModelsRejectsDeletingReferencedModel(t *testing.T) {
	h, _ := newModelConfigTestHost(t)
	// Xóa "writer-model" đang được role writer tham chiếu (giữ "old" tầng trên đang dùng) phải bị từ chối.
	err := h.ConfigureModels(ModelConfigurationDraft{
		Provider: "proxy", Type: "openai", BaseURL: "https://example.com/v1",
		Models:       []bootstrap.ModelConfig{{Name: "old"}, {Name: "new"}},
		APIKeyAction: APIKeyKeep,
	})
	if err == nil || !strings.Contains(err.Error(), "writer") {
		t.Fatalf("expected writer reference error, got %v", err)
	}
	provider, model, _ := h.models.CurrentSelection("default")
	if provider != "proxy" || model != "old" {
		t.Fatalf("runtime mutated after failure: %s/%s", provider, model)
	}
}

// /config không còn chuyển thay mặc định: xóa model tầng trên đang dùng phải bị từ chối, để người dùng sang /model chuyển trước.
func TestConfigureModelsRejectsDeletingCurrentModel(t *testing.T) {
	h, _ := newModelConfigTestHost(t)
	err := h.ConfigureModels(ModelConfigurationDraft{
		Provider: "proxy", Type: "openai", BaseURL: "https://example.com/v1",
		Models:       []bootstrap.ModelConfig{{Name: "writer-model"}, {Name: "new"}},
		APIKeyAction: APIKeyKeep,
	})
	if err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("expected default reference error, got %v", err)
	}
}

func TestConfigureModelsPersistsAndHotApplies(t *testing.T) {
	h, path := newModelConfigTestHost(t)
	err := h.ConfigureModels(ModelConfigurationDraft{
		Provider: "proxy", Type: "openai", API: "responses", BaseURL: "https://new.example/v1",
		Models:       []bootstrap.ModelConfig{{Name: "old", ContextWindow: 640000}, {Name: "writer-model"}},
		APIKeyAction: APIKeyKeep,
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	// Lựa chọn tầng trên không bị /config đổi: vẫn là proxy/old.
	provider, model, _ := h.models.CurrentSelection("default")
	if provider != "proxy" || model != "old" {
		t.Fatalf("runtime selection mutated = %s/%s", provider, model)
	}
	// Áp dụng nóng khối provider: cửa sổ của old cập nhật thành 640000.
	if window, source := h.models.ResolveContextWindow("proxy", "old"); window != 640000 || source != bootstrap.CtxWindowModelConfig {
		t.Fatalf("runtime window = %d %s", window, source)
	}
	saved, err := bootstrap.LoadConfigFile(path)
	if err != nil {
		t.Fatalf("load saved: %v", err)
	}
	if saved.Provider != "proxy" || saved.ModelName != "old" || saved.Providers["proxy"].APIKey != "old-secret" {
		t.Fatalf("saved config = %#v", saved)
	}
	if saved.Providers["proxy"].API != "responses" || saved.Providers["proxy"].BaseURL != "https://new.example/v1" {
		t.Fatalf("saved provider not patched = %#v", saved.Providers["proxy"])
	}
	if len(saved.Providers["proxy"].Models) != 2 || saved.Providers["proxy"].Models[0].ContextWindow != 640000 {
		t.Fatalf("saved models = %#v", saved.Providers["proxy"].Models)
	}
}

// Lưu bản nháp TUI không được làm mất ba trạng thái json_schema (regression lock cho
// prepareProviderDraftLocked khứ hồi toàn struct).
func TestConfigureModelsPreservesJSONSchemaTriState(t *testing.T) {
	h, path := newModelConfigTestHost(t)
	tr := true
	err := h.ConfigureModels(ModelConfigurationDraft{
		Provider: "proxy", Type: "openai", BaseURL: "https://example.com/v1",
		Models: []bootstrap.ModelConfig{
			{Name: "old", ContextWindow: 128000, JSONSchema: &tr},
			{Name: "writer-model"},
		},
		APIKeyAction: APIKeyKeep,
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	saved, err := bootstrap.LoadConfigFile(path)
	if err != nil {
		t.Fatalf("load saved: %v", err)
	}
	models := saved.Providers["proxy"].Models
	if len(models) != 2 || models[0].JSONSchema == nil || !*models[0].JSONSchema {
		t.Fatalf("json_schema bị mất: %#v", models)
	}
	if models[1].JSONSchema != nil {
		t.Fatalf("Model chưa cấu hình không được ngụy tạo ba trạng thái: %#v", models[1])
	}
}

func TestConfigureModelsRenamesModelAndReferencesAtomically(t *testing.T) {
	h, path := newModelConfigTestHost(t)
	err := h.ConfigureModels(ModelConfigurationDraft{
		Provider: "proxy", Type: "openai", BaseURL: "https://example.com/v1",
		Models: []bootstrap.ModelConfig{{Name: "renamed", ContextWindow: 256000}, {Name: "writer-renamed"}},
		Renames: []ModelRename{
			{From: "old", To: "renamed"},
			{From: "writer-model", To: "writer-renamed"},
		}, APIKeyAction: APIKeyKeep,
	})
	if err != nil {
		t.Fatalf("rename model: %v", err)
	}
	if h.cfg.ModelName != "renamed" || h.cfg.Roles["writer"].Model != "writer-renamed" ||
		h.cfg.Roles["writer"].Fallbacks[0].Model != "renamed" {
		t.Fatalf("runtime references not migrated: default=%q writer=%#v", h.cfg.ModelName, h.cfg.Roles["writer"])
	}
	provider, model, ok := h.models.CurrentSelection("default")
	if !ok || provider != "proxy" || model != "renamed" {
		t.Fatalf("runtime model set not migrated: %s/%s ok=%v", provider, model, ok)
	}
	saved, err := bootstrap.LoadConfigFile(path)
	if err != nil {
		t.Fatalf("load saved: %v", err)
	}
	if saved.ModelName != "renamed" || saved.Roles["writer"].Model != "writer-renamed" ||
		saved.Roles["writer"].Fallbacks[0].Model != "renamed" {
		t.Fatalf("saved references not migrated: default=%q writer=%#v", saved.ModelName, saved.Roles["writer"])
	}
	if _, ok := saved.Providers["proxy"].ModelConfig("renamed"); !ok {
		t.Fatalf("saved provider missing renamed model: %#v", saved.Providers["proxy"].Models)
	}
}

func TestConfigureModelsDoesNotGuessRenameFromDeleteAndAdd(t *testing.T) {
	h, _ := newModelConfigTestHost(t)
	err := h.ConfigureModels(ModelConfigurationDraft{
		Provider: "proxy", Type: "openai", BaseURL: "https://example.com/v1",
		Models: []bootstrap.ModelConfig{{Name: "renamed"}, {Name: "writer-model"}}, APIKeyAction: APIKeyKeep,
	})
	if err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("Khi không khai báo đổi tên tường minh vẫn phải bảo vệ theo kiểu xóa, got %v", err)
	}
}

func TestModelConfigurationsIncludesReferencedUnlistedModel(t *testing.T) {
	pc := bootstrap.ProviderConfig{Models: []bootstrap.ModelConfig{{Name: "listed"}}}
	cfg := bootstrap.Config{
		Provider: "proxy", ModelName: "listed", Providers: map[string]bootstrap.ProviderConfig{"proxy": pc},
		Roles: map[string]bootstrap.RoleConfig{"writer": {Provider: "proxy", Model: "referenced-only"}},
	}
	models := modelConfigurations(cfg, "proxy", pc)
	if len(models) != 2 || models[0].Name != "listed" || models[1].Name != "referenced-only" {
		t.Fatalf("Giao diện và kiểm tra đổi tên phải dùng chung danh sách model ứng viên đầy đủ, got %#v", models)
	}
}

func TestMaskAPIKeyAndSnapshotNeverExposeFullValue(t *testing.T) {
	if got := MaskAPIKey("  sk-1234567890abcdef  "); got != "sk-1******cdef" {
		t.Fatalf("MaskAPIKey = %q", got)
	}
	if got := MaskAPIKey("short-secret"); got != "******" {
		t.Fatalf("Key ngắn phải ẩn hết, được %q", got)
	}

	h, _ := newModelConfigTestHost(t)
	snapshot := h.ModelConfiguration()
	if len(snapshot.Providers) != 1 {
		t.Fatalf("providers = %#v", snapshot.Providers)
	}
	provider := snapshot.Providers[0]
	if provider.APIKeyHint != "******" || strings.Contains(provider.APIKeyHint, "old-secret") {
		t.Fatalf("Snapshot phơi nguyên API Key: %#v", provider)
	}
}

func TestConfigureModelsRejectsMissingRequiredAPIKeyForUnusedProvider(t *testing.T) {
	h, _ := newModelConfigTestHost(t)
	err := h.ConfigureModels(ModelConfigurationDraft{
		Provider:     "anthropic",
		Models:       []bootstrap.ModelConfig{{Name: "claude-test"}},
		APIKeyAction: APIKeyKeep,
	})
	if err == nil || !strings.Contains(err.Error(), "phải cấu hình API Key") {
		t.Fatalf("Provider chưa dùng nhưng yêu cầu chứng nhiệm cũng phải từ chối Key rỗng, được %v", err)
	}
	if _, exists := h.cfg.Providers["anthropic"]; exists {
		t.Fatal("Sau khi kiểm tra thất bại không được sửa cấu hình runtime")
	}
}

func TestModelConnectionUsesDraftWithoutSaving(t *testing.T) {
	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"test","object":"chat.completion","created":1,"model":"old",
			"choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}
		}`))
	}))
	defer server.Close()

	h, path := newModelConfigTestHost(t)
	originalURL := h.cfg.Providers["proxy"].BaseURL
	err := h.TestModelConnection(context.Background(), ModelConfigurationDraft{
		Provider: "proxy", Type: "openai", BaseURL: server.URL + "/v1",
		Models: []bootstrap.ModelConfig{{Name: "old"}}, APIKeyAction: APIKeyKeep,
	}, "old")
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if requestPath != "/v1/chat/completions" {
		t.Fatalf("request path = %q", requestPath)
	}
	if got := h.cfg.Providers["proxy"].BaseURL; got != originalURL {
		t.Fatalf("Kiểm tra kết nối đã sửa cấu hình runtime: %q", got)
	}
	saved, err := bootstrap.LoadConfigFile(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if got := saved.Providers["proxy"].BaseURL; got != originalURL {
		t.Fatalf("Kiểm tra kết nối đã ghi vào file cấu hình: %q", got)
	}
}

func TestConfigureModelsSuggestsSwitchForNewProvider(t *testing.T) {
	h, _ := newModelConfigTestHost(t)
	err := h.ConfigureModels(ModelConfigurationDraft{
		Provider: "backup", Type: "openai", BaseURL: "https://backup.example/v1",
		Models: []bootstrap.ModelConfig{{Name: "backup-model"}}, APIKeyAction: APIKeyKeep,
	})
	if err != nil {
		t.Fatalf("configure backup: %v", err)
	}
	event := <-h.events
	if !strings.Contains(event.Summary, "dùng /model để chuyển") {
		t.Fatalf("Sau khi thêm Provider không phải cái hiện tại phải nhắc chuyển, event=%q", event.Summary)
	}
}
