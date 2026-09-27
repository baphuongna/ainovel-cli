package bootstrap

import (
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/notify"
	"github.com/voocel/ainovel-cli/internal/styles"
)

func TestConfigResolveReasoningEffort(t *testing.T) {
	cfg := Config{
		ReasoningEffort: "low", // mặc định cấp cao nhất
		Roles: map[string]RoleConfig{
			"writer":    {Provider: "p", Model: "m", ReasoningEffort: "high"}, // lớp phủ vai trò
			"architect": {Provider: "p", Model: "m"},                          // không có reasoning_effort, phải rơi về mặc định
		},
	}

	cases := []struct {
		role string
		want string
	}{
		{"writer", "high"},   // lớp phủ vai trò ưu tiên
		{"architect", "low"}, // vai trò không cấu hình -> rơi về mặc định cấp cao nhất
		{"editor", "low"},    // vai trò không tồn tại -> mặc định cấp cao nhất
		{"", "low"},          // rỗng -> mặc định cấp cao nhất
		{"default", "low"},   // default -> mặc định cấp cao nhất
		{"arbiter", "low"},   // vai trò ngoài cấu hình (phán định luôn theo mặc định cấp cao nhất)
	}
	for _, c := range cases {
		if got := cfg.ResolveReasoningEffort(c.role); got != c.want {
			t.Errorf("ResolveReasoningEffort(%q) = %q, want %q", c.role, got, c.want)
		}
	}

	// Khi mặc định cấp cao nhất cũng rỗng, vai trò không phủ trả về "" (không phủ).
	empty := Config{Roles: map[string]RoleConfig{"writer": {ReasoningEffort: "xhigh"}}}
	if got := empty.ResolveReasoningEffort("editor"); got != "" {
		t.Errorf("với mặc định rỗng, editor phải trả \"\", được %q", got)
	}
	if got := empty.ResolveReasoningEffort("writer"); got != "xhigh" {
		t.Errorf("với mặc định rỗng, lớp phủ writer phải có hiệu lực, được %q", got)
	}
}

func TestValidateBaseRejectsNonConfigurableRoles(t *testing.T) {
	for _, role := range []string{"coordinator", "arbiter"} {
		t.Run(role, func(t *testing.T) {
			cfg := Config{
				Provider:  "openrouter",
				ModelName: "test-model",
				Providers: map[string]ProviderConfig{
					"openrouter": {APIKey: "sk-test-123456"},
				},
				Roles: map[string]RoleConfig{
					role: {Provider: "openrouter", Model: "test-model"},
				},
			}

			err := cfg.ValidateBase()
			if err == nil {
				t.Fatalf("roles.%s phải bị từ chối", role)
			}
			if !errors.Is(err, errs.ErrConfig) {
				t.Fatalf("phải bọc errs.ErrConfig, được: %v", err)
			}
		})
	}
}

func TestValidateBaseNotifyEventsMatchRuntimeContract(t *testing.T) {
	validConfig := func(events []string) Config {
		return Config{
			Provider:  "openrouter",
			ModelName: "test-model",
			Providers: map[string]ProviderConfig{
				"openrouter": {APIKey: "sk-test-123456"},
			},
			Notify: NotifyConfig{Events: events},
		}
	}

	cfg := validConfig(notify.Kinds())
	if err := cfg.ValidateBase(); err != nil {
		t.Fatalf("hợp đồng sự kiện thông báo hiện tại phải qua hết kiểm tra cấu hình: %v", err)
	}

	cfg = validConfig([]string{"repeat"})
	if err := cfg.ValidateBase(); !errors.Is(err, errs.ErrConfig) {
		t.Fatalf("sự kiện repeat cũ phải bị từ chối, được: %v", err)
	}
}

func TestProviderStreamIdleTimeoutValue(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", defaultStreamIdleTimeout, false},
		{"900s", 15 * time.Minute, false},
		{"15m", 15 * time.Minute, false},
		{"abc", 0, true},
		{"-5s", 0, true},
		{"0", 0, true}, // không cung cấp "tắt watchdog" — luồng chết thật cần có cận hữu hạn
	}
	for _, c := range cases {
		got, err := ProviderConfig{StreamIdleTimeout: c.in}.StreamIdleTimeoutValue()
		if c.wantErr {
			if err == nil {
				t.Errorf("%q phải báo lỗi", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q = (%v, %v), want %v", c.in, got, err, c.want)
		}
	}
}

func TestValidateBaseRejectsBadStreamIdleTimeout(t *testing.T) {
	cfg := Config{
		Provider:  "openrouter",
		ModelName: "test-model",
		Providers: map[string]ProviderConfig{
			"openrouter": {APIKey: "sk-test-123456", StreamIdleTimeout: "fast"},
		},
	}
	if err := cfg.ValidateBase(); !errors.Is(err, errs.ErrConfig) {
		t.Fatalf("stream_idle_timeout bất hợp pháp phải bị từ chối và bọc ErrConfig, được: %v", err)
	}
}

// TestResolveStyleFromGenre chốt hợp đồng bắt buộc của T4 (docs/plans/style-genre-heading-fix.md):
// genre tiếng Việt phải suy ra đúng style key bền vững — "tiên hiệp" → "wuxia" (key giữ nguyên,
// chỉ nhãn hiển thị là tiếng Việt).
func TestResolveStyleFromGenre(t *testing.T) {
	if key, ok := styles.ResolveStyleFromGenre("tiên hiệp"); !ok || key != "wuxia" {
		t.Fatalf(`ResolveStyleFromGenre("tiên hiệp") = (%q, %v), want ("wuxia", true)`, key, ok)
	}
	cases := []struct {
		genre string
		want  string
	}{
		{"tiên hiệp", "wuxia"},
		{"Tiên Hiệp", "wuxia"},                 // bỏ hoa thường
		{"tien hiep", "wuxia"},                 // bỏ dấu
		{"tu tiên", "wuxia"},                   // alias cùng style
		{"xianxia", "wuxia"},                   // alias Latin
		{"truyện tiên hiệp đấu pháp", "wuxia"}, // genre tự do chứa alias theo ranh giới từ
		{"ngôn tình", "romance"},
		{"trinh thám", "suspense"},
		{"kỳ ảo", "fantasy"},
		{"khoa học viễn tưởng", ""}, // không khớp: không đoán bừa
		{"", ""},                    // rỗng
	}
	for _, c := range cases {
		key, ok := styles.ResolveStyleFromGenre(c.genre)
		if c.want == "" {
			if ok || key != "" {
				t.Errorf("ResolveStyleFromGenre(%q) = (%q, %v), want empty key and ok=false", c.genre, key, ok)
			}
			continue
		}
		if !ok || key != c.want {
			t.Errorf("ResolveStyleFromGenre(%q) = (%q, %v), want (%q, true)", c.genre, key, ok, c.want)
		}
	}
}

// TestConfigApplyGenreStyle chốt quy tắc áp style suy từ genre: chỉ khi config chưa chọn style
// đặc thù (rỗng/"default") — lựa chọn tường minh của người dùng luôn thắng (tương thích ngược).
func TestConfigApplyGenreStyle(t *testing.T) {
	cases := []struct {
		name       string
		style      string
		genre      string
		wantStyle  string
		wantChange bool
	}{
		{"default + tiên hiệp → wuxia", "default", "tiên hiệp", "wuxia", true},
		{"rỗng + genre từ prompt tự do → wuxia", "", "Viết truyện tu tiên cho nữ chính", "wuxia", true},
		{"tường minh fantasy không bị genre đè", "fantasy", "tiên hiệp", "fantasy", false},
		{"tường minh wuxia giữ nguyên khi genre khác", "wuxia", "ngôn tình", "wuxia", false},
		{"default + genre không khớp → giữ default", "default", "khoa học viễn tưởng", "default", false},
		{"default + genre 'chung' là no-op", "default", "chung", "default", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Config{Style: c.style}
			if got := cfg.ApplyGenreStyle(c.genre); got != c.wantChange {
				t.Fatalf("ApplyGenreStyle(%q) đổi style = %v, want %v", c.genre, got, c.wantChange)
			}
			if cfg.Style != c.wantStyle {
				t.Fatalf("Style sau ApplyGenreGenre = %q, want %q", cfg.Style, c.wantStyle)
			}
		})
	}
}
