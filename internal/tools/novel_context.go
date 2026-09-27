package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/store"
)

// References: tài liệu tham chiếu được nhúng.
type References struct {
	// V0
	ChapterGuide      string
	HookTechniques    string
	QualityChecklist  string
	OutlineTemplate   string
	CharacterTemplate string
	ChapterTemplate   string
	// V1
	Consistency      string
	ContentExpansion string
	DialogueWriting  string
	// V2
	StyleReference   string // tham chiếu bổ sung về phong cách (có thể rỗng)
	LongformPlanning string // tham chiếu quy hoạch trường thiên tổng quát
	Differentiation  string // tham chiếu thiết kế khác biệt hóa tổng quát
	ArcTemplates     string // mẫu cung truyện theo đề tài (tải theo style, có thể rỗng)
	AntiAITone       string // kho tiêu chí khử chất AI (writer/editor dùng chung, chèn suốt tiến trình)
}

// ContextTool lắp ráp ngữ cảnh cần cho chương hiện tại.
type ContextTool struct {
	store      *store.Store
	refs       References
	style      string
	styleStats *StyleStatsIndex
}

type contextReads struct {
	warnings []string
	seen     map[string]struct{}
	err      error
}

func (r *contextReads) warn(scope string, err error) {
	if err == nil || os.IsNotExist(err) {
		return
	}
	msg := fmt.Sprintf("đọc %s thất bại: %v", scope, err)
	if r.seen == nil {
		r.seen = make(map[string]struct{})
	}
	if _, ok := r.seen[msg]; ok {
		return
	}
	r.seen[msg] = struct{}{}
	r.warnings = append(r.warnings, msg)
}

func (r *contextReads) require(scope string, err error) {
	if r.err != nil || err == nil || os.IsNotExist(err) || errors.Is(err, store.ErrOutlineChapterNotFound) {
		return
	}
	r.err = fmt.Errorf("đọc %s thất bại: %w", scope, err)
}

// NewContextTool tạo công cụ ngữ cảnh. styleStats phải dùng chung với commit_chapter,
// nếu không sau khi viết lại chương, ngữ cảnh sẽ tiếp tục đọc thống kê cũ.
// user_rules được chèn bởi buildUserRules đọc trực tiếp snapshot của sách (meta/user_rules.json), không còn phụ thuộc tùy chọn tải.
func NewContextTool(
	store *store.Store,
	refs References,
	style string,
	styleStats *StyleStatsIndex,
) *ContextTool {
	if styleStats == nil {
		// fail-fast wiring invariant: giống NewCommitChapterTool — chỉ khi Host nối nhầm mới có thể là nil.
		panic("tools: NewContextTool requires StyleStatsIndex")
	}
	return &ContextTool{store: store, refs: refs, style: style, styleStats: styleStats}
}

func (t *ContextTool) Name() string { return "novel_context" }
func (t *ContextTool) Description() string {
	return "Lấy trạng thái hiện tại và ngữ cảnh sáng tác của tiểu thuyết." +
		"Không truyền chapter: trả về progress_status (phase/flow/next_chapter/pending_rewrites v.v. trường tiến độ) + thiết lập nền tảng, dùng để quyết định bước tiếp theo." +
		"Truyền chapter=N: ngoài ra trả về ngữ cảnh viết của chương đó như tóm tắt tiền tình, phục bút, trạng thái nhân vật, quy tắc phong cách"
}
func (t *ContextTool) Label() string { return "Nạp ngữ cảnh" }

// Công cụ thuần đọc, có thể được điều độ đồng thời.
func (t *ContextTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *ContextTool) ConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *ContextTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("chapter", schema.Int("Số chương. Không truyền thì trả về trạng thái tiến độ và thiết lập nền tảng (dùng cho Architect); truyền vào thì ngoài ra trả về ngữ cảnh viết của chương đó (dùng cho Writer/Editor)")),
	)
}

func (t *ContextTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Chapter int `json:"chapter"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w", err)
	}

	result := make(map[string]any)
	reads := &contextReads{}

	if a.Chapter > 0 {
		// Đường dẫn Writer: tải toàn bộ dữ liệu nền + ngữ cảnh chương
		t.buildBaseContext(result, reads)
		seed := newChapterContextEnvelope()
		state := t.prepareChapterContext(a.Chapter, &seed, reads)
		seed.apply(result)
		t.buildChapterContext(result, state, reads)
		// Sự kiện vi phạm cơ học của chương đó (kiểm tra theo user_rules khi commit và ghi xuống đĩa):
		// editor xem xét và ánh xạ vào bảy chiều (editor.md § ánh xạ kiểm tra cơ học); writer tự kiểm tra khi viết lại.
		if violations := t.store.World.LoadRuleViolations(a.Chapter); len(violations) > 0 {
			result["rule_violations"] = violations
		}
		// episodic là ghi nhớ đã viết vào chính văn, không phải tư liệu chờ viết.
		if epi, ok := result["episodic_memory"].(map[string]any); ok && len(epi) > 0 {
			epi["_usage"] = "Container này là ghi nhớ sự kiện đã viết vào chính văn (để đối chiếu nhất quán và liên kết); lặp lại nguyên văn các nội dung này trong chính văn chương mới là khuyết tật lặp"
		}
	} else {
		// Đường dẫn Architect: chỉ trả về trạng thái + dữ liệu có cấu trúc, không tải toàn bộ văn bản gốc
		t.buildProgressStatus(result, reads)
		t.buildArchitectContext(result, reads)
	}

	// Chèn working_memory.user_rules (đường dẫn canonical). Đường dẫn kiến trúc sư vốn không có working_memory,
	// buildUserRules tạo mới theo nhu cầu container chỉ chứa user_rules. Khi thiếu snapshot thì lùi về mặc định nội bộ,
	// luôn xuất cấu trúc ổn định, tránh LLM thấy user_rules=null rồi đi nhánh ngoại lệ.
	if a.Chapter > 0 {
		t.buildSimulationProfile(result, "working_memory", reads)
	} else {
		t.buildSimulationProfile(result, "planning_memory", reads)
	}

	t.buildUserRules(result, reads)

	if reads.err != nil {
		return nil, reads.err
	}
	if len(reads.warnings) > 0 {
		result["_warnings"] = reads.warnings
	}

	// Ngân sách ưu tiên: khi tổng kích thước vượt ngưỡng thì cắt bớt dữ liệu ưu tiên thấp; tóm tắt được dựng lại sau khi cắt xong,
	// bảo đảm số trường hiển thị và _trimmed khớp với payload cuối.
	budget := 60 * 1024
	if a.Chapter > 0 {
		budget = 100 * 1024
	}
	return finalizeContextPayload(result, a.Chapter, budget)
}

func finalizeContextPayload(result map[string]any, chapter, budget int) (json.RawMessage, error) {
	if err := trimByBudget(result, budget); err != nil {
		return nil, err
	}
	result["_loading_summary"] = buildLoadingSummary(result, chapter)
	if err := trimByBudget(result, budget); err != nil {
		return nil, err
	}
	result["_loading_summary"] = buildLoadingSummary(result, chapter)

	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal context payload: %w", err)
	}
	if len(data) > budget {
		return nil, fmt.Errorf("context payload exceeds budget after summary rebuild: size=%d budget=%d", len(data), budget)
	}
	return data, nil
}

// buildLoadingSummary thống kê lượng dữ liệu từng mục từ result đã lắp ráp, tạo một dòng tóm tắt dễ đọc.
func buildLoadingSummary(result map[string]any, chapter int) string {
	var parts []string
	working, _ := result["working_memory"].(map[string]any)
	episodic, _ := result["episodic_memory"].(map[string]any)
	planning, _ := result["planning_memory"].(map[string]any)
	foundation, _ := result["foundation_memory"].(map[string]any)
	referencePack, _ := result["reference_pack"].(map[string]any)

	if chapter > 0 {
		parts = append(parts, fmt.Sprintf("ch=%d", chapter))
		if tier, ok := episodic["planning_tier"].(domain.PlanningTier); ok && tier != "" {
			parts = append(parts, fmt.Sprintf("tier=%s", tier))
		}
	} else {
		parts = append(parts, "architect")
		if tier, ok := planning["planning_tier"].(domain.PlanningTier); ok && tier != "" {
			parts = append(parts, fmt.Sprintf("tier=%s", tier))
		}
	}

	if pos, ok := episodic["position"].(map[string]any); ok {
		parts = append(parts, fmt.Sprintf("V%dA%d", pos["volume"], pos["arc"]))
	}

	var items []string

	if n := firstSliceLen(episodic["character_snapshots"], foundation["character_snapshots"]); n > 0 {
		items = append(items, fmt.Sprintf("nhân vật:%d(snapshot)", n))
	} else if n := firstSliceLen(episodic["characters"], foundation["characters"]); n > 0 {
		items = append(items, fmt.Sprintf("nhân vật:%d", n))
	}

	if len(working) > 0 {
		items = append(items, fmt.Sprintf("bộ nhớ làm việc:%d", len(working)))
	}
	if len(episodic) > 0 {
		items = append(items, fmt.Sprintf("bộ nhớ tình tiết:%d", len(episodic)))
	}
	if len(planning) > 0 {
		items = append(items, fmt.Sprintf("bộ nhớ quy hoạch:%d", len(planning)))
	}
	if len(foundation) > 0 {
		items = append(items, fmt.Sprintf("bộ nhớ nền tảng:%d", len(foundation)))
	}

	if n := firstSliceLen(working["volume_summaries"], planning["volume_summaries"]); n > 0 {
		items = append(items, fmt.Sprintf("tóm tắt quyển:%d", n))
	}
	if n := firstSliceLen(working["arc_summaries"], planning["arc_summaries"]); n > 0 {
		items = append(items, fmt.Sprintf("tóm tắt cung:%d", n))
	}
	if n := sliceLen(working["recent_summaries"]); n > 0 {
		items = append(items, fmt.Sprintf("tóm tắt chương:%d", n))
	}

	if n := sliceLen(planning["layered_outline"]); n > 0 {
		items = append(items, fmt.Sprintf("dàn ý phân tầng:%d quyển", n))
	}

	if n := sliceLen(working["timeline"]); n > 0 {
		items = append(items, fmt.Sprintf("dòng thời gian:%d", n))
	}
	if n := firstSliceLen(episodic["foreshadow_ledger"], foundation["foreshadow_ledger"]); n > 0 {
		items = append(items, fmt.Sprintf("phục bút:%d", n))
	}
	if n := sliceLen(episodic["relationship_state"]); n > 0 {
		items = append(items, fmt.Sprintf("quan hệ:%d", n))
	}
	if n := sliceLen(episodic["recent_state_changes"]); n > 0 {
		items = append(items, fmt.Sprintf("thay đổi trạng thái:%d", n))
	}
	if _, ok := working["previous_tail"]; ok {
		items = append(items, "đuôi chương trước:ok")
	}
	if _, ok := referencePack["style_rules"]; ok {
		items = append(items, "quy tắc phong cách:ok")
	}
	if n := sliceLen(episodic["related_chapters"]); n > 0 {
		items = append(items, fmt.Sprintf("chương liên quan:%d", n))
	}
	if selected, ok := result["selected_memory"].(map[string]any); ok && len(selected) > 0 {
		if n := sliceLen(selected["story_threads"]); n > 0 {
			items = append(items, fmt.Sprintf("tri hồi manh mối:%d", n))
		}
		if n := sliceLen(selected["review_lessons"]); n > 0 {
			items = append(items, fmt.Sprintf("tri hồi xem xét:%d", n))
		}
	}

	if refs, ok := referencePack["references"].(map[string]string); ok && len(refs) > 0 {
		items = append(items, fmt.Sprintf("tham chiếu:%d mục", len(refs)))
	}
	if len(referencePack) > 0 {
		items = append(items, fmt.Sprintf("gói tham chiếu:%d", len(referencePack)))
	}
	if _, ok := result["memory_policy"]; ok {
		items = append(items, "chiến lược bộ nhớ:ok")
	}
	if _, ok := working["simulation_profile"]; ok {
		items = append(items, "hồ sơ mô phỏng văn phong:ok")
	} else if _, ok := planning["simulation_profile"]; ok {
		items = append(items, "hồ sơ mô phỏng văn phong:ok")
	}
	if warnings, ok := result["_warnings"].([]string); ok && len(warnings) > 0 {
		items = append(items, fmt.Sprintf("cảnh báo:%d", len(warnings)))
	}
	if trimmed, ok := result["_trimmed"].([]string); ok && len(trimmed) > 0 {
		items = append(items, fmt.Sprintf("cắt tỉa:%s", strings.Join(trimmed, ",")))
	}

	if len(items) > 0 {
		parts = append(parts, strings.Join(items, " "))
	}
	return strings.Join(parts, " | ")
}

// sliceLen thử lấy độ dài slice với kiểu any.
func sliceLen(v any) int {
	switch s := v.(type) {
	case []domain.ChapterSummary:
		return len(s)
	case []domain.ArcSummary:
		return len(s)
	case []domain.VolumeSummary:
		return len(s)
	case []domain.CharacterSnapshot:
		return len(s)
	case []domain.TimelineEvent:
		return len(s)
	case []domain.ForeshadowEntry:
		return len(s)
	case []domain.RelationshipEntry:
		return len(s)
	case []domain.StateChange:
		return len(s)
	case []domain.VolumeOutline:
		return len(s)
	case []domain.Character:
		return len(s)
	case []domain.RelatedChapter:
		return len(s)
	case []domain.RecallItem:
		return len(s)
	case []planningVolumeOutline:
		return len(s)
	default:
		return 0
	}
}

func firstSliceLen(values ...any) int {
	for _, value := range values {
		if n := sliceLen(value); n > 0 {
			return n
		}
	}
	return 0
}

// loadFilteredCharacters lọc nhân vật theo Tier và sự xuất hiện trong cảnh.
// core/important luôn trả về; secondary/decorative chỉ trả về khi được nhắc trong dàn ý chương hiện tại.
func (t *ContextTool) loadFilteredCharacters(result map[string]any, chapter int, reads *contextReads) {
	chars, err := t.store.Characters.Load()
	if err != nil {
		reads.require("characters", err)
		return
	}
	if len(chars) == 0 {
		return
	}

	// Lấy mô tả cảnh của dàn ý chương hiện tại, dùng để khớp nhân vật phụ
	entry, err := t.store.Outline.GetChapterOutline(chapter)
	if err != nil {
		reads.require("current_chapter_outline", err)
		result["characters"] = chars
		return
	}
	if entry == nil {
		result["characters"] = chars
		return
	}
	sceneText := strings.Join(entry.Scenes, " ") + " " + entry.CoreEvent + " " + entry.Title

	var filtered []domain.Character
	for _, c := range chars {
		switch c.Tier {
		case "secondary", "decorative":
			if matchCharacter(sceneText, c) {
				filtered = append(filtered, c)
			}
		default: // core, important, hoặc chưa đặt
			filtered = append(filtered, c)
		}
	}
	result["characters"] = filtered
}

// matchCharacter kiểm tra văn bản cảnh có chứa tên chính thức hay một trong các biệt danh của nhân vật.
func matchCharacter(text string, c domain.Character) bool {
	if strings.Contains(text, c.Name) {
		return true
	}
	for _, alias := range c.Aliases {
		if strings.Contains(text, alias) {
			return true
		}
	}
	return false
}

// loadLayeredSummaries tải tóm tắt phân tầng: tóm tắt quyển + tóm tắt cung của quyển hiện tại + tóm tắt chương trong cung.
func (t *ContextTool) loadLayeredSummaries(result map[string]any, chapter, summaryWindow int, reads *contextReads) {
	vol, arc, err := t.store.Outline.LocateChapter(chapter)
	if err != nil {
		reads.require("layered_outline_position", err)
		return
	}

	// 1. Tóm tắt quyển cho các quyển đã hoàn thành
	if volSummaries, err := t.store.Summaries.LoadAllVolumeSummaries(); err == nil && len(volSummaries) > 0 {
		result["volume_summaries"] = volSummaries
	} else {
		reads.require("volume_summaries", err)
	}

	// 2. Tóm tắt cung của các cung đã hoàn thành trong quyển hiện tại (không gồm cung hiện tại)
	if arcSummaries, err := t.store.Summaries.LoadArcSummaries(vol); err == nil && len(arcSummaries) > 0 {
		var prior []domain.ArcSummary
		for _, s := range arcSummaries {
			if s.Arc < arc {
				prior = append(prior, s)
			}
		}
		if len(prior) > 0 {
			result["arc_summaries"] = prior
		}
	} else {
		reads.require("arc_summaries", err)
	}

	// 3. Tóm tắt chương của N chương gần nhất trong cung hiện tại
	if summaries, err := t.store.Summaries.LoadRecentSummaries(chapter, summaryWindow); err == nil && len(summaries) > 0 {
		result["recent_summaries"] = summaries
	} else {
		reads.require("recent_summaries", err)
	}
}

// loadLayeredCharacters tải nhân vật ở chế độ Layered: ưu tiên snapshot gần nhất, lùi về thiết lập gốc + lọc Tier.
func (t *ContextTool) loadLayeredCharacters(result map[string]any, chapter int, reads *contextReads) {
	snapshots, err := t.store.Characters.LoadLatestSnapshots()
	if err == nil && len(snapshots) > 0 {
		result["character_snapshots"] = snapshots
		// Đồng thời giữ nhân vật core/important trong thiết lập gốc (snapshot có thể không chứa nhân vật mới xuất hiện)
		t.loadFilteredCharacters(result, chapter, reads)
		return
	}
	reads.require("character_snapshots", err)
	// Khi không có snapshot thì lùi về thiết lập gốc
	t.loadFilteredCharacters(result, chapter, reads)
}

// writerReferences trả về tài liệu tham khảo viết. Chương 1 trả về toàn bộ, các chương sau cắt bỏ các mẫu không còn cần.
func (t *ContextTool) writerReferences(chapter int) map[string]string {
	refs := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			refs[k] = v
		}
	}
	// Tải tiệm tiến: luôn giữ tham chiếu lõi, 3 chương đầu tải thêm hướng dẫn viết đầy đủ
	add("consistency", t.refs.Consistency)
	add("hook_techniques", t.refs.HookTechniques)
	add("quality_checklist", t.refs.QualityChecklist)
	add("anti_ai_tone", t.refs.AntiAITone) // tiêu chí khử chất AI chèn suốt tiến trình, không cắt theo chương
	if chapter <= 3 {
		add("chapter_guide", t.refs.ChapterGuide)
		add("dialogue_writing", t.refs.DialogueWriting)
		add("style_reference", t.refs.StyleReference)
	}

	// Tham chiếu bổ sung chỉ tải ở chương đầu
	if chapter <= 1 {
		add("chapter_template", t.refs.ChapterTemplate)
		add("content_expansion", t.refs.ContentExpansion)
	}
	return refs
}

func (t *ContextTool) architectReferences() map[string]string {
	refs := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			refs[k] = v
		}
	}
	add("outline_template", t.refs.OutlineTemplate)
	add("character_template", t.refs.CharacterTemplate)
	add("longform_planning", t.refs.LongformPlanning)
	add("differentiation", t.refs.Differentiation)
	add("style_reference", t.refs.StyleReference)
	add("arc_templates", t.refs.ArcTemplates)
	add("anti_ai_tone", t.refs.AntiAITone) // khử giọng AI cho dàn ý architect; cũng bao đường dẫn editor chạy Chapter=0
	return refs
}

// foundationStatus kiểm tra độ đầy đủ của thiết lập nền tảng, trả về danh sách mục thiếu.
// Dùng chung logic xác định store.FoundationMissing với công cụ save_foundation, bảo đảm ready/missing mà LLM thấy từ
// novel_context luôn khớp với foundation_ready mà save_foundation trả về
// (chi tiết như các mục bắt buộc compass của trường thiên không bị trôi).
func (t *ContextTool) foundationStatus() (map[string]any, error) {
	missing, err := t.store.FoundationMissing()
	if err != nil {
		return nil, err
	}
	status := map[string]any{"ready": len(missing) == 0}
	if len(missing) > 0 {
		status["missing"] = missing
	}
	if len(missing) == 1 && missing[0] == "foundation_audit" {
		fingerprint, err := t.store.FoundationFingerprint()
		if err != nil {
			return nil, err
		}
		status["fingerprint"] = fingerprint
	}
	if audit, err := t.store.Outline.LoadFoundationAudit(); err != nil {
		return nil, err
	} else if audit != nil && !audit.Ready {
		status["last_audit"] = audit
	}
	return status, nil
}

// trimByBudget cắt result theo ưu tiên để tổng kích thước JSON không vượt budget byte.
// Ưu tiên (từ thấp đến cao): references < voice_samples < style_anchors < previous_tail < timeline
//
//	< recent_state_changes < foreshadow_ledger < relationship_state < còn lại (không cắt)
//
// style_stats là tín hiệu lõi cấp toàn sách có kích thước giới hạn, không tham gia cắt.
//
// key bị cắt được ghi vào result["_trimmed"] để phục vụ truy vết bằng log.
func trimByBudget(result map[string]any, budget int) error {
	// Đo kích thước hiện tại trước
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("measure context payload: %w", err)
	}
	if len(data) <= budget {
		return nil
	}

	// Liệt kê key có thể cắt theo ưu tiên từ thấp đến cao
	trimOrder := []string{
		"references",
		"voice_samples",
		"style_anchors",
		"style_rules",
		"previous_tail",
		"timeline",
		"recent_state_changes",
		"foreshadow_ledger",
		"relationship_state",
	}

	trimmed, _ := result["_trimmed"].([]string)
	trimmed = append([]string(nil), trimmed...)
	for _, key := range trimOrder {
		if !deleteContextKey(result, key) {
			continue
		}
		trimmed = append(trimmed, key)
		result["_trimmed"] = append([]string(nil), trimmed...)
		data, err = json.Marshal(result)
		if err != nil {
			return fmt.Errorf("measure trimmed context payload: %w", err)
		}
		if len(data) <= budget {
			return nil
		}
	}
	return fmt.Errorf("context payload exceeds budget after trimming: size=%d budget=%d", len(data), budget)
}

func deleteContextKey(result map[string]any, key string) bool {
	deleted := false
	for _, containerKey := range []string{
		"working_memory",
		"episodic_memory",
		"planning_memory",
		"foundation_memory",
		"reference_pack",
		"selected_memory",
	} {
		section, ok := result[containerKey].(map[string]any)
		if !ok {
			continue
		}
		if _, ok := section[key]; ok {
			delete(section, key)
			deleted = true
		}
	}
	return deleted
}

// buildRelatedChapters tra ngược chương lịch sử liên quan đến chương hiện tại theo dữ liệu có cấu trúc.
// Đề xuất theo bốn chiều: phục bút, sự xuất hiện nhân vật, thay đổi trạng thái, quan hệ; sau khử trùng tối đa 5 mục.
// Toàn bộ dữ liệu truyền qua tham số, không làm IO thêm.
func (t *ContextTool) buildRelatedChapters(
	chapter int,
	entry *domain.OutlineEntry,
	foreshadow []domain.ForeshadowEntry,
	relationships []domain.RelationshipEntry,
	stateChanges []domain.StateChange,
	reads *contextReads,
) []domain.RelatedChapter {
	const recentWindow = 10
	const maxResults = 5

	seen := make(map[int]struct{})
	var results []domain.RelatedChapter
	add := func(ch int, reason string) {
		if ch <= 0 || ch >= chapter {
			return
		}
		// Vài chương gần nhất quá sát, không đề xuất
		if ch > chapter-recentWindow {
			return
		}
		if _, ok := seen[ch]; ok {
			return
		}
		seen[ch] = struct{}{}
		results = append(results, domain.RelatedChapter{Chapter: ch, Reason: reason})
	}

	// Ghép văn bản dàn ý để khớp từ khóa
	outlineText := entry.Title + " " + entry.CoreEvent
	for _, s := range entry.Scenes {
		outlineText += " " + s
	}

	// 1. Tra ngược phục bút: mô tả phục bút đang hoạt động có liên quan dàn ý chương hiện tại không
	for _, f := range foreshadow {
		if strings.Contains(outlineText, f.ID) || containsAny(outlineText, strings.Fields(f.Description)) {
			add(f.PlantedAt, fmt.Sprintf("chương cài phục bút %s(%s)", f.ID, truncateRunes(f.Description, 15)))
		}
		if len(results) >= maxResults {
			break
		}
	}

	// 2. Tra ngược xuất hiện nhân vật: duyệt một lần theo lô, IO giảm từ O(số nhân vật×số chương) xuống O(số chương)
	chars, err := t.store.Characters.Load()
	if err != nil {
		reads.warn("related_chapters.characters", err)
	}
	outlineChars := matchOutlineCharacters(outlineText, chars)
	if len(outlineChars) > 0 {
		appearances, err := t.store.Summaries.FindCharacterAppearances(outlineChars, chapter, recentWindow)
		if err != nil {
			reads.warn("related_chapters.summaries", err)
		}
		for _, name := range outlineChars {
			if len(results) >= maxResults {
				break
			}
			if ch, ok := appearances[name]; ok {
				add(ch, fmt.Sprintf("chương nhân vật '%s' xuất hiện cuối", name))
			}
		}
	}

	// 3. Tra ngược thay đổi trạng thái: thao tác trên slice đã tải, không IO
	for _, name := range outlineChars {
		if len(results) >= maxResults {
			break
		}
		ch := findLastStateChange(stateChanges, name, chapter)
		if ch > 0 && ch <= chapter-recentWindow {
			add(ch, fmt.Sprintf("chương '%s' thay đổi trạng thái", name))
		}
	}

	// 4. Tra ngược quan hệ: lần thay đổi cuối của quan hệ giữa các cặp nhân vật liên quan chương hiện tại
	if len(relationships) > 0 && len(outlineChars) >= 2 {
		charSet := make(map[string]struct{}, len(outlineChars))
		for _, c := range outlineChars {
			charSet[c] = struct{}{}
		}
		for _, r := range relationships {
			if len(results) >= maxResults {
				break
			}
			_, aIn := charSet[r.CharacterA]
			_, bIn := charSet[r.CharacterB]
			if aIn && bIn {
				add(r.Chapter, fmt.Sprintf("quan hệ %s-%s thay đổi", r.CharacterA, r.CharacterB))
			}
		}
	}

	return results
}

// findLastStateChange tìm số chương lần thay đổi gần nhất của một thực thể trong danh sách thay đổi trạng thái đã tải.
func findLastStateChange(changes []domain.StateChange, entity string, currentChapter int) int {
	for i := len(changes) - 1; i >= 0; i-- {
		if changes[i].Entity == entity && changes[i].Chapter < currentChapter {
			return changes[i].Chapter
		}
	}
	return 0
}

// matchOutlineCharacters khớp tên nhân vật xuất hiện từ văn bản dàn ý.
func matchOutlineCharacters(text string, chars []domain.Character) []string {
	var matched []string
	for _, c := range chars {
		if strings.Contains(text, c.Name) {
			matched = append(matched, c.Name)
			continue
		}
		for _, alias := range c.Aliases {
			if strings.Contains(text, alias) {
				matched = append(matched, c.Name)
				break
			}
		}
	}
	return matched
}

// containsAny kiểm tra text có chứa từ nào trong words (ít nhất 2 ký tự mới khớp, tránh nhiễu).
func containsAny(text string, words []string) bool {
	for _, w := range words {
		if len([]rune(w)) >= 2 && strings.Contains(text, w) {
			return true
		}
	}
	return false
}

func (t *ContextTool) selectStoryThreads(state contextBuildState) []domain.RecallItem {
	if state.currentEntry == nil {
		return nil
	}
	if len(state.foreshadow) < storyThreadRecallThreshold {
		return nil
	}

	const maxThreads = 5
	var items []domain.RecallItem
	seen := make(map[string]struct{})
	picked := make(map[string]struct{}) // ID phục bút đã chọn, để khử trùng khi hồi chọn theo tuổi treo
	add := func(item domain.RecallItem) {
		key := item.Kind + "|" + item.Key + "|" + item.Summary
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		picked[item.Key] = struct{}{}
		items = append(items, item)
	}

	// 1. Tri hồi theo liên quan: phục bút trùng từ focus với chương hiện tại.
	focusTerms := recallFocusTerms(state.currentEntry, state.chapterPlan)
	focusText := strings.Join(focusTerms, " ")
	for _, entry := range state.foreshadow {
		if !matchesRecallTerms(entry.ID+" "+entry.Description, focusTerms) && !strings.Contains(focusText, entry.ID) {
			continue
		}
		add(domain.RecallItem{
			Kind:    "story_thread",
			Key:     entry.ID,
			Chapter: entry.PlantedAt,
			Reason:  "chương hiện tại có thể cần tiếp nối phục bút đã có",
			Summary: fmt.Sprintf("Phục bút \"%s\" cài ở chương %d: %s", entry.ID, entry.PlantedAt, truncateRunes(entry.Description, 30)),
		})
		if len(items) >= maxThreads {
			return items
		}
	}

	// 2. Hồi chọn theo tuổi treo: phục bút không liên quan chương hiện tại nhưng treo lâu chưa thu hồi (cũ nhất trước), lấp đầy phần chỉ định còn lại.
	//    Bù vào điểm mù tự nhiên của tri hồi theo liên quan — sợi dây treo một mình quá lâu mà không va từ khóa ở chương này.
	for _, entry := range agingForeshadow(state.foreshadow, state.chapter, picked) {
		add(domain.RecallItem{
			Kind:    "story_thread",
			Key:     entry.ID,
			Chapter: entry.PlantedAt,
			Reason:  "phục bút treo lâu chưa thu hồi, chú ý đẩy tiến độ hoặc thu hồi đúng lúc",
			Summary: fmt.Sprintf("Phục bút \"%s\" cài ở chương %d, đã %d chương chưa thu hồi: %s", entry.ID, entry.PlantedAt, state.chapter-entry.PlantedAt, truncateRunes(entry.Description, 30)),
		})
		if len(items) >= maxThreads {
			break
		}
	}

	return items
}

// agingForeshadow trả về phục bút chưa thu hồi có tuổi treo ≥ foreshadowAgingChapters, sắp cũ nhất trước,
// bỏ qua mục nằm trong picked đã được tri hồi theo liên quan chọn. Tham số all đã là danh sách active (chưa thu hồi) nên không cần lọc trạng thái lại.
func agingForeshadow(all []domain.ForeshadowEntry, chapter int, picked map[string]struct{}) []domain.ForeshadowEntry {
	var aging []domain.ForeshadowEntry
	for _, e := range all {
		if _, ok := picked[e.ID]; ok {
			continue
		}
		if e.PlantedAt <= 0 || chapter-e.PlantedAt < foreshadowAgingChapters {
			continue
		}
		aging = append(aging, e)
	}
	sort.SliceStable(aging, func(i, j int) bool {
		return aging[i].PlantedAt < aging[j].PlantedAt
	})
	return aging
}

func (t *ContextTool) selectReviewLessons(chapter int, reads *contextReads) []domain.RecallItem {
	if chapter <= 1 {
		return nil
	}

	var items []domain.RecallItem
	seen := make(map[string]struct{})
	add := func(item domain.RecallItem) {
		key := item.Summary
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		items = append(items, item)
	}

	appendReview := func(review *domain.ReviewEntry) bool {
		if review == nil {
			return false
		}
		for i, miss := range review.ContractMisses {
			add(domain.RecallItem{
				Kind:    "review_lesson",
				Key:     fmt.Sprintf("review-%d-contract-%d", review.Chapter, i),
				Chapter: review.Chapter,
				Reason:  "lần xem xét gần nhất chỉ ra thiếu mục contract",
				Summary: fmt.Sprintf("chương %d thiếu mục contract: %s", review.Chapter, miss),
			})
			if len(items) >= 3 {
				return true
			}
		}
		for i, issue := range review.Issues {
			switch issue.Severity {
			case "", "warning", "error", "critical":
				add(domain.RecallItem{
					Kind:    "review_lesson",
					Key:     fmt.Sprintf("review-%d-issue-%d", review.Chapter, i),
					Chapter: review.Chapter,
					Reason:  "lần xem xét gần nhất chỉ ra cần tránh vấn đề lặp lại",
					Summary: fmt.Sprintf("nhắc nhở xem xét chương %d: %s", review.Chapter, truncateRunes(issue.Description, 36)),
				})
			}
			if len(items) >= 3 {
				return true
			}
		}
		return false
	}

	for ch := chapter - 1; ch >= max(chapter-3, 1); ch-- {
		review, err := t.store.World.LoadReview(ch)
		if err != nil {
			reads.warn("review", err)
			continue
		}
		if appendReview(review) {
			return items
		}
	}

	globalReview, err := t.store.World.LoadLastReview(chapter - 1)
	if err != nil {
		reads.warn("global_review", err)
	} else if appendReview(globalReview) {
		return items
	}
	return items
}

func recallFocusTerms(entry *domain.OutlineEntry, plan *domain.ChapterPlan) []string {
	if entry == nil {
		return nil
	}
	var terms []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v != "" {
			terms = append(terms, v)
		}
	}

	add(entry.Title)
	add(entry.CoreEvent)
	add(entry.Hook)
	for _, scene := range entry.Scenes {
		add(scene)
	}
	if plan != nil {
		add(plan.Goal)
		add(plan.Hook)
		for _, point := range plan.Contract.PayoffPoints {
			add(point)
		}
		add(plan.Contract.HookGoal)
	}
	return terms
}

func matchesRecallTerms(text string, terms []string) bool {
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if len([]rune(term)) < 2 {
			continue
		}
		if strings.Contains(text, term) || strings.Contains(term, text) {
			return true
		}
		if hasMeaningfulOverlap(term, text) {
			return true
		}
	}
	return false
}

func hasMeaningfulOverlap(a, b string) bool {
	ar := []rune(strings.TrimSpace(a))
	br := []rune(strings.TrimSpace(b))
	if len(ar) < 5 || len(br) < 5 {
		return false
	}
	shorter := len(ar)
	if len(br) < shorter {
		shorter = len(br)
	}
	threshold := 5
	switch {
	case shorter >= 12:
		threshold = 7
	case shorter >= 9:
		threshold = 6
	}
	return longestCommonSubstringRunes(ar, br) >= threshold
}

const storyThreadRecallThreshold = 6
const storyThreadRecallMinSelected = 2

// foreshadowAgingChapters: một phục bút tính từ lúc cài mà qua nhiều chương như vậy vẫn chưa thu hồi thì xem là "treo lâu".
// Loại phục bút này dù không liên quan từ khóa của chương hiện tại cũng được hồi chọn vào story_threads, tránh bị quên sạch trong trường thiên
// (tri hồi theo liên quan tự nhiên chỉ thấy sợi dây liên quan chương này, không thấy sợi dây treo một mình quá lâu).
// Tuổi treo là sự thật thuần code suy ra (chương hiện tại - chương cài), chỉ trình bày "đã treo N chương chưa thu hồi", không ra lệnh.
const foreshadowAgingChapters = 30

func longestCommonSubstringRunes(a, b []rune) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1)
	best := 0
	for i := 1; i <= len(a); i++ {
		curr := make([]int, len(b)+1)
		for j := 1; j <= len(b); j++ {
			if a[i-1] != b[j-1] {
				continue
			}
			curr[j] = prev[j-1] + 1
			if curr[j] > best {
				best = curr[j]
			}
		}
		prev = curr
	}
	return best
}

// truncateRunes cắt chuỗi đến số rune chỉ định.
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
