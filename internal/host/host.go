package host

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/arbiter"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/flow"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/host/sim"
	runtimelog "github.com/voocel/ainovel-cli/internal/logger"
	modelreg "github.com/voocel/ainovel-cli/internal/models"
	"github.com/voocel/ainovel-cli/internal/notify"
	"github.com/voocel/ainovel-cli/internal/revision"
	"github.com/voocel/ainovel-cli/internal/rules"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/styles"
	"github.com/voocel/ainovel-cli/internal/tools"
	"github.com/voocel/ainovel-cli/internal/userrules"
)

// Host là vỏ bọc runtime: vòng đời/lối vào can thiệp/chiếu sự kiện/quản lý model.
// Điều phối và thực thi nằm ở engine(vòng lặp deterministic); phán định ngữ nghĩa nằm ở arbiter(LLM-as-function).
type Host struct {
	cfg             bootstrap.Config
	bundle          assets.Bundle
	store           *storepkg.Store
	bookLease       *bookLease
	styleStats      *tools.StyleStatsIndex
	models          *bootstrap.ModelSet
	engine          *engine
	thinkingApplier agents.ApplyThinking // khi /model đổi mức suy luận thì liên đới cập nhật các Worker
	writerRestore   *ctxpack.WriterRestorePack
	// guardHook là proxy StopGuard nạp vào BuildWorkers (xem New): giữ tham chiếu ổn định qua
	// các lần dựng lại Worker giữa phiên (applyResolvedStyle) mà vẫn đẩy đúng sự kiện về h.
	guardHook   func(agent, reason string, consecutive int32)
	userRules   *userrules.Service
	observer    *observer
	usage       *UsageTracker
	usageCancel context.CancelFunc  // dừng autoSaveLoop và kích hoạt lần flush cuối
	budget      *BudgetSentinel     // chính sách ngân sách; nil khi chưa bật (method nil-safe)
	gate        *ChapterAdvanceGate // thành phần chính sách thống nhất cho giấy phép chương và tạm dừng một lần
	notifier    *notify.Notifier    // cảnh báo chế độ không người trực; nil khi chưa bật (Send nil-safe)
	configPath  string              // đích ghi config: /config, /model ghi vào bản đang hiệu lực gần nhất (có cấp dự án thì ghi nó, ngược lại ghi toàn cục)
	logCleanup  func()
	fileLogErr  error

	events   chan Event
	streamCh chan string
	done     chan struct{}

	mu         sync.Mutex
	lifecycle  lifecycle
	cocreating bool   // chiếm dụng đồng sáng tạo theo giai đoạn: trong cửa sổ paused chặn import/simulate/continue can thiệp đồng thời
	exclusive  string // chiếm dụng tác vụ độc chiếm nền (nhập/mô phỏng văn phong/chỉnh sửa): khác rỗng nghĩa là có tác vụ đang chạy, chặn các lối vào độc chiếm đồng thời
	// exclusiveCancel là hàm cancel của tác vụ độc chiếm hiện tại: dừng cứng ngân sách/tạm dừng thủ công phải dừng được
	// việc nhập đang đốt tiền, chứ không chỉ Engine — abortWithEvent cancel nó khi Engine không chạy (callback abort của
	// sentinel ngân sách và Abort thủ công dùng chung một cơ chế dừng). releaseExclusive dọn sạch luôn.
	exclusiveCancel context.CancelFunc
	closeOnce       sync.Once
	asyncWG         sync.WaitGroup
	closing         bool

	interMu sync.Mutex // phán định can thiệp nối tiếp FIFO (cùng lúc tối đa một tham vấn in-flight)

	outputMu     sync.RWMutex
	outputClosed bool

	// runCtx ràng buộc các gọi phán định LLM phía host (phán định khởi động/phân loại can thiệp); Close cancel,
	// tránh lúc thoát vẫn còn phán định in-flight mà không thể ngắt.
	runCtx    context.Context
	runCancel context.CancelFunc
}

type lifecycle string

const (
	lifecycleIdle      lifecycle = "idle"
	lifecycleRunning   lifecycle = "running"
	lifecyclePaused    lifecycle = "paused"
	lifecycleCompleted lifecycle = "completed"
)

// New tạo Host.
func New(cfg bootstrap.Config, bundle assets.Bundle, options ...NewOption) (*Host, error) {
	cfg.FillDefaults()
	if err := cfg.ValidateBase(); err != nil {
		return nil, err
	}
	var opts newOptions
	for _, option := range options {
		if option != nil {
			option(&opts)
		}
	}

	bookLease, err := acquireBookLease(cfg.OutputDir)
	if err != nil {
		return nil, err
	}
	keepBookLease := false
	var logCleanup func()
	defer func() {
		if keepBookLease {
			return
		}
		if err := bookLease.Close(); err != nil {
			slog.Error("Giải phóng chiếm dụng thư mục tiểu thuyết thất bại", "module", "host", "dir", cfg.OutputDir, "err", err)
		}
		if logCleanup != nil {
			logCleanup()
		}
	}()

	var fileLogErr error
	if opts.logFile != "" {
		logCleanup, fileLogErr = runtimelog.SetupFile(cfg.OutputDir, opts.logFile, opts.logAlsoStderr, opts.logAttrs...)
		if fileLogErr != nil {
			logCleanup = nil
			slog.Warn("Log file không khả dụng, tiếp tục dùng log của tiến trình hiện tại", "module", "host", "file", opts.logFile, "err", fileLogErr)
		}
	}

	slog.Info("Khởi động", "module", "boot", "provider", cfg.Provider, "model", cfg.ModelName, "output", cfg.OutputDir)

	// Chạy goroutine nền làm mới metadata model từ OpenRouter (cửa sổ ngữ cảnh/giá), cache đĩa 24h.
	modelreg.StartPricingRefresh(modelreg.DefaultRegistry(), bootstrap.DefaultConfigDir())

	store := storepkg.NewStore(cfg.OutputDir)
	// Nhãn Markdown dẫn xuất bám theo ngôn ngữ tác phẩm: các view này được novel_context đọc lại vào context,
	// nhãn khác ngôn ngữ với phần thân sẽ kéo model lệch sang ngôn ngữ khác.
	store.SetLanguage(cfg.NormalizedLanguage())
	if err := store.Init(); err != nil {
		return nil, fmt.Errorf("init store: %w", err)
	}
	configPath := bootstrap.EffectiveConfigPath()
	// T4 (R3, docs/plans/style-genre-heading-fix.md): suy style hiệu dụng của cuốn sách trước khi
	// ghi RunMeta và dựng Worker — genre trong user_rules ("tiên hiệp" → wuxia) phải tự chọn đúng
	// preset thể loại, không bó mọi sách vào cfg.Style toàn cục; sách cũ đã khóa style riêng thì
	// nạp lại bundle theo style của chính nó.
	cfg, bundle, resolvedStyle, resolvedGenre := resolveBookStyle(cfg, bundle, store)
	if resolvedStyle != "" {
		slog.Info("Đã suy style từ thể loại trong user_rules", "module", "host", "style", resolvedStyle, "genre", resolvedGenre)
		if err := bootstrap.SaveConfig(configPath, cfg); err != nil {
			slog.Warn("Lưu style suy từ thể loại vào cấu hình thất bại (vẫn dùng cho phiên này)", "module", "host", "err", err)
		}
	}
	// RunMeta là nguồn dữ kiện của mọi ngữ nghĩa điều khiển, phải hoàn tất kiểm tra trước khi dựng model/tác vụ nền.
	// advance mode lạ thì trả lỗi có cấu trúc ngay; cấm đoán mò downgrade rồi cứ thế ghi đĩa.
	if err := store.RunMeta.Init(cfg.Style, cfg.Provider, cfg.ModelName); err != nil {
		return nil, fmt.Errorf("init run meta: %w", err)
	}

	models, err := bootstrap.NewModelSet(cfg)
	if err != nil {
		return nil, fmt.Errorf("create models: %w", err)
	}
	slog.Info("Model sẵn sàng", "module", "boot", "summary", models.Summary())

	usage := NewUsageTracker(models, store)
	// Ưu tiên đọc meta/usage.json; các trường hợp sau đều đi qua backfill một lần từ sessions/*.jsonl:
	//   - file không tồn tại (trước lần persist đầu tiên)
	//   - phiên bản schema không khớp (bỏ format cũ sau khi nâng cấp tương lai)
	//   - file tồn tại nhưng hỏng / lỗi IO (không để dữ liệu hỏng đưa tích lũy về 0 vĩnh viễn)
	// Xong backfill gọi SaveNow ngay để chốt kết quả, lần khởi động sau Load trúng trực tiếp.
	loaded, loadErr := usage.LoadFromStore()
	if loadErr != nil {
		slog.Warn("Tải usage thất bại, sẽ thử backfill từ sessions", "module", "usage", "err", loadErr)
	}
	if !loaded {
		if n, err := usage.ReplaySessions(cfg.OutputDir); err != nil {
			slog.Warn("Replay usage thất bại", "module", "usage", "err", err)
		} else if n > 0 {
			slog.Info("usage backfill từ session hoàn tất", "module", "usage", "messages", n)
			if err := usage.SaveNow(); err != nil {
				slog.Warn("Lưu usage sau backfill thất bại", "module", "usage", "err", err)
			}
		}
	}
	usageCtx, usageCancel := context.WithCancel(context.Background())
	usage.StartAutoSave(usageCtx)

	// onGuardBlock khai báo trước: phải dựng xong h mới gắn được closure đẩy sự kiện ra ngoài.
	var onGuardBlock func(agent, reason string, consecutive int32)
	// guardHook là proxy nạp vào mọi lần BuildWorkers: giữ tham chiếu ổn định để dựng lại Worker
	// giữa phiên (applyResolvedStyle) vẫn đẩy đúng sự kiện StopGuard về h.
	guardHook := func(agent, reason string, consecutive int32) {
		if onGuardBlock != nil {
			onGuardBlock(agent, reason, consecutive)
		}
	}
	styleStats := tools.NewStyleStatsIndex(store)
	workers, restore, applyThinking := agents.BuildWorkers(cfg, store, styleStats, models, bundle, usage.Record, guardHook)
	store.Signals.ClearStaleSignals()

	h := &Host{
		cfg:             cfg,
		bundle:          bundle,
		store:           store,
		bookLease:       bookLease,
		styleStats:      styleStats,
		models:          models,
		thinkingApplier: applyThinking,
		guardHook:       guardHook,
		writerRestore:   restore,
		userRules:       userrules.NewService(store, models.Default, rules.DefaultOptions()),
		usage:           usage,
		usageCancel:     usageCancel,
		configPath:      configPath,
		logCleanup:      logCleanup,
		fileLogErr:      fileLogErr,
		events:          make(chan Event, 100),
		streamCh:        make(chan string, 256),
		done:            make(chan struct{}, 4),
		lifecycle:       lifecycleIdle,
	}
	h.runCtx, h.runCancel = context.WithCancel(context.Background())
	h.observer = newObserver(store, h.emitEvent, h.emitDelta, h.emitClear)
	// Arbiter phía host và Worker dùng chung một chuỗi ToolProgress → observer → bàn làm việc.
	h.runCtx = agentcore.WithToolProgress(h.runCtx, h.observer.workerProgress)
	if cfg.Notify.IsEnabled() {
		h.notifier = notify.New(cfg.Notify.Command, cfg.Notify.Events)
	}
	// Sentinel ngân sách: Engine gọi HandleBoundary trực tiếp tại biên mỗi vòng lặp (không còn đi qua subscribe sự kiện).
	if sentinel := NewBudgetSentinel(cfg.Budget,
		func() float64 { c, _, _, _, _ := usage.Totals(); return c },
		func(reason string) { h.abortWithEvent(reason, "error") },
		func(level, summary string) {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
			h.notifier.Send(notify.Notification{Kind: notify.KindBudget, Level: level, Title: "ainovel: ngân sách", Body: summary})
		},
	); sentinel != nil {
		h.budget = sentinel
		usage.SetOnCost(sentinel.OnCost)
		// Cảnh báo vùng mù tính phí: model không báo usage thì chi phí luôn 0, ngân sách không bao giờ kích hoạt — cầu chì chưa nối thì phải báo động.
		usage.SetOnMissingUsage(func() {
			const blind = "Vùng mù ngân sách: model không trả dữ liệu usage, thống kê chi phí là 0, hạn mức ngân sách sẽ không kích hoạt (model tùy chỉnh hãy kiểm tra giá trong registry hoặc include_usage phía upstream)"
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: blind, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: notify.KindBudget, Level: "warn", Title: "ainovel: ngân sách", Body: blind})
		})
	}
	// Cổng chặn tiến trình hợp nhất: thực thi hold một lần, và chặn chương mới chưa có giấy phép trong chế độ review.
	h.gate = NewChapterAdvanceGate(store,
		func(reason string) {
			h.abortWithEvent(reason, "info")
			h.notifier.Send(notify.Notification{Kind: notify.KindAdvanceGate, Level: "info", Title: "ainovel: chờ nghiệm thu", Body: reason})
		},
		func(level, summary string) {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
			h.notifier.Send(notify.Notification{Kind: notify.KindAdvanceGate, Level: level, Title: "ainovel: tiến trình chương", Body: summary})
		},
	)
	// Đẩy ra ngoài các lần chặn của StopGuard: blocked là hành động tự sửa tần suất cao, chỉ vào luồng sự kiện trên màn hình (push sẽ spam);
	// escalated / hard_stop nghĩa là tác vụ con lượt này bị hủy, phát cặp event+notify (kiến trúc §2.3).
	onGuardBlock = func(agent, reason string, n int32) {
		switch reason {
		case "escalated":
			body := fmt.Sprintf("%s quay trống %d lần liên tiếp không ghi đĩa sản phẩm cần thiết, tác vụ lượt này chấm dứt, trả lại Engine xử lý", agent, n)
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: agent, Summary: "StopGuard leo thang: " + body, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: notify.KindStopGuard, Level: "warn", Title: "ainovel: StopGuard", Body: body})
		case "hard_stop":
			body := fmt.Sprintf("%s bị provider từ chối trả lời (safety/content_filter), tác vụ lượt này chấm dứt ngay", agent)
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: agent, Summary: "StopGuard leo thang: " + body, Level: "warn"})
			h.notifier.Send(notify.Notification{Kind: notify.KindStopGuard, Level: "warn", Title: "ainovel: StopGuard", Body: body})
		default: // blocked
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Agent: agent,
				Summary: fmt.Sprintf("StopGuard: %s cố kết thúc khi chưa có sản phẩm cần thiết, đã chặn và thúc giục (liên tiếp lần thứ %d)", agent, n), Level: "info"})
		}
	}
	// Engine: engine thực thi deterministic (docs/engine-rfc.md). arbiter dùng model Default (giới hạn giai đoạn chuyển tiếp,
	// xem engine-arbiter.md §4.2).
	h.engine = &engine{
		store:           store,
		workers:         workers,
		arbiterModel:    newUsageTrackedModel(models.Default, "arbiter", usage.Record),
		failurePrompt:   bundle.Prompts.ArbiterFailure,
		planStartPrompt: bundle.Prompts.ArbiterPlanStart,
		style:           cfg.Style,
		// Re-consult đồng bộ: chặn vòng lặp engine một lần phán định (vài giây), đổi lấy "can thiệp có hiệu lực trước sáng tác tiếp theo".
		reconsult: h.handleIntervention,
		observer:  h.observer,
		budget:    h.budget,
		gate:      h.gate,
		refresh:   h.refreshWriterRestore,
		emitEvent: h.emitEvent,
		notify: func(kind, level, title, body string) {
			h.notifier.Send(notify.Notification{Kind: kind, Level: level, Title: title, Body: body})
		},
		onPause: func(summary string) { h.abortWithEvent(summary, "warn") },
		onDone:  h.runEnded,
	}

	keepBookLease = true
	return h, nil
}

// resolveBookStyle suy style hiệu dụng khi mở sách (T4/R3, docs/plans/style-genre-heading-fix.md):
//
//  1. RunMeta.Style đặc thù đã lưu là style sticky của riêng cuốn sách — thắng cfg.Style toàn cục
//     (nhiều sách chia sẻ một config); lần mở sau nạp lại bundle theo nó;
//  2. sách chưa khóa style → suy từ genre trong snapshot user_rules (meta/user_rules.json) qua
//     Config.ApplyGenreStyle — chỉ áp khi cfg.Style rỗng/"default", không đè lựa chọn tường minh
//     của người dùng; style suy được trả về để caller persist config (lựa chọn bền cho các lần sau);
//  3. fallback: giữ nguyên cfg.Style.
//
// Khi style hiệu dụng khác style bundle đang giữ thì nạp lại bundle để references thể loại
// (StyleReference/ArcTemplates) theo đúng style; bản đồ styles preset vốn nạp đủ mọi key.
func resolveBookStyle(cfg bootstrap.Config, bundle assets.Bundle, store *storepkg.Store) (bootstrap.Config, assets.Bundle, string, string) {
	meta, err := store.RunMeta.Load()
	if err != nil {
		slog.Warn("Đọc RunMeta thất bại, giữ style theo cấu hình", "module", "host", "err", err)
	}
	bookStyle := ""
	if meta != nil {
		bookStyle = strings.TrimSpace(meta.Style)
	}
	if bookStyle != "" && bookStyle != "default" {
		// Sách đã khóa style riêng (đã từng được chọn/suy): style của sách thắng config toàn cục.
		if bookStyle != cfg.Style {
			slog.Info("Dùng style đã lưu của sách (khác config toàn cục)", "module", "host", "style", bookStyle, "config_style", cfg.Style)
			cfg.Style = bookStyle
		}
		return cfg, maybeReloadBundle(cfg, bundle), "", ""
	}
	// Sách chưa khóa style: thử suy từ genre của snapshot user_rules nếu đã có trên đĩa.
	genre := ""
	if snap, serr := store.UserRules.Load(); serr != nil {
		slog.Warn("Đọc user rules thất bại, bỏ qua suy style từ thể loại", "module", "host", "err", serr)
	} else if snap != nil {
		genre = strings.TrimSpace(snap.Structured.Genre)
	}
	if cfg.ApplyGenreStyle(genre) {
		return cfg, maybeReloadBundle(cfg, bundle), cfg.Style, genre
	}
	return cfg, maybeReloadBundle(cfg, bundle), "", ""
}

// maybeReloadBundle nạp lại bundle theo style hiệu dụng của cfg khi khác style bundle đang giữ.
// Chỉ nạp khi cfg.Style là style đặc thù: "default" và "" nạp references như nhau nên bundle
// dựng tay không Style (test cũ) không bị thay thế oan, đánh đổi/ghi đè prompt của eval vẫn giữ.
func maybeReloadBundle(cfg bootstrap.Config, bundle assets.Bundle) assets.Bundle {
	if cfg.Style == "" || cfg.Style == "default" || bundle.Style == cfg.Style {
		return bundle
	}
	return assets.LoadWithLanguage(cfg.NormalizedLanguage(), cfg.Style, assets.DefaultLoadOptions(cfg.OutputDir))
}

// ── Vòng đời ──

// PrepareUserRules sinh snapshot user rules của sách trong chế độ tạo mới (deterministic phía khởi động, không vào Run sáng tác chính).
//
// Đầu vào là yêu cầu sáng tác **gốc** của người dùng (chưa qua BuildStartPrompt bọc) — chuẩn hóa cần chính user rules,
// không phải khung khởi động. Lối vào phải gọi một lần trước StartPrepared (cả hai đường tạo mới quick/cocreate đều đi qua đây).
//
// Chuẩn hóa thất bại chỉ downgrade không báo lỗi (đường tăng cường); chỉ khi snapshot không ghi đĩa được mới trả error hủy mở sách —
// các lần chạy sau sẽ không còn nguồn dữ kiện ổn định (xem thiết kế §Thất bại và downgrade).
func (h *Host) PrepareUserRules(rawPrompt string) error {
	if err := h.refuseNewBookOverExisting(); err != nil {
		return err
	}
	svc := userrules.NewService(h.store, h.models.Default, rules.DefaultOptions())
	snap, err := svc.Build(context.Background(), rawPrompt)
	if err != nil {
		return fmt.Errorf("Ghi đĩa snapshot user rules thất bại, không thể tiếp tục: %w", err)
	}
	logUserRulesSnapshot(snap)
	return nil
}

// ensureUserRules đảm bảo snapshot tồn tại trên đường khôi phục; thiếu thì sinh theo
// system_defaults + file rules.
func (h *Host) ensureUserRules() {
	svc := userrules.NewService(h.store, h.models.Default, rules.DefaultOptions())
	snap, err := svc.GetOrBuild(context.Background())
	if err != nil {
		slog.Warn("Đọc/sinh snapshot user rules thất bại, runtime sẽ fallback về mặc định dựng sẵn", "module", "rules", "err", err)
		return
	}
	logUserRulesSnapshot(snap)
}

// syncEngineLocked đồng bộ các trường engine bắt nguồn từ cfg/bundle hiện tại (prompt arbiter,
// style). Phải gọi dưới h.mu.
func (h *Host) syncEngineLocked() {
	if h.engine == nil {
		return
	}
	h.engine.failurePrompt = h.bundle.Prompts.ArbiterFailure
	h.engine.planStartPrompt = h.bundle.Prompts.ArbiterPlanStart
	h.engine.style = h.cfg.Style
}

// rebuildWorkersLocked dựng lại bộ Worker theo cfg/bundle hiện tại. Phải gọi dưới h.mu và chỉ
// khi Engine không chạy (StartPrepared đã chặn lifecycle==running): Runner chỉ được dùng trong
// vòng chạy engine, ngoài vòng chạy không ai giữ tham chiếu cũ nên thay con trỏ an toàn.
// Host dựng tay trong test (guardHook nil) không có Worker — chỉ đồng bộ engine.
func (h *Host) rebuildWorkersLocked() {
	h.syncEngineLocked()
	if h.guardHook == nil {
		return
	}
	workers, restore, applyThinking := agents.BuildWorkers(h.cfg, h.store, h.styleStats, h.models, h.bundle, h.usage.Record, h.guardHook)
	h.thinkingApplier = applyThinking
	h.writerRestore = restore
	if h.engine != nil {
		h.engine.workers = workers
	}
}

// applyResolvedStyle áp style suy từ thể loại cho sách mới (T4/R3): genre lấy từ snapshot
// user_rules vừa được PrepareUserRules dựng (nguồn đã chuẩn hóa, đáng tin hơn), thiếu thì thử
// trực tiếp trên prompt gốc (khớp theo từ, bỏ dấu/hoa thường). Chỉ áp khi cấu hình chưa chọn
// style đặc thù — tôn trọng lựa chọn tường minh của người dùng. Khi đổi style: neo RunMeta.Style
// (style sticky của sách), persist config, nạp lại bundle và dựng lại Worker để chính phiên
// sáng tác đầu tiên đã chạy đúng preset thể loại (không phải đợi khởi động lại).
func (h *Host) applyResolvedStyle(rawPrompt string) error {
	genre := ""
	if snap, err := h.store.UserRules.Load(); err != nil {
		slog.Warn("Đọc user rules thất bại khi suy style", "module", "host", "err", err)
	} else if snap != nil {
		genre = strings.TrimSpace(snap.Structured.Genre)
	}
	if genre == "" {
		genre = rawPrompt
	}

	h.mu.Lock()
	if !h.cfg.ApplyGenreStyle(genre) {
		h.mu.Unlock()
		return nil
	}
	style := h.cfg.Style
	h.bundle = maybeReloadBundle(h.cfg, h.bundle)
	h.rebuildWorkersLocked()
	cfgSnapshot := h.cfg
	h.mu.Unlock()

	if err := h.store.RunMeta.SetStyle(style); err != nil {
		return fmt.Errorf("Ghi style suy từ thể loại vào RunMeta: %w", err)
	}
	if h.configPath != "" {
		if err := bootstrap.SaveConfig(h.configPath, cfgSnapshot); err != nil {
			slog.Warn("Lưu style suy từ thể loại vào cấu hình thất bại (vẫn dùng cho sách này)", "module", "host", "err", err)
		}
	}
	label := styles.LabelFor(style)
	if label == "" {
		label = style
	}
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "info",
		Summary: fmt.Sprintf("Đã suy thể loại → style %q (%s); preset thể loại áp cho phiên này", style, label)})
	return nil
}

// logUserRulesSnapshot hiển thị lại lúc khởi động: cho người dùng thấy hệ thống hiểu rules thành gì (tái dùng log, không thêm cơ chế mới).
func logUserRulesSnapshot(snap *rules.Snapshot) {
	if snap == nil {
		return
	}
	slog.Info("Snapshot user rules",
		"module", "rules",
		"status", string(snap.Status),
		"nguồn", snap.Sources,
		"cụm từ bị cấm", len(snap.Structured.ForbiddenPhrases),
		"từ mệt mỏi", len(snap.Structured.FatigueWords),
	)
	if snap.Status == rules.StatusDegraded {
		slog.Warn("Một số rules không phân tích được, đang chạy theo raw preferences (có thể sinh lại snapshot)",
			"module", "rules", "uncertain", snap.Uncertain)
	}
}

// StartPrepared bắt đầu sáng tác bằng yêu cầu sáng tác **gốc** của người dùng: phán định plan_start chọn planner và mở rộng
// nhu cầu, kết quả phán định được chốt thành
// dữ kiện (PlanStartRecord) rồi mới khởi động Engine — khôi phục luôn dựa vào dữ kiện đã ghi đĩa, không làm lại phán định sẵn có.
// Dữ kiện đầu vào (StartPrompt) được ghi đĩa trước phán định: khi phán định thất bại nó là căn cứ để engine phán định bù,
// khởi động thất bại có thể tự sửa từ mọi lối khôi phục (Resume/tiếp tục), không phải ngõ cụt.
func (h *Host) StartPrepared(rawRequirement string) error {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("Đồng sáng tạo theo giai đoạn đang diễn ra, vui lòng kết thúc đồng sáng tạo trước")
	}
	h.mu.Unlock()

	rawRequirement = strings.TrimSpace(rawRequirement)
	if rawRequirement == "" {
		return fmt.Errorf("prompt is required")
	}
	if err := h.refuseNewBookOverExisting(); err != nil {
		return err
	}
	if err := upgradeProject(h.store); err != nil {
		return err
	}
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	if err := h.store.Checkpoints.Reset(); err != nil {
		return fmt.Errorf("reset checkpoints: %w", err)
	}
	if err := h.store.Progress.Init(0); err != nil {
		return fmt.Errorf("init progress: %w", err)
	}
	// Dữ kiện đầu vào ghi đĩa trước phán định: sau khi phán định thất bại (lỗi model v.v.) StartPrompt vẫn còn,
	// lúc khôi phục/tiếp tục engine dựa vào đó mà phán định bù (planStartFallback), khởi động thất bại không còn là ngõ cụt.
	if err := h.store.RunMeta.SetStartPrompt(rawRequirement); err != nil {
		return fmt.Errorf("Ghi yêu cầu sáng tác: %w", err)
	}

	// T4 (R3): sách mới — suy style đặc thù từ thể loại (genre trong user_rules / prompt gốc)
	// trước khi phán định khởi động (arbiter nhận đúng style) và trước khi Worker chạy tác vụ đầu tiên.
	if err := h.applyResolvedStyle(rawRequirement); err != nil {
		return err
	}

	// Phán định khởi động: thất bại thì báo lỗi rõ ràng và hủy (giai đoạn khởi động người dùng có mặt, báo lỗi tốt hơn đoán mò).
	start := time.Now()
	decision, derr := runObservedDecision(h.observer, "Phán định khởi động", func() (arbiter.PlanStartDecision, error) {
		return arbiter.DecidePlanStart(h.runCtx, h.arbiterModel(),
			h.bundle.Prompts.ArbiterPlanStart, rawRequirement, h.cfg.Style)
	})
	rec := storepkg.DecisionRecord{Kind: "plan_start", Decider: "arbiter", Input: rawRequirement,
		Reason: decision.Reason, DurationMs: time.Since(start).Milliseconds()}
	if derr == nil {
		if data, err := json.Marshal(decision); err == nil {
			rec.Decision = data
		}
	} else {
		rec.Error = derr.Error()
	}
	var recErr error
	if rec, recErr = h.store.Decisions.Append(rec); recErr != nil {
		slog.Warn("Ghi đĩa audit phán định khởi động thất bại", "module", "host", "err", recErr)
	}
	if derr != nil {
		return fmt.Errorf("Phán định khởi động thất bại: %w", derr)
	}
	if err := h.store.RunMeta.SetPlanStart(domain.PlanStartRecord{
		RawPrompt: rawRequirement, Planner: decision.Planner, PlannerTask: decision.Task, DecisionID: rec.ID,
	}); err != nil {
		return fmt.Errorf("Ghi phán định khởi động: %w", err)
	}

	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM",
		Summary: fmt.Sprintf("Bắt đầu sáng tác (planner: %s — %s)", decision.Planner, decision.Reason), Level: "info"})
	if !h.startEngine(&flow.Instruction{Agent: decision.Planner, Task: decision.Task, Reason: decision.Reason}) {
		return fmt.Errorf("Engine đang chạy hoặc đang dừng, không thể khởi động sách mới")
	}
	return nil
}

// refuseNewBookOverExisting từ chối mở sách mới trong thư mục đã có chương hoàn thành: StartPrepared sẽ reset
// checkpoints và progress, chạm nhầm là âm thầm xóa sạch chuỗi tiến độ của cả sách (sau khi nhập xong đứng ở trang chào
// mà bấm nhầm Enter là kịch bản điển hình nhất). Chỉ nhìn số chương đã hoàn thành — tàn dư giai đoạn lập dàn ý/khởi động thất bại chưa có chương hoàn thành,
// cho qua để giữ đường tự sửa cho đồng sáng tạo Ctrl+S thử lại cùng session và khôi phục phán định bù.
func (h *Host) refuseNewBookOverExisting() error {
	progress, err := h.store.Progress.Load()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if progress == nil || len(progress.CompletedChapters) == 0 {
		return nil
	}
	book, err := h.store.Book.Load()
	if err != nil {
		return err
	}
	if book == nil {
		return fmt.Errorf("Thư mục xuất đã có chương, nhưng thông tin tác phẩm không tồn tại")
	}
	name := book.Title
	return fmt.Errorf("Thư mục xuất đã có tiến độ sáng tác %d chương của 《%s》, tạo mới sẽ reset tiến độ và checkpoint của nó: viết tiếp hãy đi qua lối khôi phục (khởi động lại ứng dụng sẽ tự khôi phục), sách mới hãy đổi thư mục xuất",
		len(progress.CompletedChapters), name)
}

// startEngine là lối khởi động engine hợp nhất (Start/Resume/Continue/khởi động lại sau can thiệp dùng chung).
// lifecycle phải được đặt running trước khi goroutine khởi động: engine có thể kết thúc ngay (hoàn sách/không có route),
// runEnded sẽ đưa lifecycle về trạng thái cuối; nếu thứ tự ngược, runEnded chạy trước rồi đây mới ghi running,
// UI sẽ mãi mãi hiển thị "đang chạy" trong khi engine thực tế đã dừng.
func (h *Host) startEngine(initial *flow.Instruction) bool {
	// Cổng kiểm soát xuyên khởi động lại: khi workspace nhập chưa hoàn tất, cấm Engine thường tiêu thụ trạng thái phát hành dở dang (RFC §12.5).
	active, done, importErr := imp.ResumeStatus(h.store)
	if importErr != nil {
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error",
			Summary: "Đọc trạng thái nhập thất bại, đã chặn sáng tác thường ghi đè artifact hiện có: " + importErr.Error()})
		return false
	}
	if active && !done {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
			Summary: "Có tác vụ nhập tiểu thuyết bên ngoài chưa hoàn tất, vui lòng chạy /import khôi phục hoàn tất trước rồi mới tiếp tục sáng tác"})
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return false
	}
	// Khi tác vụ độc chiếm nền (nhập/mô phỏng văn phong) đang chạy, engine không được chạy trước, tránh tranh chấp ghi. Đây là mọi đường khởi động engine
	// (khởi động lại Resume/Continue/tự động tiếp sức/next) dùng chung một backstop — guard lối vào là lớp một, đây là lớp cuối.
	if h.exclusive != "" {
		return false
	}
	// lifecycle có thể đã là paused, nhưng goroutine Engine cũ vẫn đang chạy defer thoát.
	// Phải đối chiếu cả trạng thái thật của Engine; nếu không sẽ ghi lifecycle lại thành running trong khi start
	// thực chất no-op, rồi runEnded cũ lại đưa nó về idle.
	if h.engine.isRunning() {
		return false
	}
	h.observer.setAborting(false)
	previous := h.lifecycle
	h.lifecycle = lifecycleRunning
	if !h.engine.start(initial) {
		h.lifecycle = previous
		return false
	}
	return true
}

// Reopen ép mở lại sách đã hoàn thành thành trạng thái sáng tác. Hoàn sách và mở lại đều là quyết định lớn: hoàn sách có thể do architect phán định,
// mở lại chỉ do người dùng chủ động thực hiện (/reopen), không qua phán định model. direction khác rỗng thì ghi thành can thiệp treo,
// lúc khôi phục đi qua Arbiter phán định rồi tiêm vào (cùng kênh với can thiệp lúc dừng máy), sau đó chạy tiếp engine (route cuối quyển giao việc viết tiếp quyển).
func (h *Host) Reopen(direction string) error {
	h.mu.Lock()
	switch {
	case h.lifecycle == lifecycleRunning:
		h.mu.Unlock()
		return fmt.Errorf("Engine sáng tác đang chạy, không cần mở lại")
	case h.cocreating:
		h.mu.Unlock()
		return fmt.Errorf("Đồng sáng tạo theo giai đoạn đang diễn ra, vui lòng kết thúc đồng sáng tạo trước")
	case h.exclusive != "":
		ex := h.exclusive
		h.mu.Unlock()
		return fmt.Errorf("%s đang diễn ra, vui lòng hoàn tất trước khi mở lại", ex)
	}
	h.mu.Unlock()
	if err := h.requireCleanChapters(); err != nil {
		return err
	}

	if err := h.store.Progress.ReopenContinue(); err != nil {
		return err
	}
	reopenEvent := Event{Time: time.Now(), Category: "SYSTEM", Summary: "Đã mở lại sách thành trạng thái sáng tác (người dùng thu hồi phán định hoàn thành)", Level: "info"}
	if d := strings.TrimSpace(direction); d != "" {
		reopenEvent.Detail = reopenEvent.Summary + "\nHướng viết tiếp: " + d
	}
	h.emitEvent(reopenEvent)
	if d := strings.TrimSpace(direction); d != "" {
		if err := h.store.RunMeta.SetPendingSteer(d); err != nil {
			return fmt.Errorf("Đã mở lại, nhưng ghi hướng viết tiếp thất bại: %v, vui lòng nhập lại hướng trực tiếp trong ô nhập liệu", err)
		}
	}
	return nil
}

// Resume chế độ khôi phục: sinh resume prompt từ checkpoint + progress rồi khởi động.
func (h *Host) Resume() (string, error) {
	h.mu.Lock()
	if h.lifecycle == lifecycleRunning {
		h.mu.Unlock()
		return "", fmt.Errorf("already running")
	}
	if h.cocreating {
		h.mu.Unlock()
		return "", fmt.Errorf("Đồng sáng tạo theo giai đoạn đang diễn ra, vui lòng kết thúc đồng sáng tạo trước")
	}
	if h.exclusive != "" {
		ex := h.exclusive
		h.mu.Unlock()
		return "", fmt.Errorf("%s đang diễn ra, vui lòng hoàn tất trước khi khôi phục sáng tác", ex)
	}
	h.mu.Unlock()
	if err := upgradeProject(h.store); err != nil {
		return "", err
	}

	label, err := resumeLabel(h.store)
	if err != nil {
		return "", err
	}
	if label == "" {
		return "", nil // chế độ tạo mới, không có gì để khôi phục
	}
	if err := h.requireCleanChapters(); err != nil {
		return label, err
	}
	if err := h.budget.Refuse(); err != nil {
		return "", err
	}

	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Khôi phục sáng tác: " + label, Level: "info"})
	for _, w := range h.store.CheckConsistency() {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Cảnh báo nhất quán: " + w, Level: "warn"})
	}
	// Đảm bảo snapshot user rules tồn tại; đã có thì chỉ đọc rẻ.
	h.ensureUserRules()
	h.refreshWriterRestore()
	// Can thiệp treo (do lúc dừng máy để lại/tàn dư crash lúc phán định) phải được phán định trước khi engine chạy tiếp —
	// nếu không engine có thể chạy trước phán định mà viết tiếp chương trái với can thiệp. Chạy đồng bộ (chặn vài giây chấp nhận được,
	// UI đã hiển thị "Khôi phục sáng tác"); doIntervention thành công sẽ tự xóa PendingSteer và
	// kéo engine lên theo restart=true. Không có can thiệp treo → chạy tiếp luôn.
	meta, err := h.store.RunMeta.Load()
	if err != nil {
		return label, fmt.Errorf("Đọc can thiệp treo: %w", err)
	}
	if meta != nil && meta.PendingSteer != "" {
		if err := h.doIntervention(meta.PendingSteer, true); err != nil {
			return label, err
		}
	} else {
		// Chỉ khôi phục dữ kiện, không khôi phục session (RFC §6): Engine tính lại route từ store rồi chạy tiếp.
		if !h.startEngine(nil) {
			return label, fmt.Errorf("Engine đang hoàn tất lượt dừng trước, vui lòng thử khôi phục lại sau")
		}
	}
	// lifecycle do startEngine / runEnded quản lý, ở đây không ghi đè nữa —
	// nếu engine kết thúc ngay (hoàn sách v.v.) thì ghi đè sẽ đưa trạng thái cuối về running.
	return label, nil
}

// handleIntervention thích ứng callback re-consult không trả giá trị của Engine; lỗi đã do doIntervention phát sự kiện.
func (h *Host) handleIntervention(text string) {
	_ = h.doIntervention(text, false)
}

// doIntervention là đường phán định hợp nhất cho can thiệp người dùng: Collect → Decide → thực thi.
// Nối tiếp FIFO (cùng lúc tối đa một tham vấn in-flight); answer/rules thực thi ngay, hành động trạng thái điều khiển
// (hold/reopen/dispatch) khi engine đang chạy thì xếp hàng commit tại biên, lúc dừng máy thì thực thi ngay.
// restart=true (ngữ nghĩa Continue) thì sau khi xử lý can thiệp xong đảm bảo engine chạy.
func (h *Host) doIntervention(text string, restart bool) error {
	h.interMu.Lock()
	defer h.interMu.Unlock()

	// Bảo vệ crash: persist trước khi phán định (PendingSteer), sau khi áp dụng thành công hoặc đã hiển thị thất bại trực diện thì xóa nguyên tử
	// (ClearHandledSteer đồng thời reset FlowSteering). Crash trong lúc phán định → lần Resume sau replay.
	if err := h.store.RunMeta.SetPendingSteer(text); err != nil {
		wrapped := fmt.Errorf("Persist can thiệp thất bại, đã dừng phán định: %w", err)
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Agent: "arbiter",
			Summary: wrapped.Error(), Detail: wrapped.Error(), Level: "error"})
		return wrapped
	}
	clearPending := func() error {
		if err := h.store.ClearHandledSteer(); err != nil {
			return fmt.Errorf("Xóa can thiệp đã xử lý thất bại: %w", err)
		}
		return nil
	}

	facts, err := arbiter.CollectInterventionFacts(h.store)
	if err != nil {
		wrapped := fmt.Errorf("Thu thập dữ kiện can thiệp thất bại, chưa gọi Arbiter: %w", err)
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Agent: "arbiter",
			Summary: wrapped.Error(), Detail: wrapped.Error(), Level: "error"})
		return wrapped
	}
	facts.Running = h.engine.isRunning()

	start := time.Now()
	decision, derr := runObservedDecision(h.observer, "Phán định can thiệp người dùng", func() (arbiter.InterventionDecision, error) {
		return arbiter.DecideIntervention(h.runCtx, h.arbiterModel(),
			h.bundle.Prompts.ArbiterIntervention, facts, text)
	})

	rec := storepkg.DecisionRecord{Kind: "intervention", Decider: "arbiter", Input: text,
		Reason: decision.Reason, DurationMs: time.Since(start).Milliseconds()}
	if cp := h.store.Checkpoints.LatestGlobal(); cp != nil {
		rec.CheckpointSeq = cp.Seq
	}
	if data, err := json.Marshal(facts); err == nil {
		rec.Facts = data
	}
	if derr == nil {
		if data, err := json.Marshal(decision); err == nil {
			rec.Decision = data
		}
	} else {
		rec.Error = derr.Error()
	}
	if _, err := h.store.Decisions.Append(rec); err != nil {
		wrapped := fmt.Errorf("Ghi đĩa audit phán định can thiệp thất bại, từ chối thực thi hành động: %w", err)
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Agent: "arbiter",
			Summary: wrapped.Error(), Detail: wrapped.Error(), Level: "error"})
		return wrapped
	}

	if derr != nil {
		// Thà không động, chứ không động nhầm: không tạo bất kỳ ghi nào. Lỗi gọi và
		// lỗi kiểm tra đầu ra dùng chung một kênh error, phải hiển thị lại nguyên văn, không được ngụy trang thành "không hiểu được".
		// Đã báo trực tiếp → xóa pending (không thì lần Resume sau sẽ tự động replay đúng can thiệp thất bại đó).
		h.emitEvent(newInterventionFailureEvent(derr))
		if err := clearPending(); err != nil {
			return fmt.Errorf("%v；%w", derr, err)
		}
		return derr
	}

	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Phán định: " + decision.Reason, Level: "info"})
	if decision.Answer != "" {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: decision.Answer, Level: "info"})
	}
	// Bất kỳ hành động nào persist thất bại → giữ PendingSteer (khi khôi phục replay toàn bộ để phán định lại;
	// hold/reopen idempotent, dispatch re-consult theo dữ kiện mới, replay an toàn).
	var actionErr error
	if decision.Rules != "" {
		if snap, _, err := h.userRules.AddRuntimeRule(h.runCtx, decision.Rules); err != nil {
			h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Summary: "Ghi đĩa rules viết thất bại: " + err.Error(), Level: "error"})
			actionErr = err
		} else if snap != nil {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Rules viết đã cập nhật và được persist", Level: "info"})
		}
	}

	if decision.Hold != nil || decision.Reopen != nil || decision.Dispatch != nil {
		op := controlOp{hold: decision.Hold, reopen: decision.Reopen, dispatch: decision.Dispatch, text: text, facts: facts}
		if !h.engine.enqueue(op) {
			// Engine chưa chạy: thực thi ngay; persist thất bại → giữ PendingSteer, lúc khôi phục replay toàn bộ can thiệp.
			if err := h.engine.applyControlOp(context.Background(), op); err != nil {
				h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
					Summary: "Thực thi hành động can thiệp thất bại, đã giữ lại; khi khôi phục/tiếp tục sẽ tự động thử lại"})
				return err
			}
			// reopen/dispatch thể hiện ý định tiếp tục sáng tác, kéo engine lên.
			if decision.Reopen != nil || decision.Dispatch != nil {
				restart = true
			}
		}
	}
	if actionErr != nil {
		// Giữ PendingSteer: khi khôi phục/tiếp tục replay toàn bộ để phán định lại.
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
			Summary: "Một số hành động can thiệp chưa thành công, can thiệp đã được giữ lại; khi khôi phục/tiếp tục sẽ tự động thử lại"})
		return actionErr
	}
	// Hành động đã áp dụng/xếp hàng thành công, xóa bảo vệ crash (sau khi xếp hàng, lỗi phía engine hoặc race thoát do engine
	// ghi lại PendingSteer làm fallback).
	if err := clearPending(); err != nil {
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error", Summary: err.Error()})
		return err
	}

	if restart && !h.engine.isRunning() {
		if err := h.budget.Refuse(); err != nil {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: err.Error(), Level: "warn"})
			return err
		}
		h.refreshWriterRestore()
		if !h.startEngine(nil) {
			// Lúc này hành động can thiệp đã hiệu lực và PendingSteer đã xóa, chỉ là engine chưa kéo lên ngay được — không được nói dối là "đã lưu".
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
				Summary: "Can thiệp đã hiệu lực, nhưng Engine chưa chạy tiếp ngay được; vui lòng tiếp tục trong ô nhập liệu sau ít lâu hoặc khởi động lại ứng dụng để khôi phục"})
			return fmt.Errorf("Can thiệp đã hiệu lực, nhưng Engine chưa chạy tiếp ngay được")
		}
	}
	return nil
}

func newInterventionFailureEvent(err error) Event {
	detail := err.Error()
	return Event{
		Time:     time.Now(),
		Category: "ERROR",
		Agent:    "arbiter",
		Summary:  "Phán định can thiệp thất bại: " + detail + " (chưa thay đổi gì)",
		Detail:   detail,
		Kind:     errorKind(err, detail),
		Level:    "error",
	}
}

// arbiterModel trả model phán định kèm theo dõi lượng dùng (token/chi phí vào ngân sách và hệ thống usage).
func (h *Host) arbiterModel() agentcore.ChatModel {
	return newUsageTrackedModel(h.models.Default, "arbiter", h.usage.Record)
}

// Continue được gọi khi người dùng nhập trong ô nhập liệu sau khi dừng máy: phán định can thiệp + đảm bảo engine chạy lại.
func (h *Host) Continue(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("text is required")
	}
	h.mu.Lock()
	if h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("Đồng sáng tạo theo giai đoạn đang diễn ra, vui lòng kết thúc đồng sáng tạo trước")
	}
	if h.exclusive != "" {
		ex := h.exclusive
		h.mu.Unlock()
		// Trong lúc tác vụ độc chiếm chạy phải chặn trước phán định: nếu không Arbiter đã đổi PendingSteer/rules/trạng thái điều khiển rồi engine mới bị cổng chặn.
		return fmt.Errorf("%s đang diễn ra, vui lòng hoàn tất trước khi tiếp tục sáng tác", ex)
	}
	h.mu.Unlock()
	if err := h.requireCleanChapters(); err != nil {
		return err
	}
	if err := h.budget.Refuse(); err != nil {
		return err
	}

	err, launched := h.runAsync(func() error {
		h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[Tiếp tục] " + text, Level: "info"})
		return h.doIntervention(text, true)
	})
	if !launched {
		return fmt.Errorf("Host đang đóng, không thể tiếp tục sáng tác")
	}
	return err
}

// SetAdvanceMode chuyển chế độ tiến trình chương kiểu deterministic. Nó chỉ ghi ý định vận hành của người dùng,
// không gọi Arbiter, cũng không âm thầm khởi động Engine đã tạm dừng.
func (h *Host) SetAdvanceMode(mode domain.ChapterAdvanceMode) error {
	h.interMu.Lock()
	defer h.interMu.Unlock()
	if err := h.store.RunMeta.SetAdvanceMode(mode); err != nil {
		return err
	}
	label := "tự động tiến trình"
	if mode == domain.ChapterAdvanceReview {
		label = "nghiệm thu từng chương"
	}
	summary := "Chế độ tiến trình chương đã chuyển sang " + label
	h.mu.Lock()
	state := h.lifecycle
	h.mu.Unlock()
	if mode == domain.ChapterAdvanceAuto && state != lifecycleRunning && state != lifecycleCompleted {
		summary += "; hiện vẫn đang tạm dừng, nhập lệnh tiếp tục để chạy lại"
	}
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "info"})
	return nil
}

// AdvanceOneChapter cấp phép chính xác một chương trong chế độ nghiệm thu từng chương và khởi động Engine.
func (h *Host) AdvanceOneChapter() error {
	h.interMu.Lock()
	defer h.interMu.Unlock()

	h.mu.Lock()
	running, cocreating, ex := h.lifecycle == lifecycleRunning, h.cocreating, h.exclusive
	h.mu.Unlock()
	if running || h.engine.isRunning() {
		return fmt.Errorf("Sáng tác vẫn đang chạy hoặc đang hoàn tất tạm dừng, vui lòng chạy /next lại sau")
	}
	if cocreating {
		return fmt.Errorf("Đồng sáng tạo theo giai đoạn đang diễn ra, vui lòng kết thúc đồng sáng tạo trước")
	}
	if ex != "" {
		return fmt.Errorf("%s đang diễn ra, vui lòng hoàn tất trước khi chạy /next", ex)
	}
	if err := h.requireCleanChapters(); err != nil {
		return err
	}
	meta, err := h.store.RunMeta.Load()
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("RunMeta chưa khởi tạo")
	}
	if meta.AdvanceMode != domain.ChapterAdvanceReview {
		return fmt.Errorf("/next chỉ dùng cho chế độ nghiệm thu từng chương, vui lòng chạy /review on trước")
	}
	if meta.AdvanceHold != nil {
		return fmt.Errorf("Vẫn còn ý định tạm dừng một lần đang treo (%s), vui lòng khôi phục hoặc hoàn tất can thiệp hiện tại trước", meta.AdvanceHold.Reason)
	}
	if err := h.budget.Refuse(); err != nil {
		return err
	}
	progress, err := h.store.Progress.Load()
	if err != nil {
		return err
	}
	if progress == nil || progress.Phase != domain.PhaseWriting {
		phase := "<nil>"
		if progress != nil {
			phase = string(progress.Phase)
		}
		return fmt.Errorf("Giai đoạn hiện tại không thể cấp phép chương mới (phase=%s)", phase)
	}
	target := progress.NextChapter()
	if target <= 0 {
		return fmt.Errorf("Không suy ra được chương tiếp theo từ tiến độ hiện tại")
	}
	if err := h.store.RunMeta.GrantAdvancePermit(target); err != nil {
		return err
	}
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM",
		Summary: fmt.Sprintf("Đã cấp phép cho chương %d; sau khi chương này nộp sẽ hoàn tất phần xem xét và bảo trì cấu trúc cung/quyển cần thiết, rồi lại chờ cấp phép", target), Level: "info"})
	h.refreshWriterRestore()
	if !h.startEngine(nil) {
		// Giấy phép persist theo số chương và idempotent với cùng mục tiêu, bên gọi thử lại sau sẽ không cấp trùng.
		return fmt.Errorf("Giấy phép chương đã lưu, nhưng Engine vẫn đang hoàn tất lượt dừng trước; vui lòng chạy /next lại sau")
	}
	return nil
}

// Steer gửi can thiệp người dùng (dùng được mọi lúc khi đang chạy; lúc dừng máy thì sau phán định tùy hành động mà quyết định có kéo engine lên không).
// TUI chờ kết quả qua tea.Cmd nên nhận được lỗi phán định/persist thật mà không chặn giao diện.
func (h *Host) Steer(text string) error {
	err, launched := h.runAsync(func() error {
		h.emitEvent(Event{Time: time.Now(), Category: "USER", Summary: "[Can thiệp người dùng] " + text, Level: "info"})
		return h.doIntervention(text, false)
	})
	if !launched {
		return fmt.Errorf("Host đang đóng, không thể gửi can thiệp")
	}
	return err
}

// Abort tạm dừng vòng lặp engine hiện tại.
func (h *Host) Abort() bool {
	return h.abortWithEvent("Người dùng thủ công tạm dừng sáng tác hiện tại", "warn")
}

// abortWithEvent thực thi tạm dừng với sự kiện lý do chỉ định. Dừng theo ngân sách và tạm dừng thủ công dùng chung một cơ chế dừng,
// chỉ khác văn bản sự kiện (dừng ngân sách = lệnh Abort người dùng ký trước, ngữ nghĩa tương đương tạm dừng thủ công).
func (h *Host) abortWithEvent(summary, level string) bool {
	h.mu.Lock()
	running := h.lifecycle == lifecycleRunning
	if running {
		h.lifecycle = lifecyclePaused
	}
	cancelExclusive := h.exclusiveCancel
	h.mu.Unlock()
	if running {
		// Đặt cờ phải trước engine.abort: cancel lan truyền sẽ ngay lập tức sinh sự kiện stream init / worker
		// thất bại, observer dựa cờ này nhận diện là nhiễu sinh ra từ abort và triệt tiêu.
		h.observer.setAborting(true)
		h.engine.abort()
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
		return true
	}
	// Engine chưa chạy nhưng tác vụ độc chiếm (nhập v.v.) đang chạy: nó cũng đang đốt tiền, dừng cứng ngân sách/tạm dừng thủ công phải
	// dừng được nó — nếu không chính sách ngân sách vô hiệu với việc nhập (docs/import-pipeline.md §13.1).
	if cancelExclusive != nil {
		cancelExclusive()
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: level})
		return true
	}
	return false
}

// Close chấm dứt engine và đóng kênh sự kiện.
//
// Ngữ nghĩa persist Usage: cancel autoSaveLoop trước (nó tự flush lần dirty cuối),
// rồi bù một lần SaveNow đồng bộ để kết thúc. Sau khi chấm dứt, vài trăm token cuối của gọi LLM in-flight
// bị mất sẽ được session jsonl replay bù lại tự động ở lần khởi động sau.
func (h *Host) Close() {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closing = true
		cancelExclusive := h.exclusiveCancel
		h.mu.Unlock()

		h.observer.setAborting(true)
		if h.runCancel != nil {
			h.runCancel() // ngắt các gọi phán định phía host đang truyền và chuyển tiếp của supervisor
		}
		if cancelExclusive != nil {
			cancelExclusive()
		}
		h.engine.abort()
		h.engine.wait()
		h.asyncWG.Wait()

		if h.usageCancel != nil {
			h.usageCancel()
			h.usageCancel = nil
		}
		h.usage.WaitAutoSave()
		if err := h.usage.SaveNow(); err != nil {
			slog.Warn("Lưu usage trước khi thoát thất bại", "module", "usage", "err", err)
		}
		h.closeOutputChannels()
		if err := h.bookLease.Close(); err != nil {
			slog.Error("Giải phóng chiếm dụng thư mục tiểu thuyết thất bại", "module", "host", "dir", h.cfg.OutputDir, "err", err)
		}
		if h.logCleanup != nil {
			h.logCleanup()
			h.logCleanup = nil
		}
	})
}

// FileLogError trả lỗi khởi tạo log file ở giai đoạn dựng; không đổi trong vòng đời Host.
func (h *Host) FileLogError() error {
	return h.fileLogErr
}

// runEnded được engine.onDone gọi khi vòng lặp engine kết thúc (bất kể lý do): định trạng thái cuối theo dữ kiện store.
//   - Phase=Complete  → đánh dấu completed, phát sự kiện "hoàn tất sáng tác"
//   - khác           → đánh dấu idle/paused, phát sự kiện "sáng tác dừng"
func (h *Host) runEnded() {
	h.observer.finalize()

	h.mu.Lock()
	progress, err := h.store.Progress.Load()
	if err != nil {
		if h.lifecycle == lifecycleRunning {
			h.lifecycle = lifecycleIdle
		}
		h.mu.Unlock()
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error",
			Summary: "Đọc tiến độ thất bại khi engine kết thúc: " + err.Error()})
		select {
		case h.done <- struct{}{}:
		default:
		}
		return
	}
	book, err := h.store.Book.Load()
	if err != nil {
		h.lifecycle = lifecycleIdle
		h.mu.Unlock()
		h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error",
			Summary: "Đọc thông tin tác phẩm thất bại khi engine kết thúc: " + err.Error()})
		select {
		case h.done <- struct{}{}:
		default:
		}
		return
	}
	if progress != nil && progress.Phase == domain.PhaseComplete {
		if book == nil {
			h.lifecycle = lifecycleIdle
			h.mu.Unlock()
			h.emitEvent(Event{Time: time.Now(), Category: "ERROR", Level: "error",
				Summary: "Thông tin tác phẩm không tồn tại khi engine kết thúc"})
			select {
			case h.done <- struct{}{}:
			default:
			}
			return
		}
		h.lifecycle = lifecycleCompleted
		// Kết thúc hoàn sách: sinh deterministic (store đã có đủ dữ kiện, không tốn gọi LLM; mục cuối RFC).
		summary := completionSummary(*progress, *book)
		h.mu.Unlock()
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "success"})
		h.notifier.Send(notify.Notification{
			Kind: notify.KindRunEnd, Level: "info", Title: "ainovel: hoàn tất sáng tác",
			Body: h.runEndBody("", summary),
		})
	} else {
		wasRunning := h.lifecycle == lifecycleRunning
		if wasRunning {
			h.lifecycle = lifecycleIdle
		}
		completed := 0
		title := ""
		if progress != nil {
			completed = len(progress.CompletedChapters)
		}
		if book != nil {
			title = book.Title
		}
		h.mu.Unlock()
		if wasRunning {
			summary := fmt.Sprintf("Engine dừng (đã hoàn thành %d chương)", completed)
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: summary, Level: "warn"})
			h.notifier.Send(notify.Notification{
				Kind: notify.KindRunEnd, Level: "warn", Title: "ainovel: sáng tác dừng",
				Body: h.runEndBody(title, summary),
			})
		}
	}

	select {
	case h.done <- struct{}{}:
	default:
	}
}

// runEndBody lắp nội dung thông báo run_end: tên sách + tóm tắt tiến độ + chi phí tích lũy.
func (h *Host) runEndBody(title, summary string) string {
	if name := strings.TrimSpace(title); name != "" {
		summary = "《" + name + "》" + summary
	}
	cost, _, _, _, _ := h.usage.Totals()
	if cost > 0 {
		summary += fmt.Sprintf(" · chi phí $%.2f", cost)
	}
	return summary
}

// ── Kênh ──

// StreamClearSentinel gửi một bản tin qua streamCh để ám hiệu "xóa round streaming hiện tại".
// Không dùng clearCh riêng nữa — hai kênh không thứ tự khiến ✻ header thường rơi xuống cuối round trước.
const StreamClearSentinel = "\x00\x00CLEAR\x00\x00"

func (h *Host) Events() <-chan Event  { return h.events }
func (h *Host) Stream() <-chan string { return h.streamCh }
func (h *Host) Done() <-chan struct{} { return h.done }
func (h *Host) Dir() string           { return h.store.Dir() }

// ── Phát sự kiện ──

func (h *Host) emitEvent(ev Event) {
	h.outputMu.RLock()
	defer h.outputMu.RUnlock()
	if h.outputClosed {
		return
	}
	// Khóa đọc đảm bảo sự kiện trước khi đóng được ghi trọn vẹn; sự kiện sau khi đóng bị từ chối thẳng.
	LogEvent(ev)
	select {
	case h.events <- ev:
	default:
		select {
		case <-h.events:
		default:
		}
		select {
		case h.events <- ev:
		default:
		}
	}
}

func (h *Host) emitDelta(delta string) {
	h.outputMu.RLock()
	defer h.outputMu.RUnlock()
	if h.outputClosed {
		return
	}
	select {
	case h.streamCh <- delta:
	default:
		select {
		case <-h.streamCh:
		default:
		}
		select {
		case h.streamCh <- delta:
		default:
		}
	}
}

func (h *Host) closeOutputChannels() {
	h.outputMu.Lock()
	defer h.outputMu.Unlock()
	if h.outputClosed {
		return
	}
	h.outputClosed = true
	close(h.done)
	close(h.events)
	close(h.streamCh)
}

func (h *Host) emitClear() {
	// Đi "sentinel" qua streamCh, đảm bảo tới TUI đúng thứ tự trên cùng một kênh với emitDelta.
	h.emitDelta(StreamClearSentinel)
}

// ── Snapshot (tổng hợp trạng thái TUI) ──

func (h *Host) Snapshot() UISnapshot {
	h.mu.Lock()
	state := h.lifecycle
	provider, model, _ := h.models.CurrentSelection("default")
	modelWindow, _ := h.cfg.ResolveContextWindow(provider, model)
	thinkingLevel := h.cfg.ResolveReasoningEffort("default")
	style := h.cfg.Style
	h.mu.Unlock()

	// Động resolve cửa sổ ngữ cảnh của model hiện tại, sau khi /model hoặc /config chuyển thì Snapshot lần sau tự phản ánh.
	cost, tokIn, tokOut, cacheRead, cacheWrite := h.usage.Totals()
	saved := h.usage.SavedUSD()
	overallCapable := h.usage.OverallCacheCapable()
	recentRead, recentInput, recentSamples := h.usage.OverallRecent()
	perAgent := h.usage.PerAgent()
	cacheStats := make([]AgentCacheStat, 0, len(perAgent))
	for _, a := range perAgent {
		cacheStats = append(cacheStats, AgentCacheStat{
			Role:            a.Role,
			Input:           a.Input,
			Output:          a.Output,
			CacheRead:       a.CacheRead,
			CacheWrite:      a.CacheWrite,
			Cost:            a.Cost,
			Saved:           a.Saved,
			CacheCapable:    a.CacheCapable,
			RecentCacheRead: a.RecentCacheRead,
			RecentInput:     a.RecentInput,
			RecentSamples:   a.RecentSamples,
		})
	}
	perModel := h.usage.PerModel()
	modelStats := make([]AgentCacheStat, 0, len(perModel))
	for _, a := range perModel {
		modelStats = append(modelStats, AgentCacheStat{
			Model:        a.Model,
			Input:        a.Input,
			Output:       a.Output,
			CacheRead:    a.CacheRead,
			CacheWrite:   a.CacheWrite,
			Cost:         a.Cost,
			Saved:        a.Saved,
			CacheCapable: a.CacheCapable,
		})
	}

	snap := UISnapshot{
		Provider:               provider,
		ModelName:              model,
		ModelContextWindow:     modelWindow,
		ThinkingLevel:          thinkingLevel,
		Style:                  style,
		RuntimeState:           string(state),
		IsRunning:              state == lifecycleRunning,
		TotalInputTokens:       tokIn,
		TotalOutputTokens:      tokOut,
		TotalCacheReadTokens:   cacheRead,
		TotalCacheWriteTokens:  cacheWrite,
		TotalCostUSD:           cost,
		TotalSavedUSD:          saved,
		BudgetLimitUSD:         h.budget.Limit(),
		OverallCacheCapable:    overallCapable,
		OverallRecentCacheRead: recentRead,
		OverallRecentInput:     recentInput,
		OverallRecentSamples:   recentSamples,
		TotalCacheBreaks:       h.usage.OverallCacheBreaks(),
		CachePerAgent:          cacheStats,
		CachePerModel:          modelStats,
		MissingAssistantUsage:  h.usage.MissingAssistantUsage(),
	}

	if book, _ := h.store.Book.Load(); book != nil {
		snap.BookTitle = book.Title
		snap.Synopsis = truncate(book.Synopsis, 200)
	}
	progress, _ := h.store.Progress.Load()
	if progress != nil {
		snap.Phase = string(progress.Phase)
		snap.Flow = string(progress.Flow)
		snap.CurrentChapter = progress.CurrentChapter
		snap.TotalChapters = progress.TotalChapters
		snap.CompletedCount = len(progress.CompletedChapters)
		snap.TotalWordCount = progress.TotalWordCount
		snap.InProgressChapter = progress.InProgressChapter
		snap.PendingRewrites = progress.PendingRewrites
		snap.RewriteReason = progress.RewriteReason
		snap.Layered = progress.Layered
		if progress.CurrentVolume > 0 {
			snap.CurrentVolumeArc = fmt.Sprintf("Quyển %d · Cung %d", progress.CurrentVolume, progress.CurrentArc)
		}
	}
	if meta, _ := h.store.RunMeta.Load(); meta != nil {
		snap.PendingSteer = meta.PendingSteer
		snap.AdvanceMode = string(meta.AdvanceMode)
		snap.AdvancePermitChapter = meta.AdvancePermitChapter
		if meta.AdvanceHold != nil {
			snap.HasAdvanceHold = true
			snap.AdvanceHoldReason = meta.AdvanceHold.Reason
		}
	}

	snap.Agents = h.observer.agentSnapshots()
	snap.StatusLabel = deriveStatusLabel(snap)

	// Nhãn khôi phục
	if label, err := resumeLabel(h.store); err == nil && label != "" {
		snap.RecoveryLabel = label
	}

	h.fillDetails(&snap, progress)

	return snap
}

// fillDetails đổ vùng chi tiết: thiết lập, nhân vật, commit/review/tóm tắt gần nhất.
func (h *Host) fillDetails(snap *UISnapshot, progress *domain.Progress) {
	if premise, _ := h.store.Outline.LoadPremise(); premise != "" {
		snap.Premise = truncate(premise, 80)
	}
	if outline, _ := h.store.Outline.LoadOutline(); len(outline) > 0 {
		completed := make(map[int]struct{})
		if progress != nil {
			completed = make(map[int]struct{}, len(progress.CompletedChapters))
			for _, chapter := range progress.CompletedChapters {
				completed[chapter] = struct{}{}
			}
		}
		for _, e := range outline {
			title := e.Title
			if _, ok := completed[e.Chapter]; ok {
				committedTitle, err := h.store.Summaries.LoadSummaryTitle(e.Chapter)
				if err != nil {
					slog.Warn("Chiếu tiêu đề chương thất bại", "module", "host.snapshot", "chapter", e.Chapter, "err", err)
				} else if strings.TrimSpace(committedTitle) != "" {
					title = committedTitle
				}
			}
			snap.Outline = append(snap.Outline, OutlineSnapshot{
				Chapter: e.Chapter, Title: title, CoreEvent: e.CoreEvent,
			})
		}
	}
	if progress != nil && progress.Layered {
		if compass, _ := h.store.Outline.LoadCompass(); compass != nil {
			snap.CompassDirection = compass.EndingDirection
			snap.CompassScale = compass.EstimatedScale
		}
		if volumes, _ := h.store.Outline.LoadLayeredOutline(); len(volumes) > 0 {
			for _, v := range volumes {
				if v.Index > progress.CurrentVolume {
					snap.NextVolumeTitle = v.Title
					break
				}
			}
		}
	}
	if chars, _ := h.store.Characters.Load(); len(chars) > 0 {
		for _, c := range chars {
			label := c.Name
			if c.Role != "" {
				label += "（" + c.Role + "）"
			}
			snap.Characters = append(snap.Characters, label)
		}
	}
	if ledger, _ := h.store.Cast.Load(); len(ledger) > 0 {
		snap.SupportingCount = len(ledger)
		recent, _ := h.store.Cast.RecentActive(5)
		for _, e := range recent {
			label := e.Name
			if e.BriefRole != "" {
				label += "（" + e.BriefRole + "）"
			}
			snap.RecentSupporting = append(snap.RecentSupporting, label)
		}
	}
	if progress != nil && len(progress.CompletedChapters) > 0 {
		lastCh := progress.CompletedChapters[len(progress.CompletedChapters)-1]
		wc := progress.ChapterWordCounts[lastCh]
		snap.LastCommitSummary = fmt.Sprintf("Chương %d · %d chữ", lastCh, wc)
	}
	currentCh := 1
	if progress != nil && len(progress.CompletedChapters) > 0 {
		currentCh = progress.CompletedChapters[len(progress.CompletedChapters)-1]
	}
	if review, err := h.store.World.LoadLastReview(currentCh); err == nil && review != nil {
		snap.LastReviewSummary = fmt.Sprintf("verdict=%s %d vấn đề", review.Verdict, len(review.Issues))
		if len(review.AffectedChapters) > 0 {
			snap.LastReviewSummary += fmt.Sprintf(" ảnh hưởng %v", review.AffectedChapters)
		}
	}
	if cp := h.store.Checkpoints.LatestGlobal(); cp != nil {
		snap.LastCheckpointName = fmt.Sprintf("%s.%s", cp.Scope, cp.Step)
	}
	if progress != nil {
		for i := len(progress.CompletedChapters) - 1; i >= 0 && len(snap.RecentSummaries) < 2; i-- {
			ch := progress.CompletedChapters[i]
			if summary, err := h.store.Summaries.LoadSummary(ch); err == nil && summary != nil {
				snap.RecentSummaries = append(snap.RecentSummaries,
					fmt.Sprintf("Chương %d: %s", ch, truncate(summary.Summary, 50)))
			}
		}
	}
}

func deriveStatusLabel(s UISnapshot) string {
	switch {
	case s.Phase == string(domain.PhaseComplete):
		return "COMPLETE"
	case s.Flow == string(domain.FlowReviewing):
		return "REVIEW"
	case s.Flow == string(domain.FlowRewriting) || s.Flow == string(domain.FlowPolishing):
		return "REWRITE"
	case s.RuntimeState == "running":
		return "RUNNING"
	default:
		return "READY"
	}
}

// ── Quản lý model ──

func (h *Host) ConfiguredProviders() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	providers := make([]string, 0, len(h.cfg.Providers))
	for name := range h.cfg.Providers {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	return providers
}

func (h *Host) ConfiguredModels(provider string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.CandidateModels(provider)
}

func (h *Host) CurrentModelSelection(role string) (string, string, bool) {
	return h.models.CurrentSelection(role)
}

func (h *Host) SwitchModel(role, provider, model string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if provider == "" || model == "" {
		return fmt.Errorf("provider and model are required")
	}
	if err := h.models.Swap(role, provider, model); err != nil {
		return err
	}
	if role == "" || role == "default" {
		h.cfg.Provider = provider
		h.cfg.ModelName = model
	} else {
		if h.cfg.Roles == nil {
			h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rc := h.cfg.Roles[role]
		rc.Provider = provider
		rc.Model = model
		h.cfg.Roles[role] = rc
	}
	// Đổi model không đụng ý định mức suy luận đã lưu: chỉ kẹp theo năng lực model mới lúc phát xuống.
	if h.configPath != "" {
		if err := bootstrap.SaveConfig(h.configPath, h.cfg); err != nil {
			slog.Warn("Lưu cấu hình thất bại", "module", "host", "err", err)
		}
	}
	h.applyThinkingLocked(role)
	// Khi chuyển sang model chưa đăng ký thì in một dòng warn, nhắc người dùng đang đi fallback 128k — truyện dài dễ bị nén sớm.
	logRole := role
	if logRole == "" {
		logRole = "default"
	}
	window, source := h.cfg.ResolveContextWindow(provider, model)
	bootstrap.LogContextWindowChoice(logRole, model, window, source)

	// Không có ngữ cảnh thường trú nào cần liên đới: ContextManager của writer/architect/editor đi qua
	// ContextManagerFactory, lần spawn sau tự dựng lại theo cửa sổ model mới.

	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("Model đã chuyển: %s → %s/%s", role, provider, model),
		Level:    "info",
	})
	return nil
}

// concreteThinkingRoles là các role cụ thể có thể áp mức suy luận (phù hợp định tuyến agents.ApplyThinking).
// Khi gọi default thì áp lại từng role theo ResolveReasoningEffort.
var concreteThinkingRoles = []string{"architect", "writer", "editor"}

// CurrentThinking trả chuỗi gốc mức suy luận đang hiệu lực của một role (cho panel /model đồng bộ giá trị hiện tại).
func (h *Host) CurrentThinking(role string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg.ResolveReasoningEffort(strings.ToLower(strings.TrimSpace(role)))
}

func (h *Host) AvailableThinking(role string) []agentcore.ThinkingLevel {
	h.mu.Lock()
	model := h.models.ForRole(strings.ToLower(strings.TrimSpace(role)))
	h.mu.Unlock()
	return agents.AvailableThinkingForModel(model)
}

// resolveThinkingForRoleLocked tính mức suy luận thực tế hiệu lực của một role: lấy ý định gốc
// (ResolveReasoningEffort: cấp role → mặc định tầng trên), rồi kẹp theo năng lực model hiện tại của role đó.
// Việc kẹp chỉ xảy ra trên "đường hiệu lực" này, không ghi ngược config — lưu trữ luôn giữ ý định gốc của người dùng.
func (h *Host) resolveThinkingForRoleLocked(role string) agentcore.ThinkingLevel {
	parsed, _ := agents.ParseThinkingLevel(h.cfg.ResolveReasoningEffort(role))
	resolved, _ := agents.ResolveThinkingForModel(h.models.ForRole(role), parsed)
	return resolved
}

// applyThinkingLocked phát mức hiệu lực xuống live agent; mỗi role kẹp theo model riêng của mình.
func (h *Host) applyThinkingLocked(role string) {
	if h.thinkingApplier == nil {
		return
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "default" {
		for _, r := range concreteThinkingRoles {
			h.thinkingApplier(r, h.resolveThinkingForRoleLocked(r))
		}
		return
	}
	h.thinkingApplier(role, h.resolveThinkingForRoleLocked(role))
}

// SetRoleThinking đặt mức suy luận của một role (hoặc default): kiểm tra→persist→liên đới live agent→sự kiện.
// Phản chiếu cấu trúc SwitchModel; trực giao với lựa chọn model, chỉnh riêng được. level rỗng = không ghi đè (kế thừa).
func (h *Host) SetRoleThinking(role, level string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	parsed, err := agents.ParseThinkingLevel(level)
	if err != nil {
		return err
	}
	role = strings.ToLower(strings.TrimSpace(role))
	// Lưu trữ giữ ý định gốc: persist thẳng mức người dùng chọn, việc kẹp chỉ xảy ra lúc phát xuống (applyThinkingLocked) theo năng lực model.
	if role == "" || role == "default" {
		h.cfg.ReasoningEffort = string(parsed)
	} else {
		if h.cfg.Roles == nil {
			h.cfg.Roles = make(map[string]bootstrap.RoleConfig)
		}
		rc := h.cfg.Roles[role]
		rc.ReasoningEffort = string(parsed)
		h.cfg.Roles[role] = rc
	}
	if h.configPath != "" {
		if err := bootstrap.SaveConfig(h.configPath, h.cfg); err != nil {
			slog.Warn("Lưu cấu hình thất bại", "module", "host", "err", err)
		}
	}

	// Liên đới live: role cụ thể áp trực tiếp; default thì duyệt các role cụ thể áp lại theo ResolveReasoningEffort
	// (role nào đã bị ghi đè cấp role thì giữ nguyên, role chưa ghi đè thì nhận mặc định mới).
	h.applyThinkingLocked(role)

	logRole := role
	if logRole == "" {
		logRole = "default"
	}
	shown := string(parsed)
	if shown == "" {
		shown = "mặc định (kế thừa)"
	}
	h.emitEvent(Event{
		Time:     time.Now(),
		Category: "SYSTEM",
		Summary:  fmt.Sprintf("Mức suy luận đã chuyển: %s → %s", logRole, shown),
		Level:    "info",
	})
	return nil
}

// ── Replay sự kiện ──

func (h *Host) ReplayQueue(afterSeq int64) ([]domain.RuntimeQueueItem, error) {
	if h.store == nil || h.store.Runtime == nil {
		return nil, nil
	}
	return h.store.Runtime.LoadQueueAfter(afterSeq)
}

// ── Đồng sáng tạo ──

// CoCreateStream đồng sáng tạo khởi động nguội: làm rõ nhu cầu từ số 0, sinh chỉ lệnh sáng tác cho cả cuốn sách.
func (h *Host) CoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	return coCreateStream(ctx, h.models, h.store.Sessions, coCreateSystemPrompt, history, onProgress)
}

// StageCoCreateStream đồng sáng tạo theo giai đoạn: lập hướng đi tiếp theo trên nền nội dung đã viết.
// System prompt = prompt giai đoạn + tóm tắt trạng thái truyện hiện tại, để trợ lý biết "đã viết tới đâu".
func (h *Host) StageCoCreateStream(ctx context.Context, history []CoCreateMessage, onProgress func(kind, text string)) (CoCreateReply, error) {
	return coCreateStream(ctx, h.models, h.store.Sessions, stageSystemPrompt(h.store), history, onProgress)
}

// stagePlanPrefix bọc "brief hướng đi tiếp theo" do đồng sáng tạo tạo ra thành một can thiệp lập kế hoạch giai đoạn, giao Arbiter phán định.
// Chỉ dán nhãn dữ kiện [lập kế hoạch giai đoạn] + trình bày trung tính, không cố định "thế nào triển khai" — định tuyến cụ thể (compass / architect /
// user_rules) giao cho tiêu chí "lập kế hoạch giai đoạn" của arbiter-intervention.md, tránh tạo nguồn sự thật thứ hai với prompt,
// cũng không chặn yêu cầu kiểu văn phong đi user_rules (giữ "phán định phân loại thuộc về LLM"). Continue cộng thêm tiền tố [can thiệp người dùng].
const stagePlanPrefix = "[Lập kế hoạch giai đoạn] Tôi đã tạm dừng sáng tác, cùng trợ lý đồng sáng tạo sắp xếp hướng đi tiếp theo bên dưới; hãy theo phân loại can thiệp của bạn phán định cách triển khai, rồi tiếp tục sáng tác. Hướng đi tiếp theo như sau:\n\n"

// PauseForCoCreate vào đồng sáng tạo theo giai đoạn: đặt cờ chiếm dụng đồng sáng tạo, đang chạy thì tạm dừng Engine luôn.
// Trả false nghĩa là không vào được (sách đã hoàn tất hoặc đang đồng sáng tạo), bên gọi cứ bỏ qua.
// Cờ chiếm dụng chặn import/simulate/start/resume/continue can thiệp đồng thời trong cửa sổ đồng sáng tạo —
// sau khi tạm dừng lúc đang chạy lifecycle=paused, mutual exclusion ==running hiện có mất tác dụng, dựa vào cờ này bù khuyết;
// đã dừng (idle/paused) cũng cho vào, lập kế hoạch xong chạy tiếp qua Continue.
func (h *Host) PauseForCoCreate() bool {
	h.mu.Lock()
	if h.cocreating || h.lifecycle == lifecycleCompleted {
		h.mu.Unlock()
		return false
	}
	h.cocreating = true
	running := h.lifecycle == lifecycleRunning
	h.mu.Unlock()

	// Đang chạy thì tái dùng abortWithEvent dừng máy (running→paused + setAborting + Abort + sự kiện), cùng thứ tự với
	// tạm dừng thủ công, không copy thêm một lần; đã dừng (idle/paused) thì chỉ đặt cờ, lập kế hoạch xong chạy tiếp qua Continue.
	if running {
		h.abortWithEvent("Vào đồng sáng tạo theo giai đoạn, sáng tác đã tạm dừng", "info")
	} else {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Vào đồng sáng tạo theo giai đoạn", Level: "info"})
	}
	return true
}

// ResumeFromCoCreate kết thúc đồng sáng tạo theo giai đoạn: tiêm hướng đi tiếp theo do đồng sáng tạo tạo ra vào như can thiệp rồi khôi phục sáng tác.
// Sau khi dọn cờ chiếm dụng tái dùng đường tiêm lúc dừng máy của Continue (chịu ràng buộc kiểm tra ngân sách trước).
// Ghi chú: draft rỗng thì trả về sớm, không dọn cờ là cố ý (đồng sáng tạo chưa kết thúc); guard canStart() phía TUI
// cùng dùng tiêu chí "khác rỗng" với đây, đảm bảo đường này không tới được, cocreating không leak vì thế.
func (h *Host) ResumeFromCoCreate(draft string) error {
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return fmt.Errorf("draft is required")
	}
	h.mu.Lock()
	if !h.cocreating {
		h.mu.Unlock()
		return fmt.Errorf("not in co-create")
	}
	h.cocreating = false
	h.mu.Unlock()

	// abort của PauseForCoCreate là async: đợi vòng lặp engine thật sự hội tụ rồi mới tiếp tục, trở về tiền đề
	// "dừng máy thật" nhất quán với Continue sau tạm dừng thủ công. Cửa sổ đồng sáng tạo ở thang thời gian tương tác người-máy, polling ngắn không cảm nhận được.
	for h.engine.isRunning() {
		time.Sleep(20 * time.Millisecond)
	}

	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Đồng sáng tạo theo giai đoạn hoàn tất, đã tiêm hướng đi tiếp theo và khôi phục sáng tác", Level: "info"})
	return h.Continue(stagePlanPrefix + draft)
}

// CancelCoCreate bỏ đồng sáng tạo theo giai đoạn: dọn cờ chiếm dụng, giữ trạng thái tạm dừng (người dùng có thể tiếp tục trong ô nhập liệu hoặc khởi động lại Resume).
func (h *Host) CancelCoCreate() {
	h.mu.Lock()
	if !h.cocreating {
		h.mu.Unlock()
		return
	}
	h.cocreating = false
	h.mu.Unlock()
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Summary: "Đã thoát đồng sáng tạo theo giai đoạn, sáng tác giữ tạm dừng (có thể tiếp tục trong ô nhập liệu)", Level: "info"})
}

// ── Công cụ ──

func (h *Host) refreshWriterRestore() {
	if h.writerRestore != nil {
		h.writerRestore.Refresh(h.store)
	}
}

func (h *Host) CheckChapterRevisions() ([]int, error) {
	pending, err := h.store.Revisions.LoadPending()
	if err != nil {
		return nil, fmt.Errorf("Đọc bản ghi khôi phục chỉnh sửa: %w", err)
	}
	if pending != nil {
		chapters := make([]int, 0, len(pending.Items))
		for _, item := range pending.Items {
			chapters = append(chapters, item.Chapter)
		}
		return chapters, nil
	}
	changes, err := revision.Scan(h.store)
	if err != nil {
		return nil, err
	}
	return revision.ChangedChapters(changes), nil
}

func (h *Host) SyncChapterRevisions(ctx context.Context) (*revision.Result, error) {
	if err := h.acquireExclusive("đồng bộ chỉnh sửa chương"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.exclusiveCancel = cancel
	h.mu.Unlock()
	defer h.releaseExclusive()

	pending, err := h.store.Revisions.LoadPending()
	if err != nil {
		return nil, err
	}
	if pending == nil {
		changes, err := revision.Scan(h.store)
		if err != nil {
			return nil, err
		}
		if len(changes) == 0 {
			return &revision.Result{}, nil
		}
		if err := h.budget.Refuse(); err != nil {
			return nil, err
		}
	}
	model := h.models.ForRoleWithFailover("editor", func(ev bootstrap.FailoverEvent) {
		slog.Warn("Chuyển provider khi chỉnh sửa chương", "module", "revision", "role", ev.Role,
			"reason", ev.Reason, "from", fmt.Sprintf("%s/%s", ev.FromProvider, ev.FromModel),
			"to", fmt.Sprintf("%s/%s", ev.ToProvider, ev.ToModel), "err", ev.Err)
	})
	model = newUsageTrackedModel(model, "editor", h.usage.Record)
	service := revision.NewService(h.store, model, h.bundle.Prompts.RevisionAnalyze, h.styleStats)
	return service.Sync(ctx)
}

func (h *Host) requireCleanChapters() error {
	chapters, err := h.CheckChapterRevisions()
	if err != nil {
		return fmt.Errorf("Kiểm tra chỉnh sửa chương từ bên ngoài: %w", err)
	}
	if len(chapters) > 0 {
		return fmt.Errorf("Phát hiện phần thân chương đã bị sửa từ bên ngoài: %v; vui lòng chạy /sync trước", chapters)
	}
	return nil
}

func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

// ImportFrom khởi động một lần nhập biên dịch ngữ nghĩa tiểu thuyết bên ngoài: ingest → segment → analyze → synthesize → publish.
// Model chỉ phán định ngữ nghĩa mở (ranh giới/dữ kiện/tổng hợp), Go quản tọa độ/ghi đè/idempotent; loại trừ lẫn nhau với Engine đang chạy,
// sau khi nhập xong do AdvanceHold quyết định có viết tiếp không.
// Kênh sự kiện trả về do imp.Run đóng, bên gọi chịu trách nhiệm tiêu thụ (đầy thì bỏ để tránh chặn goroutine pipeline).
func (h *Host) ImportFrom(ctx context.Context, opts imp.Options) (<-chan imp.Event, error) {
	// Kiểm tra ngân sách trước khởi động cùng kỷ luật với Start/Resume/Continue: nhập là gọi model toàn quy trình,
	// ngân sách đã vượt thì không được khởi động (§13.1 "đưa vào sentinel ngân sách hiện có").
	if err := h.budget.Refuse(); err != nil {
		return nil, err
	}
	if err := h.acquireExclusive("nhập truyện"); err != nil {
		return nil, err
	}
	// Đăng ký hàm cancel: dừng cứng ngân sách/tạm dừng thủ công qua abortWithEvent cancel context của chính việc nhập
	// (nếu không sentinel chỉ biết tạm dừng Engine vốn chưa chạy, việc nhập tiếp tục đốt tiền).
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.exclusiveCancel = cancel
	h.mu.Unlock()

	deps := imp.Deps{
		Store:         h.store,
		CommitChapter: tools.NewCommitChapterTool(h.store, h.styleStats),
		Segment:       h.importCaller("segment"),
		Analyze:       h.importCaller("analyze"),
		Synthesize:    h.importCaller("synthesize"),
		Prompts: imp.Prompts{
			Segment:    h.bundle.Prompts.ImportSegment,
			Analyze:    h.bundle.Prompts.ImportAnalyze,
			Synthesize: h.bundle.Prompts.ImportSynthesize,
			Range:      h.bundle.Prompts.ImportRange,
		},
	}
	ch, err := imp.Run(ctx, deps, opts)
	if err != nil {
		h.releaseExclusive()
		return nil, err
	}
	return h.superviseImport(ch, opts), nil
}

// ImportResumeHint trả một dòng nhắc việc nhập chưa hoàn tất (không có thì chuỗi rỗng), cho TUI chủ động báo lúc khởi động (RFC §18.2).
// Chỉ gọi một lần lúc khởi động: bên trong sẽ tính lại InputDigest của từng artifact trong workspace, không hợp để bỏ vào polling snapshot.
func (h *Host) ImportResumeHint() string {
	return imp.ResumeSummary(h.store)
}

// importCaller resolve phân khúc model cho một hàm ngữ nghĩa nhập (RFC §13.1): cấu hình roles có import_<fn>
// thì dùng phân khúc đó (lượng dùng cũng ghi vào sổ role đó), không thì rơi về architect. Đây là cấu hình gọi, không đổi hợp đồng ngữ nghĩa nào.
func (h *Host) importCaller(fn string) imp.Caller {
	role := "import_" + fn
	if _, _, explicit := h.models.CurrentSelection(role); !explicit {
		role = "architect"
	}
	model := h.models.ForRoleWithFailover(role, func(ev bootstrap.FailoverEvent) {
		slog.Warn("Chuyển provider khi nhập", "module", "import", "role", ev.Role,
			"reason", ev.Reason,
			"from", fmt.Sprintf("%s/%s", ev.FromProvider, ev.FromModel),
			"to", fmt.Sprintf("%s/%s", ev.ToProvider, ev.ToModel),
			"err", ev.Err)
	})
	model = newUsageTrackedModel(model, role, h.usage.Record)
	return imp.Caller{Model: model, Runtime: h.importModelRuntime(role, model)}
}

// importModelRuntime dò năng lực gọi của model role phân khúc đã chọn, cho imp dùng thích ứng hai lớp ngân sách / thinking (RFC §13/§21).
// Trường dò thất bại giữ giá trị 0, phía imp fallback về mặc định bảo thủ, đảm bảo chạy đúng cả khi không có thông tin năng lực.
// Đầu ra có cấu trúc do llmcontract của imp đọc dữ kiện model ngay trước mỗi yêu cầu, không cache lại trong Runtime.
func (h *Host) importModelRuntime(role string, model agentcore.ChatModel) imp.ModelRuntime {
	var rt imp.ModelRuntime
	provider, name, _ := h.models.CurrentSelection(role)
	if name == "" {
		name = bootstrap.ModelName(model)
		provider = bootstrap.ModelProvider(model)
	}
	// Giới hạn context / completion: registry là nguồn đáng tin duy nhất (Info() của model đã bọc không chứa cửa sổ).
	rt.ContextTokens, _ = h.cfg.ResolveContextWindow(provider, name)
	if entry, ok := modelreg.DefaultRegistry().Resolve(name); ok {
		rt.MaxOutputTokens = entry.MaxTokens
	}
	// thinking: resolve theo reasoning effort của role và năng lực model; không hỗ trợ thì không gửi (cùng chiến lược với arbiter).
	if level, err := agents.ParseThinkingLevel(h.cfg.ResolveReasoningEffort(role)); err == nil {
		if resolved, ok := agents.ResolveThinkingForModel(model, level); ok {
			rt.Thinking = resolved
		}
	}
	return rt
}

// Simulate đọc thư mục simulate và sinh hoặc cập nhật tăng dần hồ sơ mô phỏng văn phong.
func (h *Host) Simulate(ctx context.Context) (<-chan sim.Event, error) {
	if err := h.acquireExclusive("sinh hồ sơ mô phỏng văn phong"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.exclusiveCancel = cancel
	h.mu.Unlock()

	wd, err := os.Getwd()
	if err != nil {
		h.releaseExclusive()
		return nil, fmt.Errorf("get working dir: %w", err)
	}
	deps := sim.Deps{
		Store: h.store,
		LLM:   h.models.ForRole("architect"),
		Prompts: sim.Prompts{
			Source: h.bundle.Prompts.SimulationSource,
			Merge:  h.bundle.Prompts.SimulationMerge,
		},
	}
	ch, err := sim.Run(ctx, deps, sim.Options{SourceDir: filepath.Join(wd, "simulate")})
	if err != nil {
		h.releaseExclusive()
		return nil, err
	}
	return superviseExclusive(h, ch), nil
}

// ImportSimulationProfile nhập hồ sơ mô phỏng văn phong đã sinh trước đó.
func (h *Host) ImportSimulationProfile(ctx context.Context, path string) (<-chan sim.Event, error) {
	if err := h.acquireExclusive("nhập hồ sơ mô phỏng văn phong"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.exclusiveCancel = cancel
	h.mu.Unlock()
	ch, err := sim.RunImport(ctx, h.store, path)
	if err != nil {
		h.releaseExclusive()
		return nil, err
	}
	return superviseExclusive(h, ch), nil
}

// acquireExclusive chiếm dụng nguyên tử slot tác vụ độc chiếm nền (import/simulate/revision): Engine đang chạy, trong cửa sổ đồng sáng tạo giai đoạn,
// hoặc đã có tác vụ độc chiếm đang chạy thì từ chối. Thành công là đăng ký chiếm dụng, tác vụ kết thúc phải gọi releaseExclusive để nhả — nếu không hai việc nhập
// hoặc nhập+mô phỏng văn phong sẽ tranh nhau sửa cùng trạng thái đồng thời. Bù khuyết điểm trước đây chỉ kiểm ==running/cocreating, không đăng ký bản thân tác vụ.
func (h *Host) acquireExclusive(action string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.closing:
		return fmt.Errorf("Host đang đóng, không thể %s", action)
	// engine.isRunning() phải kiểm: Abort đặt lifecycle=paused trước rồi mới async đợi goroutine thoát,
	// trong cửa sổ đó lifecycle đã không còn running nhưng engine vẫn có thể đang ghi store (cùng kỷ luật với cổng khởi động).
	case h.lifecycle == lifecycleRunning || h.engine.isRunning():
		return fmt.Errorf("Engine sáng tác đang chạy hoặc đang dừng, vui lòng %s sau ít lâu", action)
	case h.cocreating:
		return fmt.Errorf("Đồng sáng tạo theo giai đoạn đang diễn ra, vui lòng kết thúc đồng sáng tạo trước khi %s", action)
	case h.exclusive != "":
		return fmt.Errorf("%s đang diễn ra, vui lòng hoàn tất trước khi %s", h.exclusive, action)
	}
	h.exclusive = action
	return nil
}

// releaseExclusive nhả slot tác vụ độc chiếm nền (cả hàm cancel đã đăng ký).
func (h *Host) releaseExclusive() {
	h.mu.Lock()
	cancel := h.exclusiveCancel
	h.exclusive = ""
	h.exclusiveCancel = nil
	h.mu.Unlock()
	if cancel != nil {
		cancel() // tác vụ đã kết thúc: nhả context dẫn xuất; không side effect với runner đã thoát
	}
}

// superviseExclusive chuyển tiếp sự kiện tác vụ độc chiếm, khi kênh đóng (tác vụ kết thúc) thì nhả slot chiếm dụng.
func superviseExclusive[T any](h *Host, src <-chan T) <-chan T {
	out := make(chan T, 32)
	if !h.launchAsync(func() {
		defer close(out)
		defer h.releaseExclusive()
		for ev := range src {
			select {
			case out <- ev:
			case <-h.runCtx.Done():
				// Trong lúc đóng tiếp tục xả hết kênh nguồn, tránh producer bị chặn vì sự kiện trạng thái cuối mà không thoát được.
				for range src {
				}
				return
			}
		}
	}) {
		close(out)
		h.releaseExclusive()
	}
	return out
}

// superviseImport là chủ sở hữu duy nhất của "sau khi nhập xong có tiếp sức hay không": chuyển tiếp sự kiện nhập, hoàn tất thành công thì nhả slot độc chiếm trước,
// rồi quyết định và thực thi tiếp sức, cuối cùng ghi kết quả tiếp sức thật vào trường Continued của sự kiện StageDone. TUI chỉ render theo đó,
// không còn dùng cờ --continue local phỏng đoán trạng thái chạy (loại bỏ race thời gian do ba bên Runner/Host/TUI diễn giải riêng).
func (h *Host) superviseImport(src <-chan imp.Event, opts imp.Options) <-chan imp.Event {
	out := make(chan imp.Event, 32)
	if !h.launchAsync(func() {
		defer close(out)
		released := false
		release := func() {
			if !released {
				released = true
				h.releaseExclusive()
			}
		}
		defer release()
		for ev := range src {
			if ev.Stage == imp.StageDone {
				release() // nhả slot độc chiếm trước, startEngine của tiếp sức mới qua được cổng độc chiếm
				ev.Continued = h.continueAfterImport(opts)
			}
			select {
			case out <- ev:
			case <-h.runCtx.Done():
				for range src {
				}
				return
			}
		}
	}) {
		close(out)
		h.releaseExclusive()
	}
	return out
}

// launchAsync đăng ký một tác vụ nền trong vòng đời Host. closing và WaitGroup.Add chịu cùng
// một khóa bảo vệ, đảm bảo sau khi Close bắt đầu Wait sẽ không còn Add mới xuất hiện.
func (h *Host) launchAsync(fn func()) bool {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return false
	}
	h.asyncWG.Add(1)
	h.mu.Unlock()
	go func() {
		defer h.asyncWG.Done()
		fn()
	}()
	return true
}

// runAsync tái dùng đăng ký tác vụ nền sẵn có của Host, đồng thời trả lỗi nghiệp vụ về cho bên gọi.
func (h *Host) runAsync(fn func() error) (error, bool) {
	result := make(chan error, 1)
	if !h.launchAsync(func() { result <- fn() }) {
		return nil, false
	}
	return <-result, true
}

// continueAfterImport quyết định và thực thi tự động tiếp sức thật sự của --continue, trả về Engine đã khởi động hay chưa.
// Ý định tiếp sức hợp lệ = opts lần này hoặc intent persist trong workspace (phủ cả tình huống khôi phục /import không tham số sau crash);
// chỉ chế độ tiến trình auto mới tiếp sức, do lập kế hoạch mở rộng cung thích ứng tiếp chuyện mở, hoặc cho chuyện đã hoàn thành khép lại; review giao người dùng /next.
func (h *Host) continueAfterImport(opts imp.Options) bool {
	want := opts.ContinueAfter
	if !want {
		in, err := imp.OpenWorkspace(h.store.Dir()).LoadIntent()
		if err != nil {
			h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
				Summary: "Nhập đã hoàn tất, nhưng đọc ý định tự động tiếp sức thất bại: " + err.Error()})
		} else if in != nil {
			want = in.ContinueAfterImport
		}
	}
	if !want {
		return false
	}
	meta, err := h.store.RunMeta.Load()
	if err != nil || meta == nil {
		slog.Warn("Đọc RunMeta cho tự động tiếp sức sau nhập thất bại", "module", "host", "err", err)
		return false
	}
	if meta.AdvanceMode != domain.ChapterAdvanceAuto {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "info",
			Summary: "Nhập hoàn tất; hiện là chế độ nghiệm thu từng chương, nhập tiếp tục hoặc /next để nối tiếp viết"})
		return false
	}
	h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "info", Summary: "Nhập hoàn tất, tự động tiếp sức viết tiếp"})
	if !h.startEngine(nil) {
		h.emitEvent(Event{Time: time.Now(), Category: "SYSTEM", Level: "warn",
			Summary: "Khởi động tự động tiếp sức thất bại, vui lòng nhập lệnh tiếp tục để khôi phục thủ công"})
		return false
	}
	return true
}

// Export xuất chương đã hoàn thành ra file bên ngoài (hiện chỉ hỗ trợ TXT).
//
// Khác với ImportFrom: xuất là thao tác chỉ đọc (không đụng Progress / Checkpoint),
// nên **không yêu cầu Engine dừng máy** — giữa lúc viết cũng có thể xuất "sản phẩm giai đoạn hiện tại" bất cứ lúc nào.
// Chỉ đọc snapshot nhất quán của Progress.CompletedChapters + bản cuối chương + dàn ý + premise.
func (h *Host) Export(ctx context.Context, opts exp.Options) (*exp.Result, error) {
	return exp.Run(ctx, exp.Deps{Store: h.store}, opts)
}
