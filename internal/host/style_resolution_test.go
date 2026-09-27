package host

// Hợp đồng suy style từ thể loại khi mở/tạo sách (T4/R3, docs/plans/style-genre-heading-fix.md).
//
// Ba nguồn sự thật theo thứ tự ưu tiên:
//  1. RunMeta.Style đặc thù của riêng cuốn sách (sticky) — thắng config toàn cục;
//  2. genre trong snapshot user_rules (meta/user_rules.json) — chỉ áp khi cfg.Style rỗng/"default";
//  3. fallback cfg.Style.
//
// Khi style hiệu dụng khác style bundle đang giữ thì bundle phải được nạp lại đúng style
// (references thể loại + voice ghi đè theo style), còn khi không đổi style thì bundle nguyên vẹn
// (không nạp lại oan — ghi đè prompt/voice của eval và test phải giữ).
//
// Cấp độ: integration hermetic — store tempdir + assets nhúng thật, không gọi mạng, không LLM
// (snapshot user rules ghi trực tiếp qua store, bỏ qua chuẩn hóa model).

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/rules"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// writeUserRulesSnapshot ghi snapshot user rules có genre xuống store, bỏ qua chuẩn hóa LLM.
func writeUserRulesSnapshot(t *testing.T, st *storepkg.Store, genre string) {
	t.Helper()
	snap := rules.Snapshot{
		Version:    rules.SnapshotVersion,
		Status:     rules.StatusReady,
		Structured: rules.Structured{Genre: genre},
	}
	if err := st.UserRules.Save(&snap); err != nil {
		t.Fatalf("Save user rules: %v", err)
	}
}

// newStyleTestStore dựng store đã Init trong tempdir.
func newStyleTestStore(t *testing.T) *storepkg.Store {
	t.Helper()
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return st
}

func TestResolveBookStyle_AdoptsRunMetaStyleOverGlobalConfig(t *testing.T) {
	st := newStyleTestStore(t)
	if err := st.RunMeta.Init("wuxia", "p", "m"); err != nil { // sách đã khóa style riêng
		t.Fatal(err)
	}
	cfg := bootstrap.Config{Style: "default", Language: "vi", OutputDir: st.Dir()}
	bundle := assets.LoadWithLanguage("vi", "default", assets.DefaultLoadOptions(st.Dir()))

	cfg, bundle, resolved, _ := resolveBookStyle(cfg, bundle, st)
	if cfg.Style != "wuxia" {
		t.Fatalf("style sách đã lưu phải thắng config toàn cục, được cfg.Style=%q", cfg.Style)
	}
	if resolved != "" {
		t.Fatalf("áp RunMeta.Style không phải suy từ genre — không kỳ vọng persist, được %q", resolved)
	}
	if bundle.Style != "wuxia" {
		t.Fatalf("bundle phải nạp lại theo style sách, được Style=%q", bundle.Style)
	}
	if bundle.References.StyleReference == "" {
		t.Fatal("bundle wuxia phải có references thể loại (StyleReference)")
	}
}

func TestResolveBookStyle_FromUserRulesGenre(t *testing.T) {
	st := newStyleTestStore(t)
	if err := st.RunMeta.Init("default", "p", "m"); err != nil { // sách cũ chưa khóa style
		t.Fatal(err)
	}
	writeUserRulesSnapshot(t, st, "tiên hiệp")
	cfg := bootstrap.Config{Style: "default", Language: "vi", OutputDir: st.Dir()}
	bundle := assets.LoadWithLanguage("vi", "default", assets.DefaultLoadOptions(st.Dir()))

	cfg, bundle, resolved, genre := resolveBookStyle(cfg, bundle, st)
	if resolved != "wuxia" || genre != "tiên hiệp" {
		t.Fatalf("genre tiên hiệp phải suy ra wuxia (resolved/genre = %q/%q)", resolved, genre)
	}
	if cfg.Style != "wuxia" {
		t.Fatalf("cfg.Style sau suy = %q, want wuxia", cfg.Style)
	}
	if bundle.Style != "wuxia" {
		t.Fatalf("bundle phải nạp lại theo style đã suy, được Style=%q", bundle.Style)
	}
}

func TestResolveBookStyle_KeepsExplicitConfigStyle(t *testing.T) {
	st := newStyleTestStore(t)
	if err := st.RunMeta.Init("default", "p", "m"); err != nil {
		t.Fatal(err)
	}
	writeUserRulesSnapshot(t, st, "tiên hiệp")
	cfg := bootstrap.Config{Style: "fantasy", Language: "vi", OutputDir: st.Dir()}
	bundle := assets.LoadWithLanguage("vi", "fantasy", assets.DefaultLoadOptions(st.Dir()))

	cfg, _, resolved, _ := resolveBookStyle(cfg, bundle, st)
	if cfg.Style != "fantasy" {
		t.Fatalf("lựa chọn tường minh phải thắng genre, được cfg.Style=%q", cfg.Style)
	}
	if resolved != "" {
		t.Fatalf("không được đánh dấu cần persist khi giữ lựa chọn tường minh, được %q", resolved)
	}
}

func TestResolveBookStyle_FreshBookWithoutGenreKeepsBundle(t *testing.T) {
	st := newStyleTestStore(t) // sách mới: chưa có run.json lẫn user_rules.json
	cfg := bootstrap.Config{Style: "default", Language: "vi", OutputDir: st.Dir()}
	bundle := assets.LoadWithLanguage("vi", "default", assets.DefaultLoadOptions(st.Dir()))
	bundle.Voice = "SENTINEL-VOICE" // đánh dấu bản này để phát hiện nạp lại oan

	cfg, bundle, resolved, _ := resolveBookStyle(cfg, bundle, st)
	if cfg.Style != "default" || resolved != "" {
		t.Fatalf("sách mới không genre phải giữ default (style=%q resolved=%q)", cfg.Style, resolved)
	}
	if bundle.Voice != "SENTINEL-VOICE" {
		t.Fatal("không đổi style thì không được nạp lại bundle (ghi đè voice của caller bị mất)")
	}
}

func TestApplyResolvedStyle_StartOfNewBook(t *testing.T) {
	st := newStyleTestStore(t)
	writeUserRulesSnapshot(t, st, "tiên hiệp") // PrepareUserRules vừa dựng snapshot có genre
	cfg := bootstrap.Config{Style: "default", Language: "vi", OutputDir: st.Dir()}
	h := &Host{
		cfg:    cfg,
		bundle: assets.LoadWithLanguage("vi", "default", assets.DefaultLoadOptions(st.Dir())),
		store:  st,
		engine: &engine{style: "default"},
		events: make(chan Event, 4),
	}

	if err := h.applyResolvedStyle("Viết giúp tôi truyện tiên hiệp"); err != nil {
		t.Fatalf("applyResolvedStyle: %v", err)
	}
	if h.cfg.Style != "wuxia" {
		t.Fatalf("cfg.Style sau áp = %q, want wuxia", h.cfg.Style)
	}
	if h.engine.style != "wuxia" {
		t.Fatalf("engine.style phải đồng bộ để phán định bù dùng đúng style, được %q", h.engine.style)
	}
	if h.bundle.Style != "wuxia" {
		t.Fatalf("bundle phải nạp lại theo style đã suy, được Style=%q", h.bundle.Style)
	}
	meta, err := st.RunMeta.Load()
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.Style != "wuxia" {
		t.Fatalf("RunMeta.Style phải được neo để style sticky theo sách, được %+v", meta)
	}
	select {
	case ev := <-h.events:
		if !strings.Contains(ev.Summary, "wuxia") {
			t.Fatalf("sự kiện áp style phải nêu rõ style, được %q", ev.Summary)
		}
	default:
		t.Fatal("phát một sự kiện SYSTEM báo style đã suy")
	}
}

func TestApplyResolvedStyle_FallsBackToRawPromptWhenSnapshotHasNoGenre(t *testing.T) {
	st := newStyleTestStore(t)
	writeUserRulesSnapshot(t, st, "") // chuẩn hóa không ra genre (degraded/sách cũ)
	h := &Host{
		cfg:    bootstrap.Config{Style: "default", Language: "vi", OutputDir: st.Dir()},
		bundle: assets.LoadWithLanguage("vi", "default", assets.DefaultLoadOptions(st.Dir())),
		store:  st,
		engine: &engine{style: "default"},
		events: make(chan Event, 4),
	}

	if err := h.applyResolvedStyle("Viết truyện tu tiên, nữ chính lãnh cổ phi hồn"); err != nil {
		t.Fatalf("applyResolvedStyle: %v", err)
	}
	if h.cfg.Style != "wuxia" {
		t.Fatalf("prompt gốc chứa 'tu tiên' phải suy được style, được %q", h.cfg.Style)
	}
}

func TestApplyResolvedStyle_RespectsExplicitStyle(t *testing.T) {
	st := newStyleTestStore(t)
	writeUserRulesSnapshot(t, st, "tiên hiệp")
	h := &Host{
		cfg:    bootstrap.Config{Style: "fantasy", Language: "vi", OutputDir: st.Dir()},
		bundle: assets.LoadWithLanguage("vi", "fantasy", assets.DefaultLoadOptions(st.Dir())),
		store:  st,
		engine: &engine{style: "fantasy"},
		events: make(chan Event, 4),
	}

	if err := h.applyResolvedStyle("Viết truyện tiên hiệp"); err != nil {
		t.Fatalf("applyResolvedStyle: %v", err)
	}
	if h.cfg.Style != "fantasy" || h.engine.style != "fantasy" {
		t.Fatalf("lựa chọn tường minh phải thắng genre (cfg=%q engine=%q)", h.cfg.Style, h.engine.style)
	}
	meta, err := st.RunMeta.Load()
	if err != nil {
		t.Fatal(err)
	}
	if meta != nil && meta.Style == "wuxia" {
		t.Fatal("không được neo style sai vào RunMeta khi không áp gì")
	}
}
