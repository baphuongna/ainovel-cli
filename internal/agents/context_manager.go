package agents

import (
	"log/slog"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

// contextManagerConfig tổng hợp toàn bộ tham số cấu hình của ContextManager.
type contextManagerConfig struct {
	Model            agentcore.ChatModel
	ContextWindow    int
	ReserveTokens    int
	Agent            string
	CommitProjected  bool
	Summary          *corecontext.FullSummaryConfig
	ToolMicrocompact *corecontext.ToolResultMicrocompactConfig
	ExtraStrategies  []corecontext.Strategy
}

func newContextManager(cfg contextManagerConfig) *corecontext.ContextEngine {
	var sc corecontext.FullSummaryConfig
	if cfg.Summary != nil {
		sc = *cfg.Summary
	}
	sc.Model = cfg.Model

	var tc corecontext.ToolResultMicrocompactConfig
	if cfg.ToolMicrocompact != nil {
		tc = *cfg.ToolMicrocompact
	}

	strategies := []corecontext.Strategy{
		corecontext.NewToolResultMicrocompact(tc),
	}
	strategies = append(strategies, cfg.ExtraStrategies...)
	// FullSummary chỉ lắp khi có cấu hình tóm tắt: caller không cấp Summary
	// (chỉ cần microcompact) thì không gọi LLM tóm tắt, tránh prompt mặc định
	// code-assistant của agentcore áp cho agent không phù hợp.
	if cfg.Summary != nil {
		strategies = append(strategies, corecontext.NewFullSummary(sc))
	}

	var commitStrategies []string
	if cfg.CommitProjected {
		commitStrategies = make([]string, len(strategies))
		for i, strategy := range strategies {
			commitStrategies[i] = strategy.Name()
		}
	}

	engine := corecontext.NewEngine(corecontext.EngineConfig{
		ContextWindow:    cfg.ContextWindow,
		ReserveTokens:    cfg.ReserveTokens,
		CommitStrategies: commitStrategies,
		Strategies:       strategies,
	})

	callback := contextRewriteCallback(cfg.Agent)
	engine.SetProjectHook(callback)
	engine.SetRecoverHook(callback)
	return engine
}

// NewAgentContextManager dựng ContextEngine chuẩn cho agent non-writer
// (architect_short/architect_long/editor): ToolResultMicrocompact luôn bật,
// FullSummary và ExtraStrategies tùy chọn (nil-safe; summaryCfg nil = không
// lắp FullSummary). Khác Writer, không có StoreSummaryCompact hay restore hook —
// architect/editor tái lập dữ liệu qua tool novel_context (store-first).
// Cửa sổ resolve động qua ModelSet theo model hiện tại, nên phải gọi bên trong
// ContextManagerFactory: mỗi lần spawn tự tái tạo theo model sau /model swap,
// không capture window tĩnh lúc build.
func NewAgentContextManager(
	models *bootstrap.ModelSet,
	model agentcore.ChatModel,
	agentName string,
	extraStrategies []corecontext.Strategy,
	summaryCfg *corecontext.FullSummaryConfig,
) *corecontext.ContextEngine {
	window, _ := models.ResolveContextWindow(bootstrap.ModelProvider(model), bootstrap.ModelName(model))
	return newContextManager(contextManagerConfig{
		Model:         model,
		ContextWindow: window,
		ReserveTokens: bootstrap.CompactReserveTokens(window),
		Agent:         agentName,
		ToolMicrocompact: &corecontext.ToolResultMicrocompactConfig{
			MinResultTokens: 200,
		},
		ExtraStrategies: extraStrategies,
		Summary:         summaryCfg,
	})
}

// contextRewriteCallback tạo callback log cho việc viết lại context.
// Kiến trúc mới đơn giản hóa: chỉ ghi slog, không ghi runtime queue hay UIEvent nữa.
func contextRewriteCallback(agent string) func(corecontext.RewriteEvent) {
	return func(ev corecontext.RewriteEvent) {
		attrs := []any{
			"module", "context",
			"agent", agent,
			"reason", ev.Reason,
			"strategy", ev.Strategy,
			"committed", ev.Committed,
			"tokens_before", ev.TokensBefore,
			"tokens_after", ev.TokensAfter,
		}
		if info := ev.Info; info != nil {
			attrs = append(attrs,
				"msgs_before", info.MessagesBefore,
				"msgs_after", info.MessagesAfter,
				"compacted", info.CompactedCount,
				"kept", info.KeptCount,
				"duration_ms", info.Duration.Milliseconds(),
			)
		}
		slog.Warn("viết lại context", attrs...)
	}
}
