package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
)

func hubFieldIDs(fields []hubField) []string {
	ids := make([]string, len(fields))
	for i, f := range fields {
		ids[i] = f.id
	}
	return ids
}

func hubFieldIndex(fields []hubField, id string) int {
	for i, field := range fields {
		if field.id == id {
			return i
		}
	}
	return -1
}

// Chọn Provider sẵn có phải vào hub chi tiết (xem thông tin trước, rồi mới chỉnh từng
// mục), chứ không nhảy thẳng vào "đổi giao thức".
func TestSelectingProviderOpensHub(t *testing.T) {
	st := &modelConfigState{editModelIdx: -1}
	st.applyProviderChoice(configProviderChoice{existing: &host.ProviderSnapshot{
		Name: "openrouter", BaseURL: "u", HasAPIKey: true,
		Models: []bootstrap.ModelConfig{{Name: "m"}},
	}})
	if st.step != configStepHub {
		t.Fatalf("chọn Provider sẵn có phải vào hub, nhận step=%d", st.step)
	}
	ids := hubFieldIDs(st.hubFields())
	// Provider builtin (type rỗng) không trải giao thức/Endpoint ồn ào, nhưng giữ
	// key/models/save.
	if slices.Contains(ids, "protocol") || slices.Contains(ids, "api") {
		t.Fatalf("hub provider builtin không được xuất hiện giao thức/Endpoint, nhận %v", ids)
	}
	for _, want := range []string{"key", "baseurl", "models", "save"} {
		if !slices.Contains(ids, want) {
			t.Fatalf("hub thiếu %q, nhận %v", want, ids)
		}
	}
}

// Hub của Provider tùy biến (giao thức openai tường minh) mới hiển thị giao thức và Endpoint.
func TestCustomProviderHubShowsProtocolAndEndpoint(t *testing.T) {
	st := &modelConfigState{editModelIdx: -1}
	st.applyProviderChoice(configProviderChoice{existing: &host.ProviderSnapshot{
		Name: "proxy", Type: "openai", API: "responses", HasAPIKey: true,
		Models: []bootstrap.ModelConfig{{Name: "m"}},
	}})
	ids := hubFieldIDs(st.hubFields())
	if !slices.Contains(ids, "protocol") || !slices.Contains(ids, "api") {
		t.Fatalf("hub provider openai tùy biến phải chứa giao thức/Endpoint, nhận %v", ids)
	}
}

// Esc quay về từng cấp: hub chỉnh trong dòng → hub → danh sách Provider → đóng.
func TestEscapeBackHierarchy(t *testing.T) {
	st := &modelConfigState{step: configStepHub, provider: "proxy"}
	st.beginInlineEdit("baseurl")
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEsc})
	if st.step != configStepHub || st.editingField != "" {
		t.Fatalf("Esc khi chỉnh trong dòng phải ở lại hub và hủy nhập, nhận step=%d field=%q", st.step, st.editingField)
	}
	if got, ok := st.escapeBack(); !ok || got != configStepProvider {
		t.Fatalf("Esc từ hub phải về danh sách, nhận %d,%v", got, ok)
	}
	st.step = configStepProvider
	if _, ok := st.escapeBack(); ok {
		t.Fatal("Esc từ danh sách phải đóng cả bảng")
	}
}

func TestModelListAddsAndEditsInPlace(t *testing.T) {
	st := &modelConfigState{step: configStepModels, editModelIdx: -1,
		models: []bootstrap.ModelConfig{{Name: "m1"}}, modelOrigins: []string{"m1"}}
	st.cursor = len(st.models)
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if st.step != configStepModels || st.editingField != configModelNameField || !st.addingModel {
		t.Fatalf("thêm mới phải ở lại danh sách chỉnh trong dòng, step=%d field=%q adding=%v", st.step, st.editingField, st.addingModel)
	}
	st.input.SetValue("  m2  ")
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if st.editingField != configModelWindowField || st.models[1].Name != "m2" {
		t.Fatalf("sau khi nộp tên phải vào cột cửa sổ trên cùng dòng, field=%q models=%#v", st.editingField, st.models)
	}
	st.input.SetValue("128K")
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if st.editingField != "" || st.addingModel || st.models[1].ContextWindow != 128000 {
		t.Fatalf("model mới thêm chưa hoàn tất trên cùng trang: %#v", st)
	}
}

func TestModelListEditsSelectedCellAndCancels(t *testing.T) {
	st := &modelConfigState{step: configStepModels, editModelIdx: -1,
		models: []bootstrap.ModelConfig{{Name: "m1", ContextWindow: 1000}}, modelOrigins: []string{"m1"}}
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyRight})
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if st.editingField != configModelWindowField || st.step != configStepModels {
		t.Fatalf("Enter cột phải phải chỉnh cửa sổ trong dòng, step=%d field=%q", st.step, st.editingField)
	}
	st.input.SetValue("200K")
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEsc})
	if st.editingField != "" || st.models[0].ContextWindow != 1000 {
		t.Fatalf("Esc phải hủy ô hiện tại và không đổi giá trị: %#v", st.models[0])
	}
}

func TestModelRenameProducesExplicitDraftAndReferenceNotice(t *testing.T) {
	st := &modelConfigState{
		step: configStepModels, provider: "proxy", models: []bootstrap.ModelConfig{{Name: "old"}},
		modelOrigins: []string{"old"}, snapshot: host.ModelConfigurationSnapshot{
			References: map[string][]string{"proxy\x00old": {"default", "writer fallback[0]"}},
		},
	}
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	st.input.SetValue("renamed")
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	draft := st.draft()
	if len(draft.Renames) != 1 || draft.Renames[0] != (host.ModelRename{From: "old", To: "renamed"}) {
		t.Fatalf("đổi tên model phải giữ quan hệ danh tính tường minh, renames=%#v", draft.Renames)
	}
	if !strings.Contains(st.message, "đồng bộ") || !strings.Contains(st.message, "default") {
		t.Fatalf("đổi tên model đang được tham chiếu phải nêu rõ hành vi lưu, message=%q", st.message)
	}
}

func TestModelListRendersEditableColumnsAndReferences(t *testing.T) {
	st := &modelConfigState{
		step: configStepModels, provider: "proxy", models: []bootstrap.ModelConfig{{Name: "deepseek-chat", ContextWindow: 128000}},
		modelOrigins: []string{"deepseek-chat"}, snapshot: host.ModelConfigurationSnapshot{
			References: map[string][]string{"proxy\x00deepseek-chat": {"default"}},
		},
	}
	plain := ansi.Strip(renderModelConfigModal(120, st))
	for _, want := range []string{"Tên/ID Model", "Cửa sổ ngữ cảnh", "Vai trò gán", "deepseek-chat", "128K", "default", "+ Thêm Model mới"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("bảng model một trang thiếu %q:\n%s", want, plain)
		}
	}
}

func TestModelNameInlineEditorKeepsModalRowsIntact(t *testing.T) {
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })
	st := &modelConfigState{
		step: configStepModels, provider: "deepseek",
		models:       []bootstrap.ModelConfig{{Name: "deepseek-v4-pro"}, {Name: "deepseek-v4-flash"}},
		modelOrigins: []string{"deepseek-v4-pro", "deepseek-v4-flash"},
	}
	st.beginModelEdit(0, 0)
	view := renderModelConfigModal(120, st)
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if width := lipgloss.Width(line); width != 72 {
			t.Fatalf("chỉnh tên model trong dòng làm hỏng độ rộng dòng %d: width=%d line=%q\n%s", i, width, ansi.Strip(line), ansi.Strip(view))
		}
	}
	if len(lines) != 7 {
		t.Fatalf("chỉnh trong dòng không được đưa vào xuống dòng vật lý, nhận %d dòng:\n%s", len(lines), ansi.Strip(view))
	}
}

func TestRenamedReferencedModelStillCannotBeDeleted(t *testing.T) {
	st := &modelConfigState{
		provider: "proxy", currentModel: "old", models: []bootstrap.ModelConfig{{Name: "renamed"}},
		modelOrigins: []string{"old"}, snapshot: host.ModelConfigurationSnapshot{
			References: map[string][]string{"proxy\x00old": {"default"}},
		},
	}
	if st.deleteModel(0) || len(st.models) != 1 || !strings.Contains(st.message, "đang được sử dụng") {
		t.Fatalf("khi đổi tên chưa lưu vẫn phải bảo vệ xóa theo danh tính gốc, models=%#v message=%q", st.models, st.message)
	}
}

func TestCancellingNewModelNameRemovesTemporaryRow(t *testing.T) {
	st := &modelConfigState{step: configStepModels, models: []bootstrap.ModelConfig{{Name: "m1"}}, modelOrigins: []string{"m1"}}
	st.cursor = 1
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEsc})
	if len(st.models) != 1 || len(st.modelOrigins) != 1 || st.cursor != 1 {
		t.Fatalf("hủy thêm mới phải dọn dòng tạm, models=%#v origins=%#v cursor=%d", st.models, st.modelOrigins, st.cursor)
	}
}

func TestParseContextWindowInput(t *testing.T) {
	cases := map[string]int{
		"": 0, "0": 0, "auto": 0, "128K": 128000, "1M": 1000000,
		"1.5m": 1500000, "200000": 200000,
	}
	for input, want := range cases {
		got, err := parseContextWindowInput(input)
		if err != nil || got != want {
			t.Errorf("parseContextWindowInput(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"-1", "abc", "0.5"} {
		if _, err := parseContextWindowInput(input); err == nil {
			t.Errorf("parseContextWindowInput(%q) should fail", input)
		}
	}
}

func TestModelConfigModalDoesNotRenderAPIKey(t *testing.T) {
	state := &modelConfigState{step: configStepHub, provider: "proxy", apiKeyOptional: true}
	state.beginInlineEdit("key")
	state.input.SetValue("sk-super-secret")
	view := renderModelConfigModal(120, state)
	if strings.Contains(view, "sk-super-secret") {
		t.Fatal("API key leaked into rendered modal")
	}
}

func TestProviderHubEditsAPIKeyInlineAndTrims(t *testing.T) {
	state := &modelConfigState{step: configStepHub, provider: "proxy", existing: true,
		hasAPIKey: true, apiKeyHint: "sk-o******7890", apiKeyOptional: true, apiKeyAction: host.APIKeyKeep}
	state.cursor = hubFieldIndex(state.hubFields(), "key")
	m := Model{modelConfig: state}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if state.step != configStepHub || state.editingField != "key" {
		t.Fatalf("API Key phải chỉnh tại dòng gốc của hub, nhận step=%d field=%q", state.step, state.editingField)
	}
	state.input.SetValue("  sk-new-secret-1234567890  ")
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if state.editingField != "" || state.apiKey != "sk-new-secret-1234567890" || state.apiKeyAction != host.APIKeyReplace {
		t.Fatalf("kết quả nộp API Key trong dòng sai: field=%q key=%q action=%q", state.editingField, state.apiKey, state.apiKeyAction)
	}
	if got := state.keyStatus(); got != "sk-n******7890" {
		t.Fatalf("API Key mới phải hiển thị gợi ý đã khử nhạy, nhận %q", got)
	}
}

func TestProviderHubEditsBaseURLInlineAndKeepsLongTailVisible(t *testing.T) {
	state := &modelConfigState{step: configStepHub, provider: "proxy", existing: true,
		apiKeyOptional: true, baseURL: "https://old.example/v1"}
	state.cursor = hubFieldIndex(state.hubFields(), "baseurl")
	m := Model{modelConfig: state}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if state.editingField != "baseurl" || state.input.Value() != "https://old.example/v1" {
		t.Fatalf("Base URL phải được điền sẵn chỉnh tại dòng gốc, field=%q value=%q", state.editingField, state.input.Value())
	}
	state.input.SetValue("  https://example.com/a/very/long/provider/path/UNIQUE-END  ")
	state.input.CursorEnd()
	view := renderModelConfigModal(76, state)
	if !strings.Contains(view, "UNIQUE-END") {
		t.Fatalf("khi chỉnh Base URL dài phải hiển thị phần đuôi gần con trỏ:\n%s", view)
	}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if state.baseURL != "https://example.com/a/very/long/provider/path/UNIQUE-END" {
		t.Fatalf("Base URL không được TrimSpace, nhận %q", state.baseURL)
	}
}

func TestSaveConfigHighlightsOnlyWhenDirty(t *testing.T) {
	state := &modelConfigState{editModelIdx: -1}
	state.applyProviderChoice(configProviderChoice{existing: &host.ProviderSnapshot{
		Name: "proxy", Type: "openai", BaseURL: "https://old.example/v1", HasAPIKey: true,
		APIKeyHint: "sk-o******7890", Models: []bootstrap.ModelConfig{{Name: "m1"}},
	}})
	if state.isDirty() {
		t.Fatal("vừa vào Provider sẵn có không được đánh dấu đã sửa")
	}
	state.baseURL = "https://new.example/v1"
	if !state.isDirty() {
		t.Fatal("sau khi Base URL đổi phải đánh dấu đã sửa")
	}
	state.baseURL = "https://old.example/v1"
	if state.isDirty() {
		t.Fatal("sau khi đổi về giá trị baseline phải tự phục hồi trạng thái chưa sửa")
	}
	state.beginInlineEdit("baseurl")
	state.input.SetValue("https://editing.example/v1")
	if !state.isDirty() {
		t.Fatal("khi đang nhập giá trị Base URL mới phải đánh dấu đã sửa thời gian thực")
	}
	state.input.SetValue(" https://old.example/v1 ")
	if state.isDirty() {
		t.Fatal("khi nhập trong dòng tương đương giá trị baseline không được báo sai đã sửa")
	}
	state.editingField = ""
	state.apiKeyAction = host.APIKeyReplace
	state.apiKey = "sk-new-secret"
	if !state.isDirty() {
		t.Fatal("sau khi thay API Key phải đánh dấu đã sửa")
	}

	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile) })
	lines := renderProviderHubFields(state, 68)
	want := lipgloss.NewStyle().Foreground(colorSuccess).Render("Lưu cấu hình")
	found := false
	for _, line := range lines {
		if strings.Contains(line, want) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("khi có thay đổi mục lưu phải dùng màu thành công, lines=%q", lines)
	}

	newProvider := &modelConfigState{}
	if !newProvider.isDirty() {
		t.Fatal("Provider mới thêm luôn phải coi là thay đổi chưa lưu")
	}
}

func TestStyledBaseURLLineKeepsANSIAndFillsModalWidth(t *testing.T) {
	plain := "› Base URL  https://api.deepseek.com"
	styled := "\x1b[38;2;255;200;0m› \x1b[0m" +
		"\x1b[1;38;2;255;200;0mBase URL\x1b[0m  " +
		"\x1b[4;38;2;220;220;220mhttps://api.deepseek.com\x1b[0m"
	if got := ansi.Strip(truncateStyledWidth(styled, 56)); got != plain {
		t.Fatalf("cắt độ rộng nhận thức ANSI làm hỏng dòng nhập: %q", got)
	}

	modal := renderPaddedModalFrame(60, 3, "/config", "", []string{styled})
	lines := strings.Split(modal, "\n")
	if len(lines) != 3 || lipgloss.Width(lines[1]) != 60 {
		t.Fatalf("dòng nhập lớp phủ không lấp đầy độ rộng cố định: width=%d\n%s", lipgloss.Width(lines[1]), modal)
	}
	if !strings.Contains(ansi.Strip(lines[1]), "https://api.deepseek.com") {
		t.Fatalf("lớp phủ làm mất Base URL:\n%s", modal)
	}
}

func TestProviderHubDeleteClearsOnlyOptionalAPIKey(t *testing.T) {
	optional := &modelConfigState{step: configStepHub, provider: "proxy", providerType: "openai",
		hasAPIKey: true, apiKeyHint: "sk-o******7890", apiKeyOptional: true, apiKeyAction: host.APIKeyKeep}
	optional.cursor = hubFieldIndex(optional.hubFields(), "key")
	m := Model{modelConfig: optional}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyDelete})
	if optional.apiKeyAction != host.APIKeyClear || optional.keyStatus() != "Đã xóa" {
		t.Fatalf("Delete của Key tùy chọn phải đánh dấu xóa, action=%q status=%q", optional.apiKeyAction, optional.keyStatus())
	}

	required := &modelConfigState{step: configStepHub, provider: "openrouter",
		hasAPIKey: true, apiKeyHint: "sk-o******7890", apiKeyOptional: false, apiKeyAction: host.APIKeyKeep}
	required.cursor = hubFieldIndex(required.hubFields(), "key")
	m = Model{modelConfig: required}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyDelete})
	if required.apiKeyAction != host.APIKeyKeep || !strings.Contains(required.message, "không thể xóa") {
		t.Fatalf("Key bắt buộc không được xóa, action=%q message=%q", required.apiKeyAction, required.message)
	}
}

func TestConfigTextInputSupportsCursorEditing(t *testing.T) {
	state := &modelConfigState{step: configStepCustomName}
	state.startTextInput("ac", "Tên Provider", false)
	state.input.SetCursor(1)
	m := Model{modelConfig: state}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if got := state.input.Value(); got != "abc" {
		t.Fatalf("ô nhập thống nhất phải hỗ trợ chèn tại vị trí con trỏ, nhận %q", got)
	}
}

func TestProviderHubShowsConfigPathAndConnectionAction(t *testing.T) {
	state := &modelConfigState{
		step: configStepHub, provider: "proxy", apiKeyOptional: true, currentModel: "m2",
		models:   []bootstrap.ModelConfig{{Name: "m1"}, {Name: "m2"}},
		snapshot: host.ModelConfigurationSnapshot{ConfigPath: `C:\work\.ainovel\config.json`},
	}
	fields := state.hubFields()
	idx := hubFieldIndex(fields, "test")
	if idx < 0 || fields[idx].value != "m2" {
		t.Fatalf("kiểm tra kết nối phải ưu tiên model hiện tại, fields=%#v", fields)
	}
	view := renderModelConfigModal(120, state)
	for _, want := range []string{"Cấu hình nâng cao", "extra_body"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Hub cấu hình thiếu %q:\n%s", want, view)
		}
	}
	compact := strings.NewReplacer("\r", "", "\n", "", " ", "", "│", "").Replace(view)
	if !strings.Contains(compact, `C:\work\.ainovel\config.json`) {
		t.Fatalf("Hub cấu hình chưa hiển thị đầy đủ đường dẫn cấu hình:\n%s", view)
	}
}

func TestModelConfigMessageWrapKeepsErrorTail(t *testing.T) {
	state := &modelConfigState{step: configStepHub, provider: "proxy", apiKeyOptional: true,
		message: "连接失败：" + strings.Repeat("上游返回了很长的错误信息", 8) + " UNIQUE-ERROR-TAIL"}
	view := renderModelConfigModal(64, state)
	compact := strings.NewReplacer("\r", "", "\n", "", " ", "", "│", "").Replace(view)
	if !strings.Contains(compact, "UNIQUE-ERROR-TAIL") {
		t.Fatalf("lỗi dài không được cắt cụt phần đuôi:\n%s", view)
	}
}

func TestConnectionActionStartsAsyncTestWithoutLeavingHub(t *testing.T) {
	state := &modelConfigState{step: configStepHub, provider: "proxy", providerType: "openai",
		apiKeyOptional: true, models: []bootstrap.ModelConfig{{Name: "m1"}}}
	state.cursor = hubFieldIndex(state.hubFields(), "test")
	m := Model{modelConfig: state}
	_, cmd := m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !state.testing || state.step != configStepHub {
		t.Fatalf("kiểm tra kết nối phải chạy bất đồng bộ ở lại hub, cmd=%v testing=%v step=%d", cmd != nil, state.testing, state.step)
	}
}

func TestConnectionTestCanBeCancelled(t *testing.T) {
	cancelled := false
	state := &modelConfigState{step: configStepHub, provider: "proxy", testing: true,
		testCancel: func() { cancelled = true }}
	m := Model{modelConfig: state}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !cancelled || !state.testing || state.message != "Đang hủy kiểm tra kết nối..." {
		t.Fatalf("Esc phải hủy kiểm tra đang chạy và chờ kết quả, cancelled=%v testing=%v message=%q", cancelled, state.testing, state.message)
	}

	updated, _, handled := m.handleRuntimeMsg(modelConfigConnectionMsg{err: context.Canceled})
	m = updated.(Model)
	if !handled || m.modelConfig.testing || m.modelConfig.message != "Kiểm tra kết nối đã bị hủy" {
		t.Fatalf("kết quả hủy chưa hội tụ đúng: handled=%v testing=%v message=%q", handled, m.modelConfig.testing, m.modelConfig.message)
	}
}

func TestConfigCommandIsRegistered(t *testing.T) {
	spec, ok := commandRegistryInstance().Find("config")
	if !ok {
		t.Fatal("/config is not registered")
	}
	if spec.Usage != "/config" || !spec.AutoExecute {
		t.Fatalf("config spec = %#v", spec)
	}
}

func TestModelSwitchLabelIncludesContextWindow(t *testing.T) {
	state := modelSwitchState{models: []host.ConfiguredModel{{Name: "gpt-test", ContextWindow: 400000}}}
	if got := state.modelLabel(); got != "gpt-test · 400K" {
		t.Fatalf("modelLabel = %q", got)
	}
}

// Giống /model: /config render thành lớp phủ có viền cao theo nội dung (không còn
// trải thành lớp phủ giữa màn chiếm 3/4 màn hình).
func TestModelConfigModalIsCompactOverlay(t *testing.T) {
	state := &modelConfigState{step: configStepProvider, providerChoices: []configProviderChoice{
		{label: "chỉnh sửa openrouter", existing: &host.ProviderSnapshot{Name: "openrouter"}},
		{label: "+ Thêm Provider mới…", add: true},
	}}
	lines := strings.Split(renderModelConfigModal(120, state), "\n")

	// 1 dòng tiêu đề + 2 tùy chọn + viền trên dưới = 5 dòng; chiều cao theo nội dung,
	// không phình ra vì chiều cao màn hình.
	if len(lines) != 5 {
		t.Fatalf("lớp phủ gọn phải là 5 dòng (cao theo nội dung), nhận %d dòng:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "┌") || !strings.Contains(lines[0], "/config") {
		t.Fatalf("dòng đầu phải là viền trên mang tiêu đề /config, nhận %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "└") {
		t.Fatalf("dòng cuối phải là viền dưới, nhận %q", lines[len(lines)-1])
	}
}

// Menu cấp một chỉ liệt kê "chỉnh sửa sẵn có + lối vào thêm mới", không trải ra cả
// thư mục Provider builtin; thư mục chỉ xuất hiện ở menu cấp hai.
func TestProviderMenuIsTwoLevel(t *testing.T) {
	state := &modelConfigState{snapshot: host.ModelConfigurationSnapshot{
		Providers:       []host.ProviderSnapshot{{Name: "openrouter"}, {Name: "anthropic"}},
		DefaultProvider: "openrouter",
	}}
	state.buildProviderMenus()

	// Cấp một = 2 chỉnh sửa + 1 lối vào thêm mới; mục cuối là "thêm mới", và không có
	// add/preset nào khác lẫn vào.
	if len(state.providerChoices) != 3 {
		t.Fatalf("menu cấp một phải là 2 chỉnh sửa + 1 thêm mới, nhận %d mục", len(state.providerChoices))
	}
	if !state.providerChoices[len(state.providerChoices)-1].add {
		t.Fatal("mục cuối menu cấp một phải là lối vào \"Thêm Provider mới…\"")
	}
	for i, c := range state.providerChoices[:2] {
		if c.existing == nil || c.add {
			t.Fatalf("mục thứ %d menu cấp một phải là chỉnh sửa Provider sẵn có, nhận %#v", i, c)
		}
	}

	// Cấp hai = thư mục có thể thêm mới: không rỗng, và các mục builtin đã cấu hình
	// (openrouter/anthropic) không xuất hiện lặp.
	if len(state.presetChoices) == 0 {
		t.Fatal("menu cấp hai phải liệt kê thư mục Provider có thể thêm mới")
	}
	if len(state.presetChoices) >= len(bootstrap.ProviderPresets()) {
		t.Fatalf("Provider builtin đã cấu hình phải bị loại khỏi thư mục thêm mới, presets=%d toàn bộ=%d",
			len(state.presetChoices), len(bootstrap.ProviderPresets()))
	}
	for _, c := range state.presetChoices {
		if c.preset != nil && (c.preset.Name == "openrouter" || c.preset.Name == "anthropic") {
			t.Fatalf("thư mục thêm mới không được chứa %q đã cấu hình", c.preset.Name)
		}
	}
}

// T3: hub /config phải có mục Thể loại hiển thị nhãn tiếng Việt từ registry (không lộ
// key thô "wuxia"); key lạ của config cũ fallback hiển thị key để không che giấu cấu
// hình; rỗng hiển thị "Chưa đặt".
func TestHubShowsStyleFieldWithRegistryLabel(t *testing.T) {
	st := &modelConfigState{editModelIdx: -1, style: "wuxia"}
	fields := st.hubFields()
	idx := hubFieldIndex(fields, "style")
	if idx < 0 {
		t.Fatalf("hub phải có mục style, nhận %v", hubFieldIDs(fields))
	}
	if fields[idx].label != "Thể loại" || fields[idx].value != "Võ hiệp / Tu tiên" {
		t.Fatalf("mục style sai nhãn/giá trị: %#v", fields[idx])
	}

	st.style = "co-truyen-xua" // key lạ: fallback key thô
	if got := st.hubFields()[idx].value; got != "co-truyen-xua" {
		t.Fatalf("key lạ phải hiển thị key thô, nhận %q", got)
	}

	st.style = ""
	if got := st.hubFields()[idx].value; got != "Chưa đặt" {
		t.Fatalf("style rỗng phải hiển thị Chưa đặt, nhận %q", got)
	}
}

// Enter vào Thể loại mở màn chọn theo registry với con trỏ đứng tại style hiện có;
// Esc quay về hub — cùng mô hình màn chọn giao thức/endpoint.
func TestStyleFieldOpensSelectAtCurrentStyle(t *testing.T) {
	st := &modelConfigState{step: configStepHub, provider: "proxy", editModelIdx: -1, style: "wuxia"}
	st.cursor = hubFieldIndex(st.hubFields(), "style")
	m := Model{modelConfig: st}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if st.step != configStepStyle || st.cursor != styleOptionIndex("wuxia") {
		t.Fatalf("Enter phải mở màn chọn style với con trỏ tại lựa chọn hiện có, step=%d cursor=%d", st.step, st.cursor)
	}
	plain := ansi.Strip(renderModelConfigModal(120, st))
	for _, want := range []string{"Chọn thể loại", "Võ hiệp / Tu tiên", "Ngôn tình", "Chung (không đặc thù)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("màn chọn style thiếu %q:\n%s", want, plain)
		}
	}
	m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEsc})
	if st.step != configStepHub {
		t.Fatalf("Esc từ màn chọn style phải quay về hub, nhận step=%d", st.step)
	}
}

// Xác nhận chọn style: cập nhật state, quay về hub đúng dòng Thể loại, phát lệnh lưu
// bất đồng bộ theo mẫu lưu provider (không chặn TUI trên I/O file); kết quả lưu giữ
// modal mở để tiếp tục chỉnh provider, lỗi hiển thị rõ.
func TestConfirmStyleUpdatesStateAndSavesAsync(t *testing.T) {
	st := &modelConfigState{step: configStepStyle, provider: "proxy", editModelIdx: -1, style: "default"}
	st.cursor = styleOptionIndex("wuxia")
	m := Model{modelConfig: st}
	_, cmd := m.handleModelConfigKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || st.style != "wuxia" || st.step != configStepHub || !st.saving {
		t.Fatalf("xác nhận style phải cập nhật state và chạy lệnh lưu: cmd=%v style=%q step=%d saving=%v",
			cmd != nil, st.style, st.step, st.saving)
	}
	if want := styleHubFieldIndex(st.hubFields()); st.cursor != want {
		t.Fatalf("con trỏ phải về đúng dòng Thể loại, cursor=%d want=%d", st.cursor, want)
	}
	if !strings.Contains(st.message, "Đang lưu") {
		t.Fatalf("khi lưu phải có thông báo tiến trình, nhận %q", st.message)
	}

	updated, _, handled := m.handleRuntimeMsg(styleSavedMsg{})
	m = updated.(Model)
	if !handled || m.modelConfig == nil || m.modelConfig.saving {
		t.Fatalf("styleSavedMsg thành công phải hạ cờ saving và giữ modal mở: handled=%v saving=%v",
			handled, m.modelConfig.saving)
	}
	if want := "Đã lưu thể loại: Võ hiệp / Tu tiên"; m.modelConfig.message != want {
		t.Fatalf("thông báo lưu sai, nhận %q", m.modelConfig.message)
	}

	updated, _, _ = m.handleRuntimeMsg(styleSavedMsg{err: errors.New("boom")})
	m = updated.(Model)
	if m.modelConfig == nil || m.modelConfig.message != "Lưu thể loại thất bại: boom" {
		t.Fatalf("thông báo lỗi lưu sai, nhận %#v", m.modelConfig)
	}
}

// Lệnh lưu thực sự ghi style vào file cấu hình hiệu lực (HOME/cwd cô lập) và đọc lại
// được đúng key — tiêu chí nghiệm thu "/config sửa được thể loại".
func TestSaveStyleConfigurationPersistsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir trên Windows đọc USERPROFILE
	t.Chdir(t.TempDir())          // không có ./.ainovel/config.json → ghi về ~/.ainovel/config.json

	msg := saveStyleConfiguration("wuxia")()
	saved, ok := msg.(styleSavedMsg)
	if !ok || saved.err != nil {
		t.Fatalf("lưu style thất bại: %#v", msg)
	}
	cfg, err := bootstrap.LoadConfig()
	if err != nil {
		t.Fatalf("đọc lại cấu hình: %v", err)
	}
	if cfg.Style != "wuxia" {
		t.Fatalf("style ghi xuống file = %q, muốn wuxia", cfg.Style)
	}
}
