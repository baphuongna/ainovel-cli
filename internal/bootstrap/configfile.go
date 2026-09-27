package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const configDirName = ".ainovel"

// DefaultConfigPath trả về đường dẫn cấu hình toàn cục ~/.ainovel/config.json.
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, configDirName, "config.json")
}

// DefaultConfigDir trả về đường dẫn thư mục ~/.ainovel; khi không lấy được thư mục home thì trả về chuỗi rỗng.
// Chỉ dùng cho file đọc/ghi không bắt buộc tồn tại (như cache model), không tự tạo thư mục.
func DefaultConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, configDirName)
}

// configDir trả về đường dẫn ~/.ainovel, tạo nếu chưa tồn tại.
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, configDirName)
	// 0o700: thư mục chứa file thông tin đăng nhập dạng api_key (config.json 0o600),
	// máy nhiều người dùng không nên cho user cục bộ khác vào (review L2).
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	return dir, nil
}

// projectConfigPath trả về đường dẫn tương đối của cấu hình cấp dự án ./.ainovel/config.json.
// dotdir cấp dự án nhân bản toàn cục ~/.ainovel/, tái dùng configDirName; phân giải tương đối cwd.
func projectConfigPath() string {
	return filepath.Join(configDirName, "config.json")
}

// EffectiveConfigPath trả về file cấu hình mà các thay đổi TUI (/config, /model) nên ghi về:
// thư mục dự án có ./.ainovel/config.json thì ghi vào đó — cùng chiều với việc đọc (lớp dự án đè lên toàn cục),
// bảo đảm "sửa đúng bản đang hiệu lực", sửa xong có tác dụng ngay; nếu không thì ghi toàn cục ~/.ainovel/config.json.
// Chỉ sửa cấu hình dự án đã tồn tại, không tự tạo mới (tạo override dự án là hành động người dùng chủ động đặt file).
func EffectiveConfigPath() string {
	rel := projectConfigPath()
	if _, err := os.Stat(rel); err == nil {
		if abs, err := filepath.Abs(rel); err == nil {
			return abs
		}
		return rel
	}
	return DefaultConfigPath()
}

// LoadConfig tải và hợp nhất cấu hình theo thứ tự ưu tiên:
//  1. ~/.ainovel/config.json (toàn cục)
//  2. ./.ainovel/config.json (override cấp dự án)
func LoadConfig() (Config, error) {
	var cfg Config

	// 1. Cấu hình toàn cục. Là nền ưu tiên thấp nhất, file hỏng bị hạ cấp thành cảnh báo thay vì chặn — có thể bị dự án đè;
	//    fail cứng sẽ chặn đứng người dùng có "toàn cục hỏng + cấu hình dự án hợp lệ".
	if p := DefaultConfigPath(); p != "" {
		global, found, err := loadOptionalJSON(p)
		switch {
		case err != nil:
			slog.Warn("phân tích cấu hình toàn cục thất bại, đã bỏ qua (có thể bị cấp dự án đè)", "module", "config", "path", p, "err", err)
		case found:
			cfg = global
		}
	}

	// 2. Override cấp dự án. File hỏng fail loud: cấu hình người dùng chủ động đặt trong thư mục hiện tại,
	//    nuốt im lặng sẽ khiến "cấu hình mà không hiệu lực" không thể truy nguyên nhân (issue #37).
	project, found, err := loadOptionalJSON(projectConfigPath())
	if err != nil {
		return cfg, fmt.Errorf("phân tích cấu hình cấp dự án ./.ainovel/config.json thất bại (hãy kiểm tra cú pháp JSON): %w", err)
	}
	if found {
		project = sanitizeProjectConfig(project)
		cfg = mergeConfig(cfg, project)
	}

	return cfg, nil
}

// projectConfigTrustEnv là công tắc tường minh cho phép trường nhạy cảm ở lớp dự án ("1"/"true").
// Thư mục dự án (workspace tiểu thuyết) thường bị zip chia sẻ/commit git, thuộc ranh giới không tin cậy;
// use case hợp lệ (local proxy override base_url) do người dùng tường minh đặt biến này để cho phép.
const projectConfigTrustEnv = "AINOVEL_TRUST_PROJECT_CONFIG"

func projectConfigTrusted() bool {
	v := strings.TrimSpace(os.Getenv(projectConfigTrustEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// sanitizeProjectConfig làm sạch trường nhạy cảm của lớp dự án (./.ainovel/config.json) trước khi hợp nhất.
// Khi chưa tin tưởng tường minh (AINOVEL_TRUST_PROJECT_CONFIG=1):
//   - notify.command: sự kiện kích hoạt là chạy sh -c/powershell, tương đương RCE, bỏ hết;
//   - providers.*.base_url / api_key: base_url đổi hướng sẽ gửi key toàn cục cùng toàn bộ
//     request (kể cả nội dung tiểu thuyết) tới máy chủ kẻ tấn công, bỏ luôn;
//   - providers.*.extra / extra_body: có thể tiêm trường request tùy ý, bỏ luôn.
//
// Các trường bị bỏ đều để lại slog.Warn, người dùng thấy được lý do "cấu hình mà không hiệu lực".
func sanitizeProjectConfig(project Config) Config {
	if projectConfigTrusted() {
		return project
	}
	if project.Notify.Command != "" {
		slog.Warn("đã bỏ qua notify.command của cấu hình cấp dự án (chống RCE); nếu thật sự cần lệnh cấp dự án hãy đặt "+projectConfigTrustEnv+"=1",
			"module", "config")
		project.Notify.Command = ""
		// command là trường nhạy cảm duy nhất trong khối notify; nếu phần còn lại rỗng hết thì zero cả khối,
		// để mergeConfig giữ nguyên cài đặt notify toàn cục (enabled/events vẫn dùng của toàn cục).
		if project.Notify.Enabled == nil && len(project.Notify.Events) == 0 {
			project.Notify = NotifyConfig{}
		}
	}
	for name, pc := range project.Providers {
		dropped := []string{}
		if pc.BaseURL != "" {
			pc.BaseURL = ""
			dropped = append(dropped, "base_url")
		}
		if pc.APIKey != "" {
			pc.APIKey = ""
			dropped = append(dropped, "api_key")
		}
		if len(pc.Extra) > 0 {
			pc.Extra = nil
			dropped = append(dropped, "extra")
		}
		if len(pc.ExtraBody) > 0 {
			pc.ExtraBody = nil
			dropped = append(dropped, "extra_body")
		}
		if len(dropped) > 0 {
			slog.Warn("đã bỏ qua trường nhạy cảm của provider cấp dự án (chống rò thông tin đăng nhập/tiêm request); kịch bản hợp lệ như local proxy hãy đặt "+projectConfigTrustEnv+"=1",
				"module", "config", "provider", name, "fields", strings.Join(dropped, ","))
			project.Providers[name] = pc
		}
	}
	return project
}

// IsProjectConfigPath kiểm tra đường dẫn đã cho có trỏ tới cấu hình lớp dự án của thư mục làm việc hiện tại
// (./.ainovel/config.json) hay không. Đường dẫn lưu đi qua lớp dự án cần xử lý chống rò rỉ thông tin đăng nhập bổ sung.
func IsProjectConfigPath(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	projAbs, err := filepath.Abs(projectConfigPath())
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	projAbs = filepath.Clean(projAbs)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(abs, projAbs)
	}
	return abs == projAbs
}

// loadOptionalJSON đọc một file cấu hình tùy chọn:
//   - file không tồn tại → (zero, false, nil), bên gọi quyết định dùng mặc định/giá trị tầng trên
//   - file tồn tại nhưng phân tích thất bại → trả về lỗi (không nuốt im lặng nữa — nếu không, cấu hình của người dùng "cấu hình mà không hiệu lực"
//     mà không truy được nguyên nhân, chính là gốc rễ issue #37)
func loadOptionalJSON(path string) (Config, bool, error) {
	cfg, err := loadJSONFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	return cfg, true, nil
}

// LoadConfigFile đọc một file cấu hình JSON đơn, hỗ trợ chú thích dòng //.
// Không hợp nhất gì cả, chỉ trả về cấu hình của chính file đó. File không tồn tại thì trả về lỗi.
func LoadConfigFile(path string) (Config, error) {
	return loadJSONFile(path)
}

// loadJSONFile đọc file cấu hình JSON, hỗ trợ chú thích dòng //.
// File không tồn tại thì trả về lỗi (bên gọi quyết định có bỏ qua không).
func loadJSONFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	cleaned := stripJSONComments(data)
	var cfg Config
	if err := json.Unmarshal(cleaned, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// mergeConfig hợp nhất overlay lên base. Trường khác rỗng sẽ đè, map hợp nhất theo key.
func mergeConfig(base, overlay Config) Config {
	if overlay.Provider != "" {
		base.Provider = overlay.Provider
	}
	if overlay.ModelName != "" {
		base.ModelName = overlay.ModelName
	}
	if overlay.ReasoningEffort != "" {
		base.ReasoningEffort = overlay.ReasoningEffort
	}
	if overlay.Style != "" {
		base.Style = overlay.Style
	}
	if overlay.ContextWindow > 0 {
		base.ContextWindow = overlay.ContextWindow
	}

	// Providers: key của overlay đè lên key trùng tên của base
	if len(overlay.Providers) > 0 {
		if base.Providers == nil {
			base.Providers = make(map[string]ProviderConfig)
		}
		for k, v := range overlay.Providers {
			existing := base.Providers[k]
			if v.Type != "" {
				existing.Type = v.Type
			}
			if v.API != "" {
				existing.API = v.API
			}
			if v.APIKey != "" {
				existing.APIKey = v.APIKey
			}
			if v.BaseURL != "" {
				existing.BaseURL = v.BaseURL
			}
			if len(v.Models) > 0 {
				existing.Models = append([]ModelConfig(nil), v.Models...)
			}
			if len(v.ExtraBody) > 0 {
				existing.ExtraBody = cloneMap(v.ExtraBody)
			}
			if len(v.Extra) > 0 {
				existing.Extra = cloneMap(v.Extra)
			}
			base.Providers[k] = existing
		}
	}

	// Roles: key của overlay đè lên key trùng tên của base
	if len(overlay.Roles) > 0 {
		if base.Roles == nil {
			base.Roles = make(map[string]RoleConfig)
		}
		for k, v := range overlay.Roles {
			existing := base.Roles[k]
			if v.Provider != "" {
				existing.Provider = v.Provider
			}
			if v.Model != "" {
				existing.Model = v.Model
			}
			if len(v.Fallbacks) > 0 {
				existing.Fallbacks = append([]ModelRef(nil), v.Fallbacks...)
			}
			if v.ReasoningEffort != "" {
				existing.ReasoningEffort = v.ReasoningEffort
			}
			base.Roles[k] = existing
		}
	}

	// Budget / Notify: đè cả khối (ngân sách/cảnh báo cấp dự án là tuyên bố chính sách độc lập, không ghép từng trường với toàn cục)
	if overlay.Budget != (BudgetConfig{}) {
		base.Budget = overlay.Budget
	}
	if overlay.Notify.Enabled != nil || overlay.Notify.Command != "" || len(overlay.Notify.Events) > 0 {
		// Hợp nhất từng trường thay vì thay cả khối (review V-2 MINOR6): lớp dự án chỉ đè các trường
		// đưa ra tường minh; command rỗng thì kế thừa toàn cục — nếu không, {enabled:true} của project sẽ
		// làm mất lặng lẻ lệnh notify toàn cục của người dùng (khi chưa tin tưởng, sanitize đã bóc command, ở đây
		// xử lý lớp dự án dạng trusted hoặc enabled/events-only).
		if overlay.Notify.Command != "" {
			base.Notify.Command = overlay.Notify.Command
		}
		if overlay.Notify.Enabled != nil {
			base.Notify.Enabled = overlay.Notify.Enabled
		}
		if len(overlay.Notify.Events) > 0 {
			base.Notify.Events = overlay.Notify.Events
		}
	}

	return base
}

func cloneMap(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// CloneConfig sao chép sâu các map/slice trong cấu hình sẽ bị sửa lúc runtime, tránh cấu hình ứng viên làm bẩn cấu hình hiện tại.
func CloneConfig(cfg Config) Config {
	clone := cfg
	clone.Providers = make(map[string]ProviderConfig, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		pc.Models = append([]ModelConfig(nil), pc.Models...)
		pc.Extra = cloneMap(pc.Extra)
		pc.ExtraBody = cloneMap(pc.ExtraBody)
		clone.Providers[name] = pc
	}
	clone.Roles = make(map[string]RoleConfig, len(cfg.Roles))
	for role, rc := range cfg.Roles {
		rc.Fallbacks = append([]ModelRef(nil), rc.Fallbacks...)
		clone.Roles[role] = rc
	}
	clone.Notify.Events = append([]string(nil), cfg.Notify.Events...)
	return clone
}

// SaveProviderConfig cập nhật dạng patch thông tin đăng nhập và danh sách model của một provider trong lớp cấu hình đích.
// Chỉ đụng đoạn providers, tuyệt đối không chạm lựa chọn provider/model tầng trên — "đang dùng cái nào" thuộc về /model.
// Đích chưa tồn tại thì tạo cấu hình tối thiểu; đích hỏng thì từ chối ghi đè.
func SaveProviderConfig(path string, provider string, pc ProviderConfig) error {
	target, found, err := loadOptionalJSON(path)
	if err != nil {
		return err
	}
	if !found {
		target = Config{}
	}
	if target.Providers == nil {
		target.Providers = make(map[string]ProviderConfig)
	}
	// Lớp dự án không lưu thông tin đăng nhập (review H2c + V-2.1): thư mục thường bị zip chia sẻ/commit,
	// api_key một khi ghi vào là coi như theo tiểu thuyết ra khỏi nhà. Ở đâyvô điều kiệnchuyển thông tin đăng nhập sang lớp toàn cục —
	// không lấy "file dự án đã có key" làm điều kiện cho phép: key đó có thể là placeholder người chia sẻ cài sẵn,
	// lấy nó phán định thuộc sở hữu sẽ khiến key toàn cục thật của người dùng bị "nhận làm của" vào lớp dự án và rò theo thư mục ra ngoài.
	// Chỉ ngoại lệ khi AINOVEL_TRUST_PROJECT_CONFIG=1 (người dùng tường minh tin lớp dự án).
	// Ghi toàn cục thất bại phải khiến lưu thất bại (review V-2 MAJOR2): nuốt lỗi im lặng rồi vẫn strip,
	// sẽ làm mất key mới khỏi cả hai lớp, lần gọi sau người dùng dính 401 mà không truy được nguồn.
	if IsProjectConfigPath(path) && pc.APIKey != "" && !projectConfigTrusted() {
		if err := writeGlobalCredential(provider, pc); err != nil {
			return fmt.Errorf("ghi thông tin đăng nhập về cấu hình toàn cục thất bại (%s chưa lưu, hãy kiểm tra ~/.ainovel có ghi được không): %w", provider, err)
		}
		_pc := pc
		_pc.APIKey = ""
		pc = _pc
		slog.Info("api_key đã lưu vào toàn cục ~/.ainovel/config.json (thư mục dự án không lưu thông tin đăng nhập)",
			"module", "config", "provider", provider)
	}
	target.Providers[provider] = pc
	return SaveConfig(path, target)
}

// writeGlobalCredential ghi thông tin đăng nhập của một provider vào lớp cấu hình toàn cục theo kiểu best-effort.
// Tách thành biến riêng để dễ thay thế khi test (tránh test đụng ~/.ainovel thật).
var writeGlobalCredential = func(provider string, pc ProviderConfig) error {
	path := DefaultConfigPath()
	if path == "" {
		return fmt.Errorf("thư mục home không khả dụng")
	}
	target, found, err := loadOptionalJSON(path)
	if err != nil {
		return err
	}
	if !found {
		target = Config{}
	}
	if target.Providers == nil {
		target.Providers = make(map[string]ProviderConfig)
	}
	target.Providers[provider] = pc
	return SaveConfig(path, target)
}

// stripJSONComments bỏ chú thích dòng // trong JSON, theo dõi trạng thái ngoặc kép để tránh xóa nhầm nội dung chuỗi.
func stripJSONComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false

	for i := 0; i < len(data); i++ {
		b := data[i]

		if escaped {
			out = append(out, b)
			escaped = false
			continue
		}

		if inString {
			out = append(out, b)
			if b == '\\' {
				escaped = true
			} else if b == '"' {
				inString = false
			}
			continue
		}

		// không nằm trong chuỗi
		if b == '"' {
			inString = true
			out = append(out, b)
			continue
		}

		// phát hiện chú thích //
		if b == '/' && i+1 < len(data) && data[i+1] == '/' {
			// nhảy tới cuối dòng
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				out = append(out, '\n')
			}
			continue
		}

		out = append(out, b)
	}

	return out
}

// stripProjectLayerCredentials bóc toàn bộ trường dạng thông tin đăng nhập khỏi cấu hình chuẩn bị ghi vào lớp dự án:
// api_key, cùng extra/extra_body (headers có thể giấu proxy token, review V-2 MINOR1,
// đối xứng với sanitizeProjectConfig phía tải).
// Không còn lấy "file dự án vốn có key" phán định thuộc sở hữu (fix V-2.1): sanitize phía tải đã bóc key
// của lớp dự án, key xuất hiện trong payload chỉ có thể đến từ hợp nhất toàn cục hoặc nhập trong phiên này — tất cả thuộc
// lớp toàn cục, ghi vào thư mục dự án chỉ khiến thông tin đăng nhập rò theo tiểu thuyết ra ngoài.
// Khi người dùng tường minh tin lớp dự án (AINOVEL_TRUST_PROJECT_CONFIG=1) thì giữ nguyên.
func stripProjectLayerCredentials(path string, cfg Config) Config {
	if projectConfigTrusted() {
		return cfg
	}
	stripped := make([]string, 0)
	for name, pc := range cfg.Providers {
		if pc.APIKey == "" && pc.Extra == nil && pc.ExtraBody == nil {
			continue
		}
		pc.APIKey = ""
		pc.Extra = nil
		pc.ExtraBody = nil
		cfg.Providers[name] = pc
		stripped = append(stripped, name)
	}
	if len(stripped) > 0 {
		slog.Warn("đã loại trường thông tin đăng nhập khỏi cấu hình cấp dự án (api_key/extra/extra_body vẫn lưu ở ~/.ainovel/config.json)",
			"module", "config", "providers", strings.Join(stripped, ","))
	}
	return cfg
}

// WriteStartupError ghi thêm lỗi chí mạng lúc khởi động vào ~/.ainovel/last-error.log, và trả về
// đường dẫn file đó (best-effort, thất bại trả về chuỗi rỗng). Khởi động bằng double-click thì cửa sổ console
// đóng ngay khi tiến trình thoát, lỗi hiện lên một cái rồi biến mất, ghi đĩa là con đường duy nhất để kiểu người dùng này truy cứu sau đó.
func WriteStartupError(msg string) string {
	dir := DefaultConfigDir()
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	path := filepath.Join(dir, "last-error.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "[%s] %s\n", time.Now().Format(time.RFC3339), msg); err != nil {
		return ""
	}
	return path
}

// SaveConfig ghi cấu hình vào đường dẫn chỉ định (định dạng JSON, thụt lề đẹp).
// Khi đích là lớp dự án (./.ainovel/config.json), trước tiên bóc api_key không thuộc chính lớp dự án
// khỏi payload (review H2c): key nguồn toàn cục đã tồn tại ở ~/.ainovel/config.json,
// chép sang thư mục dự án chỉ khiến thông tin đăng nhập bị chia sẻ/commit cùng tiểu thuyết.
func SaveConfig(path string, cfg Config) error {
	if IsProjectConfigPath(path) {
		cfg = stripProjectLayerCredentials(path, cfg)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}
