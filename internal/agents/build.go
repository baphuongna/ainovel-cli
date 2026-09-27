package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/agentcore/llm"
	"github.com/voocel/agentcore/subagent"
	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/agents/ctxpack"
	"github.com/voocel/ainovel-cli/internal/agents/guard"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

// agentToRole chuẩn hóa tên subagent thành tên role mà ModelSet nhận biết.
// architect_short / architect_long cùng dùng cấu hình role architect.
// Đồng nghĩa với host.agentRoleName, vì build và host không phụ thuộc lẫn nhau nên mỗi bên giữ một bản.
func agentToRole(name string) string {
	if strings.HasPrefix(name, "architect_") {
		return "architect"
	}
	return name
}

// promptCacheBase tạo hash ngắn ổn định từ thư mục sách, làm tiền tố nhận dạng bộ đệm prompt:
// cùng một sách qua các lần khởi động lại process chia sẻ bucket định tuyến,
// không để lộ đường dẫn cục bộ cho provider. Hậu tố role do phía gọi ghép,
// mỗi lần subagent spawn lại thêm "#seq" (một khóa cho mỗi phiên).
func promptCacheBase(bookDir string) string {
	sum := sha256.Sum256([]byte(bookDir))
	return "nvl-" + hex.EncodeToString(sum[:6])
}

// subagentMaxRetries là giới hạn retry LLM cho mọi Worker.
// Chiến lược backoff: backoff mũ (giới hạn bởi maxDelay), ưu tiên tuân theo Retry-After của server.
// Tool chỉ khởi động sau khi hoàn tất một Assistant message,
// nên stream-idle / 503 / jitter mạng ngắn có thể retry an toàn trong Worker,
// không phát lại tác dụng phụ của tool.
const subagentMaxRetries = 7

// UsageRecorder là callback lượng dùng tùy chọn của BuildWorkers; signature đồng OnMessage,
// mỗi agent message đều được gọi một lần, Host layer chịu trách nhiệm tổng hợp.
// task là văn bản nhiệm vụ của lần spawn này dùng làm định danh phiên,
// phục vụ phát hiện đứt chuỗi đệm và reset baseline theo phiên.
// nil = không theo dõi.
type UsageRecorder func(agentName, task string, msg agentcore.AgentMessage)

// ApplyThinking áp cường độ suy luận của một role cụ thể lên Worker (điều chỉnh /model lúc chạy).
// architect → hai subagent architect_*; writer/editor → subagent tương ứng.
// level rỗng = dùng mặc định của model/provider. Các role khác bỏ qua.
type ApplyThinking func(role string, level agentcore.ThinkingLevel)

// ParseThinkingLevel chuyển chuỗi cấu hình thành agentcore.ThinkingLevel.
// "" hợp lệ (= không ghi đè/kế thừa); các giá trị khác phải là off/low/medium/high/xhigh/max,
// nếu không trả về error (lúc khởi động hạ cấp thành rỗng và warn, lúc chạy thì echo error cho người dùng).
func ParseThinkingLevel(s string) (agentcore.ThinkingLevel, error) {
	lv := agentcore.NormalizeThinkingLevel(agentcore.ThinkingLevel(s))
	switch lv {
	case "", agentcore.ThinkingOff, agentcore.ThinkingLow, agentcore.ThinkingMedium,
		agentcore.ThinkingHigh, agentcore.ThinkingXHigh, agentcore.ThinkingMax:
		return lv, nil
	default:
		return "", fmt.Errorf("cường độ suy luận không hợp lệ %q (có thể: off/low/medium/high/xhigh/max)", s)
	}
}

func ResolveThinkingForModel(model agentcore.ChatModel, level agentcore.ThinkingLevel) (agentcore.ThinkingLevel, bool) {
	level = agentcore.NormalizeThinkingLevel(level)
	// Với model chat thường không hỗ trợ thinking, explicit off không phải no-op mà là tham số bất hợp lệ.
	if cp, ok := model.(llm.CapabilityProvider); ok && cp.Capabilities().Thinking.Supported == llm.SupportNo {
		return agentcore.ThinkingAuto, level == agentcore.ThinkingAuto
	}
	return llm.ThinkingPolicyFor(model).Resolve(level)
}

func AvailableThinkingForModel(model agentcore.ChatModel) []agentcore.ThinkingLevel {
	if cp, ok := model.(llm.CapabilityProvider); ok && cp.Capabilities().Thinking.Supported == llm.SupportNo {
		return []agentcore.ThinkingLevel{agentcore.ThinkingAuto}
	}
	return llm.ThinkingPolicyFor(model).Available
}

// roleThinking phân tích cường độ suy luận có hiệu lực cho một role; giá trị không hợp lệ hạ cấp thành rỗng (không ghi đè) và warn.
func roleThinking(cfg bootstrap.Config, role string) agentcore.ThinkingLevel {
	lv, err := ParseThinkingLevel(cfg.ResolveReasoningEffort(role))
	if err != nil {
		slog.Warn("bỏ qua cấu hình cường độ suy luận không hợp lệ", "module", "agent", "role", role, "err", err)
		return ""
	}
	return lv
}

func resolvedRoleThinking(model agentcore.ChatModel, cfg bootstrap.Config, role string) agentcore.ThinkingLevel {
	resolved, _ := ResolveThinkingForModel(model, roleThinking(cfg, role))
	return resolved
}

// BuildWorkers lắp ráp ba Worker (architect_short/long, writer, editor) thành subagent.Runner
// gọi được bằng code. Engine gọi trực tiếp entry point kiểu, không qua LLM tool layer
// (docs/engine-rfc.md §1).
// Trả về Runner, WriterRestorePack và ApplyThinking (liên động cường độ suy luận từng role khi /model điều chỉnh;
// ContextManager của writer/architect/editor được factory tự động tái tạo).
// onGuardBlock tùy chọn (nil-safe): callback audit chặn/nâng cấp StopGuard của từng Worker.
func BuildWorkers(
	cfg bootstrap.Config,
	store *store.Store,
	styleStats *tools.StyleStatsIndex,
	models *bootstrap.ModelSet,
	bundle assets.Bundle,
	recordUsage UsageRecorder,
	onGuardBlock guard.BlockHook,
) (*subagent.Runner, *ctxpack.WriterRestorePack, ApplyThinking) {
	// tool dùng chung
	contextTool := tools.NewContextTool(store, bundle.References, cfg.Style, styleStats)
	readChapter := tools.NewReadChapterTool(store)

	architectTools := []agentcore.Tool{
		contextTool,
		tools.NewSaveBookTool(store),
		tools.NewSaveFoundationTool(store),
		tools.NewReviseOutlineTool(store),
		// Các chương trong hàng đợi viết lại chỉ có thể dựa vào chapter_contract để xác định "cần viết lại thế nào":
		// revise_outline không được đụng chương đã viết, save_foundation(outline) cấm ghi đè toàn bộ trong thời gian viết.
		tools.NewPlanChapterTool(store),
		tools.NewResolveOutlineFeedbackTool(store),
		tools.NewAuditFoundationTool(store),
	}
	writerTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewPlanChapterTool(store),
		tools.NewDraftChapterTool(store),
		tools.NewEditChapterTool(store),
		tools.NewCheckConsistencyTool(store),
		tools.NewCommitChapterTool(store, styleStats),
	}
	editorTools := []agentcore.Tool{
		contextTool,
		readChapter,
		tools.NewSaveReviewTool(store),
		tools.NewSaveArcSummaryTool(store),
		tools.NewSaveVolumeSummaryTool(store),
	}

	// Provider failover chỉ ghi log, không thông báo host
	reportFailover := func(ev bootstrap.FailoverEvent) {
		slog.Warn("provider chuyển",
			"module", "agent",
			"role", ev.Role,
			"reason", ev.Reason,
			"from", fmt.Sprintf("%s/%s", ev.FromProvider, ev.FromModel),
			"to", fmt.Sprintf("%s/%s", ev.ToProvider, ev.ToModel),
			"err", ev.Err,
		)
	}

	architectModel := models.ForRoleWithFailover("architect", reportFailover)
	writerModel := models.ForRoleWithFailover("writer", reportFailover)
	editorModel := models.ForRoleWithFailover("editor", reportFailover)

	// ContextManager của mọi Worker được factory tái tạo mỗi lần gọi, cửa sổ động theo model swap
	// (Writer dùng cấu hình riêng bên dưới; architect/editor đi qua NewAgentContextManager).
	writerProvider, writerModelName, _ := models.CurrentSelection("writer")
	writerContextWindow, writerSource := cfg.ResolveContextWindow(writerProvider, writerModelName)
	bootstrap.LogContextWindowChoice("writer", writerModelName, writerContextWindow, writerSource)
	architectProvider, architectModelName, _ := models.CurrentSelection("architect")
	architectContextWindow, architectSource := cfg.ResolveContextWindow(architectProvider, architectModelName)
	bootstrap.LogContextWindowChoice("architect", architectModelName, architectContextWindow, architectSource)
	editorProvider, editorModelName, _ := models.CurrentSelection("editor")
	editorContextWindow, editorSource := cfg.ResolveContextWindow(editorProvider, editorModelName)
	bootstrap.LogContextWindowChoice("editor", editorModelName, editorContextWindow, editorSource)

	// modelLookup ghi session thì đính mỗi assistant message với _meta:{provider,model},
	// giúp replay không còn phụ thuộc "ModelSet hiện tại" để suy ngược cost lịch sử, chuyển model giữa chừng cũng tính chính xác.
	modelLookup := func(agentName string) (string, string) {
		role := agentToRole(agentName)
		provider, name, _ := models.CurrentSelection(role)
		return provider, name
	}
	baseOnMsg := store.Sessions.SubAgentLogger(modelLookup)
	onMsg := func(agentName, task string, msg agentcore.AgentMessage) {
		baseOnMsg(agentName, task, msg)
		if recordUsage != nil {
			recordUsage(agentName, task, msg)
		}
	}

	// bộ đệm prompt: một sách một base, một role một tên, một phiên một khóa (subagent spawn thêm #seq).
	// Dòng OpenAI dùng prompt_cache_key để affinity định tuyến; dòng Claude dùng cache_control rolling checkpoint
	// (system floor + đuôi message cuối). Khi provider không hỗ trợ, agentcore im lặng loại bỏ theo năng lực,
	// lợi ích đọc bộ đệm trong phiên nhiều vòng luôn dương nên không cần công tắc.
	cacheBase := promptCacheBase(store.Dir())

	architectStopGuardFactory := func(_, _ string) agentcore.StopGuard {
		return guard.NewArchitectStopGuard(store, onGuardBlock)
	}
	architectThinking, _ := ResolveThinkingForModel(architectModel, roleThinking(cfg, "architect"))
	architectShort := subagent.Config{
		Name:             "architect_short",
		Description:      "Quy hoạch viên ngắn: tạo thiết lập gọn và dàn ý phẳng cho truyện một tập, một xung đột, mật độ cao",
		Model:            architectModel,
		SystemPrompt:     bundle.Prompts.ArchitectShort,
		Tools:            architectTools,
		MaxTurns:         15,
		MaxRetries:       subagentMaxRetries,
		ThinkingLevel:    architectThinking,
		OnMessage:        onMsg,
		CacheLastMessage: "ephemeral",
		PromptCacheKey:   cacheBase + "-architect_short",
		StopAfterToolResult: func(toolName string, result json.RawMessage) bool {
			return foundationReadyResult(toolName, result)
		},
		StopGuardFactory: architectStopGuardFactory,
		// Nén session theo context_window; không đụng store (architect đọc lại qua novel_context).
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			return NewAgentContextManager(models, model, "architect_short", nil, &corecontext.FullSummaryConfig{
				SystemPrompt:        ctxpack.ArchitectSummarySystemPrompt,
				SummaryPrompt:       ctxpack.ArchitectSummaryPrompt,
				UpdateSummaryPrompt: ctxpack.ArchitectUpdateSummaryPrompt,
			})
		},
	}
	architectLong := subagent.Config{
		Name:                "architect_long",
		Description:         "Quy hoạch viên dài: tạo thiết lập phân tầng và dàn ý tập-cung cho truyện dạng serialized, có thể nâng cấp liên tục",
		Model:               architectModel,
		SystemPrompt:        bundle.Prompts.ArchitectLong,
		Tools:               architectTools,
		MaxTurns:            20,
		MaxRetries:          subagentMaxRetries,
		ThinkingLevel:       architectThinking,
		OnMessage:           onMsg,
		CacheLastMessage:    "ephemeral",
		PromptCacheKey:      cacheBase + "-architect_long",
		StopAfterToolResult: architectLongShouldStopAfterToolResult,
		StopGuardFactory:    architectStopGuardFactory,
		// Nén session theo context_window; không đụng store (architect đọc lại qua novel_context).
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			return NewAgentContextManager(models, model, "architect_long", nil, &corecontext.FullSummaryConfig{
				SystemPrompt:        ctxpack.ArchitectSummarySystemPrompt,
				SummaryPrompt:       ctxpack.ArchitectSummaryPrompt,
				UpdateSummaryPrompt: ctxpack.ArchitectUpdateSummaryPrompt,
			})
		},
	}

	// Đường duy nhất lắp ráp: protocol template {{VOICE}} điền lại phần văn phong tại chỗ, rồi thêm preset kiểu.
	// eval voice A/B đi cùng hàm, đảm bảo hai nhánh tương đương (docs/voice-layer.md §3.2).
	writerPrompt := assets.BuildWriterPrompt(bundle.Prompts.Writer, bundle.Voice, bundle.Styles[cfg.Style])

	restore := &ctxpack.WriterRestorePack{}
	restore.Refresh(store)

	writer := subagent.Config{
		Name:             "writer",
		Description:      "Người viết: tự hoàn thành một chương từ ý tưởng, viết, tự xem xét đến nộp",
		Model:            writerModel,
		SystemPrompt:     writerPrompt,
		Tools:            writerTools,
		MaxTurns:         30,
		MaxRetries:       subagentMaxRetries,
		ThinkingLevel:    resolvedRoleThinking(writerModel, cfg, "writer"),
		StopAfterTools:   []string{"commit_chapter"},
		OnMessage:        onMsg,
		CacheLastMessage: "ephemeral",
		PromptCacheKey:   cacheBase + "-writer",
		StopGuardFactory: func(_, _ string) agentcore.StopGuard {
			return guard.NewWriterStopGuard(store, onGuardBlock)
		},
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			// Mỗi chương tái tạo context manager theo model writer hiện tại.
			window, _ := models.ResolveContextWindow(bootstrap.ModelProvider(model), bootstrap.ModelName(model))
			return newContextManager(contextManagerConfig{
				Model:         model,
				ContextWindow: window,
				ReserveTokens: bootstrap.CompactReserveTokens(window),
				Agent:         "writer",
				// dự đoán commit, tránh viết lại prefix request lặp lại ở các vòng sau.
				CommitProjected: true,
				ToolMicrocompact: &corecontext.ToolResultMicrocompactConfig{
					MinResultTokens: 200,
				},
				ExtraStrategies: []corecontext.Strategy{
					ctxpack.NewStoreSummaryCompact(ctxpack.StoreSummaryCompactConfig{
						Store:            store,
						KeepRecentTokens: 20000,
					}),
				},
				Summary: &corecontext.FullSummaryConfig{
					PostSummaryHooks:    []corecontext.PostSummaryHook{restore.Hook()},
					SystemPrompt:        ctxpack.WriterSummarySystemPrompt,
					SummaryPrompt:       ctxpack.WriterSummaryPrompt,
					UpdateSummaryPrompt: ctxpack.WriterUpdateSummaryPrompt,
					TurnPrefixPrompt:    ctxpack.WriterTurnPrefixPrompt,
				},
			})
		},
	}

	editor := subagent.Config{
		Name:             "editor",
		Description:      "Người xem xét: đọc văn gốc, phát hiện vấn đề từ hai góc độ cấu trúc và thẩm mỹ",
		Model:            editorModel,
		SystemPrompt:     bundle.Prompts.Editor,
		Tools:            editorTools,
		MaxTurns:         20,
		MaxRetries:       subagentMaxRetries,
		ThinkingLevel:    resolvedRoleThinking(editorModel, cfg, "editor"),
		OnMessage:        onMsg,
		CacheLastMessage: "ephemeral",
		PromptCacheKey:   cacheBase + "-editor",
		// sản phẩm đã đóng băng dừng ngay khi trúng. exit đã đóng băng vẫn hỏi StopGuard (test hợp đồng TestContract_
		// TerminalToolExitConsultsStopGuard), NewEditorStopGuard nhận biết nhiệm vụ chịu trách nhiệm
		// bác exit sớm "được phái tạo tóm tắt nhưng chỉ làm kiểm duyệt", nên save_review có thể hard-stop an toàn.
		StopAfterToolResult: func(toolName string, _ json.RawMessage) bool {
			return toolName == "save_review" || toolName == "save_arc_summary" || toolName == "save_volume_summary"
		},
		StopGuardFactory: func(_, task string) agentcore.StopGuard {
			return guard.NewEditorStopGuard(store, task, onGuardBlock)
		},
		// Nén session theo context_window; không đụng store (editor đọc lại qua novel_context).
		ContextManagerFactory: func(model agentcore.ChatModel) agentcore.ContextManager {
			return NewAgentContextManager(models, model, "editor", nil, &corecontext.FullSummaryConfig{
				SystemPrompt:        ctxpack.EditorSummarySystemPrompt,
				SummaryPrompt:       ctxpack.EditorSummaryPrompt,
				UpdateSummaryPrompt: ctxpack.EditorUpdateSummaryPrompt,
			})
		},
	}

	runner := subagent.NewRunner(architectShort, architectLong, writer, editor)

	// Liên động cường độ suy luận từng role lúc chạy (điều chỉnh /model).
	applyThinking := func(role string, level agentcore.ThinkingLevel) {
		switch role {
		case "architect":
			level, _ = ResolveThinkingForModel(models.ForRole("architect"), level)
			runner.SetThinkingLevel("architect_short", level)
			runner.SetThinkingLevel("architect_long", level)
		case "writer", "editor":
			level, _ = ResolveThinkingForModel(models.ForRole(role), level)
			runner.SetThinkingLevel(role, level)
		}
	}

	return runner, restore, applyThinking
}

type saveFoundationResult struct {
	Type            string `json:"type"`
	FoundationReady bool   `json:"foundation_ready"`
}

func decodeSaveFoundationResult(toolName string, result json.RawMessage) saveFoundationResult {
	if toolName != "save_foundation" {
		return saveFoundationResult{}
	}
	var r saveFoundationResult
	_ = json.Unmarshal(result, &r)
	return r
}

func architectLongShouldStopAfterToolResult(toolName string, result json.RawMessage) bool {
	if foundationReadyResult(toolName, result) {
		return true
	}
	r := decodeSaveFoundationResult(toolName, result)
	switch r.Type {
	case "expand_arc", "complete_book":
		return true
	default:
		return false
	}
}

func foundationReadyResult(toolName string, result json.RawMessage) bool {
	if toolName != "audit_foundation" {
		return false
	}
	var r struct {
		FoundationReady bool `json:"foundation_ready"`
	}
	return json.Unmarshal(result, &r) == nil && r.FoundationReady
}
