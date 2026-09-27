package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/store"
)

// SaveFoundationTool lưu thiết lập nền tảng (premise/outline/characters), chuyên dùng cho Architect.
type SaveFoundationTool struct {
	store *store.Store
}

func NewSaveFoundationTool(store *store.Store) *SaveFoundationTool {
	return &SaveFoundationTool{store: store}
}

func (t *SaveFoundationTool) Name() string { return "save_foundation" }
func (t *SaveFoundationTool) Description() string {
	return "Lưu thiết lập nền tảng của tiểu thuyết (premise/outline/characters/world_rules/compass v.v.). **Đây là lối vào lưu trữ duy nhất**: nội dung chưa qua công cụ này sẽ không vào store, chỉ xuất Markdown/JSON trong tin nhắn coi như mất. Tham số cố định {type, content, scale?, volume?, arc?}. type chọn premise / outline / layered_outline / characters / world_rules / expand_arc / append_volume / update_compass / complete_book. Với premise, content phải là chuỗi Markdown; các loại khác ưu tiên truyền thẳng mảng hoặc đối tượng JSON. expand_arc hiệu chỉnh và triển khai một cung khung chưa viết (số chương chi tiết một cung không quá 8 chương — xem xét cuối cung cần đọc trọn cung trong một lượt, quá dài không thể xem xét; vượt hạn hãy tách thành nhiều cung, cần volume + arc, content là {title, goal, chapters}, có thể sửa mục tiêu khung gốc theo chính văn đã hoàn thành); append_volume thêm quyển mới (content là JSON VolumeOutline đầy đủ, gồm cấu trúc cung; đỉnh tầng kèm \"final\": true tức tuyên bố quyển kết thúc — toàn sách khép trong quyển đó, viết xong mọi chương sẽ tự hoàn thành, không cần gọi complete_book nữa); update_compass cập nhật hướng kết cục (content là JSON StoryCompass, trường chỉ gồm {ending_direction: string bắt buộc, open_threads?: string[], estimated_scale?: string}, các trường khác nhất luật không nhận); complete_book tuyên bố toàn sách hoàn thành (content truyền đối tượng rỗng {}, đẩy thẳng Phase=Complete; công cụ sẽ kiểm tra: mọi chương trong dàn ý đã viết xong, không còn hàng đợi viết lại, compass không còn open_threads chưa khép — xác nhận tuyến dài đã khép phải trước tiên update_compass dọn open_threads rồi ghi xuống đĩa, muốn khép sớm hãy dùng quyển kết thúc final của append_volume). append_volume / complete_book phải kèm tham số reason (một câu lý do phán định, đối chiếu danh sách phán định hoàn thành, ghi vào kiểm toán phán định). scale tùy chọn, chỉ cho short / mid / long."
}
func (t *SaveFoundationTool) Label() string { return "Lưu thiết lập" }

// Công cụ ghi (cập nhật xuyên miền Outline/Progress/Characters), cấm đồng thời.
func (t *SaveFoundationTool) ReadOnly(_ json.RawMessage) bool        { return false }
func (t *SaveFoundationTool) ConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *SaveFoundationTool) Schema() map[string]any {
	return schema.Object(
		schema.Property("type", schema.Enum("Loại thiết lập", "premise", "outline", "layered_outline", "characters", "world_rules", "expand_arc", "append_volume", "update_compass", "complete_book")).Required(),
		schema.Property("content", map[string]any{
			"description": "Nội dung. premise truyền chuỗi Markdown; các loại khác truyền thẳng mảng hoặc đối tượng JSON là được, cũng tương thích truyền chuỗi JSON. Với expand_arc truyền {title, goal, chapters}, title/goal là quy hoạch cung mục tiêu đã hiệu chỉnh theo sự kiện đã hoàn thành.",
		}).Required(),
		schema.Property("scale", schema.Enum("Cấp quy hoạch", "short", "mid", "long")),
		schema.Property("volume", schema.Int("Số thứ tự quyển mục tiêu, tính từ 1 (bắt buộc khi expand_arc)")),
		schema.Property("arc", schema.Int("Số thứ tự cung mục tiêu trong quyển, tính từ 1 (bắt buộc khi expand_arc)")),
		schema.Property("reason", schema.String("Lý do phán định cuối quyển (bắt buộc với append_volume / complete_book): đối chiếu danh sách phán định hoàn thành, một câu nói rõ vì sao tiếp quyển, tuyên bố kết thúc hay hoàn thành")),
	)
}

func (t *SaveFoundationTool) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
		Scale   string          `json:"scale"`
		Volume  int             `json:"volume"`
		Arc     int             `json:"arc"`
		Reason  string          `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	content, err := normalizeFoundationContent(a.Content)
	if err != nil {
		return nil, err
	}
	if a.Scale != "" {
		switch domain.PlanningTier(a.Scale) {
		case domain.PlanningTierShort, domain.PlanningTierMid, domain.PlanningTierLong:
		default:
			return nil, fmt.Errorf("invalid scale %q, expected short/mid/long: %w", a.Scale, errs.ErrToolArgs)
		}
	}

	result := map[string]any{"saved": true, "type": a.Type, "scale": a.Scale}

	// Dàn ý toàn lượng chỉ thuộc giai đoạn quy hoạch. Giai đoạn viết phải dùng thao tác gia tăng được bảo vệ, sau khi hoàn thành phải mở lại trước;
	// nếu không sẽ lách qua lớp bảo vệ chương đã hoàn thành, phá vỡ nhất quán giữa Progress và sự kiện chương.
	progress, err := t.store.Progress.Load()
	if err != nil {
		return nil, fmt.Errorf("check foundation phase: %w: %w", errs.ErrStoreRead, err)
	}
	if (a.Type == "outline" || a.Type == "layered_outline") && progress != nil {
		switch progress.Phase {
		case domain.PhaseWriting:
			return nil, fmt.Errorf(
				"Giai đoạn viết cấm dùng %s ghi đè toàn bộ dàn ý. Hãy dùng revise_outline sửa chương chưa xảy ra, expand_arc triển khai cung khung, hoặc append_volume thêm quyển mới: %w",
				a.Type, errs.ErrToolPrecondition)
		case domain.PhaseComplete:
			return nil, fmt.Errorf(
				"Toàn sách đã hoàn thành, cấm dùng %s ghi đè toàn bộ dàn ý. Hãy mở lại tác phẩm trước, rồi dùng thao tác sửa dàn ý hoặc tiếp viết được bảo vệ: %w",
				a.Type, errs.ErrToolPrecondition)
		}
	}
	if a.Scale != "" {
		if err := t.store.RunMeta.SetPlanningTier(domain.PlanningTier(a.Scale)); err != nil {
			return nil, fmt.Errorf("save planning tier: %w: %w", errs.ErrStoreWrite, err)
		}
	}

	// Ba chọn một cuối quyển (tiếp quyển/kết thúc/hoàn thành) là phán đoán ngữ nghĩa nặng nhất toàn sách, lý do phải thành sự kiện kiểm toán
	// (decisions.jsonl, cùng một dòng chảy với plan_start/intervention), nếu không kết thúc quá sớm/
	// tiếp quyển sai chỉ biết đi lục log phiên để truy vết. Snapshot sự kiện lấy tiến độ tại thời điểm phán định (trước khi thay đổi ghi xuống đĩa).
	volumeEnd := a.Type == "append_volume" || a.Type == "complete_book"
	if volumeEnd && strings.TrimSpace(a.Reason) == "" {
		return nil, fmt.Errorf("%s phải kèm tham số reason: đối chiếu danh sách phán định hoàn thành, một câu nói rõ lần này vì sao tiếp quyển, tuyên bố kết thúc hay hoàn thành: %w", a.Type, errs.ErrToolArgs)
	}
	var volumeEndFacts json.RawMessage
	if volumeEnd {
		p, err := t.store.Progress.Load()
		if err != nil {
			return nil, fmt.Errorf("load progress for volume-end facts: %w: %w", errs.ErrStoreRead, err)
		}
		if p != nil {
			facts := map[string]any{"completed_chapters": len(p.CompletedChapters)}
			if p.Layered {
				outline, outlineErr := t.store.Outline.LoadOutline()
				if outlineErr != nil {
					return nil, fmt.Errorf("load outlined chapters for volume-end facts: %w: %w", errs.ErrStoreRead, outlineErr)
				}
				facts["dynamic_planning"] = true
				facts["outlined_chapters"] = len(outline)
			} else {
				facts["total_chapters"] = p.TotalChapters
			}
			volumeEndFacts, err = json.Marshal(facts)
			if err != nil {
				return nil, fmt.Errorf("marshal volume-end facts: %w", err)
			}
		}
	}

	decode := func(typeName string, out any) error {
		return decodeFoundationJSON(typeName, content, out)
	}

	switch a.Type {
	case "premise":
		if err := t.store.Outline.SavePremise(content); err != nil {
			return nil, fmt.Errorf("save premise: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Progress.AdvancePhase(domain.PhasePremise); err != nil {
			return nil, fmt.Errorf("update premise phase: %w: %w", errs.ErrStoreWrite, err)
		}

	case "outline":
		var entries []domain.OutlineEntry
		if err := decode("outline", &entries); err != nil {
			return nil, err
		}
		if defect := domain.StalledOutline(entries); defect != "" {
			return nil, fmt.Errorf("%s: %w", defect, errs.ErrToolArgs)
		}
		if err := t.store.Outline.SaveOutline(entries); err != nil {
			return nil, fmt.Errorf("save outline: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Progress.AdvancePhase(domain.PhaseOutline); err != nil {
			return nil, fmt.Errorf("update outline phase: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Progress.SetTotalChapters(len(entries)); err != nil {
			return nil, fmt.Errorf("set total chapters: %w: %w", errs.ErrStoreWrite, err)
		}
		if domain.PlanningTier(a.Scale) != domain.PlanningTierLong {
			if err := t.store.Progress.SetLayered(false); err != nil {
				return nil, fmt.Errorf("disable layered mode: %w: %w", errs.ErrStoreWrite, err)
			}
			if err := t.store.Progress.UpdateVolumeArc(0, 0); err != nil {
				return nil, fmt.Errorf("reset volume/arc: %w: %w", errs.ErrStoreWrite, err)
			}
			if err := t.store.Outline.ClearLayeredOutline(); err != nil {
				return nil, fmt.Errorf("clear layered outline: %w: %w", errs.ErrStoreWrite, err)
			}
		}
		result["chapters"] = len(entries)

	case "layered_outline":
		var volumes []domain.VolumeOutline
		if err := decode("layered_outline", &volumes); err != nil {
			return nil, err
		}
		for vi := range volumes {
			for ai := range volumes[vi].Arcs {
				arc := &volumes[vi].Arcs[ai]
				if defect := domain.OversizedArc(
					fmt.Sprintf("quyển %d cung %d", volumes[vi].Index, arc.Index), arcPlannedSize(arc)); defect != "" {
					return nil, fmt.Errorf("%s: %w", defect, errs.ErrToolArgs)
				}
			}
		}
		if defect := domain.StalledOutline(domain.FlattenOutline(volumes)); defect != "" {
			return nil, fmt.Errorf("%s: %w", defect, errs.ErrToolArgs)
		}
		if err := t.store.Outline.SaveLayeredOutline(volumes); err != nil {
			return nil, fmt.Errorf("save layered_outline: %w: %w", errs.ErrStoreWrite, err)
		}
		total := domain.EstimatedChapterCapacity(volumes)
		if err := t.store.Progress.AdvancePhase(domain.PhaseOutline); err != nil {
			return nil, fmt.Errorf("update outline phase: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Progress.SetTotalChapters(total); err != nil {
			return nil, fmt.Errorf("set total chapters: %w: %w", errs.ErrStoreWrite, err)
		}
		if err := t.store.Progress.SetLayered(true); err != nil {
			return nil, fmt.Errorf("enable layered mode: %w: %w", errs.ErrStoreWrite, err)
		}
		if len(volumes) > 0 && len(volumes[0].Arcs) > 0 {
			if err := t.store.Progress.UpdateVolumeArc(volumes[0].Index, volumes[0].Arcs[0].Index); err != nil {
				return nil, fmt.Errorf("set initial volume/arc: %w: %w", errs.ErrStoreWrite, err)
			}
		}
		result["volumes"] = len(volumes)
		result["dynamic_planning"] = true
		result["outlined_chapters"] = len(domain.FlattenOutline(volumes))

	case "characters":
		var chars []domain.Character
		if err := decode("characters", &chars); err != nil {
			return nil, err
		}
		if err := t.store.Characters.Save(chars); err != nil {
			return nil, fmt.Errorf("save characters: %w: %w", errs.ErrStoreWrite, err)
		}
		result["count"] = len(chars)

	case "world_rules":
		var rules []domain.WorldRule
		if err := decode("world_rules", &rules); err != nil {
			return nil, err
		}
		if err := t.store.World.SaveWorldRules(rules); err != nil {
			return nil, fmt.Errorf("save world_rules: %w: %w", errs.ErrStoreWrite, err)
		}
		result["count"] = len(rules)

	case "expand_arc":
		if a.Volume <= 0 || a.Arc <= 0 {
			return nil, fmt.Errorf(
				"expand_arc cần volume và arc (số quyển/số cung đều tính từ 1, nhận volume=%d arc=%d);"+
					"hãy gọi novel_context trước, dùng giá trị index của cung mục tiêu trong layered_outline điền vào: %w",
				a.Volume, a.Arc, errs.ErrToolArgs)
		}
		var expansion domain.ArcExpansion
		if err := decode("expand_arc", &expansion); err != nil {
			return nil, err
		}
		if defect := domain.OversizedArc(
			fmt.Sprintf("quyển %d cung %d", a.Volume, a.Arc), len(expansion.Chapters)); defect != "" {
			return nil, fmt.Errorf("%s: %w", defect, errs.ErrToolArgs)
		}
		if err := t.store.ExpandArc(a.Volume, a.Arc, expansion); err != nil {
			return nil, fmt.Errorf("expand arc: %w: %w", errs.ErrStoreWrite, err)
		}
		result["volume"] = a.Volume
		result["arc"] = a.Arc
		result["title"] = expansion.Title
		result["goal"] = expansion.Goal
		result["chapters"] = len(expansion.Chapters)
		if err := t.consumeWriterFeedback(); err != nil {
			return nil, err
		}

	case "append_volume":
		p, err := t.store.Progress.Load()
		if err != nil {
			return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
		}
		if p != nil && p.Phase == domain.PhaseComplete {
			return nil, fmt.Errorf("Toàn sách đã hoàn thành (phase=complete), không cho thêm quyển mới: %w", errs.ErrToolPrecondition)
		}
		var vol domain.VolumeOutline
		if err := decode("append_volume", &vol); err != nil {
			return nil, err
		}
		for i := range vol.Arcs {
			arc := &vol.Arcs[i]
			if defect := domain.OversizedArc(
				fmt.Sprintf("cung %d của quyển mới", arc.Index), arcPlannedSize(arc)); defect != "" {
				return nil, fmt.Errorf("%s: %w", defect, errs.ErrToolArgs)
			}
		}
		prior, err := t.store.Outline.LoadLayeredOutline()
		if err != nil {
			return nil, fmt.Errorf("load layered outline: %w: %w", errs.ErrStoreRead, err)
		}
		if err := t.store.AppendVolume(vol); err != nil {
			return nil, fmt.Errorf("append volume: %w: %w", errs.ErrStoreWrite, err)
		}
		result["volume"] = vol.Index
		if vol.Final {
			result["final_volume"] = true
		} else if domain.FinaleVolume(prior) > 0 {
			// Hiển thị lại sự kiện: trạng thái kết thúc đã tuyên bố trước đó được thả vì thêm quyển mới thường (quyển mới thành quyển cuối)
			result["finale_released"] = true
		}
		result["arcs"] = len(vol.Arcs)
		chCount := 0
		for _, arc := range vol.Arcs {
			chCount += len(arc.Chapters)
		}
		if chCount > 0 {
			result["chapters"] = chCount
		}
		if err := t.consumeWriterFeedback(); err != nil {
			return nil, err
		}

	case "complete_book":
		// Lối vào duy nhất hoàn thành toàn sách: đẩy thẳng Phase=Complete.
		// Chỉ cho ở giai đoạn Writing, phòng gọi nhầm ở giai đoạn quy hoạch bỏ qua toàn bộ việc viết.
		// Từ chối gọi khi còn hàng đợi viết lại — bảo đảm PendingRewrites chạy xong mới được kết thúc.
		progress, perr := t.store.Progress.Load()
		if perr != nil {
			return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, perr)
		}
		if progress == nil {
			return nil, fmt.Errorf("progress chưa khởi tạo: %w", errs.ErrToolPrecondition)
		}
		if progress.Phase != domain.PhaseWriting {
			return nil, fmt.Errorf("complete_book chỉ gọi được ở giai đoạn writing (hiện phase=%s): %w", progress.Phase, errs.ErrToolPrecondition)
		}
		if len(progress.PendingRewrites) > 0 {
			return nil, fmt.Errorf("Còn %d chương trong hàng đợi viết lại, xử lý xong hãy gọi complete_book: %w", len(progress.PendingRewrites), errs.ErrToolPrecondition)
		}
		// Kiểm tra điều kiện trước khi hoàn thành có thể đếm được phải nằm ở tầng code (tam phân), không thể chỉ dựa vào
		// "danh sách phán định hoàn thành" trong prompt — sự cố thật: quy hoạch vừa ghi xuống đĩa thì phase lật sang writing, mô hình yếu tiện tay
		// gọi nhầm complete_book, 0/68 chương bị đánh dấu hoàn thành thẳng.
		if len(progress.CompletedChapters) == 0 {
			return nil, fmt.Errorf("Chưa viết chương nào thì không thể hoàn thành; sau khi quy hoạch xong, việc viết do hệ thống tự đẩy tiến, không cần gọi complete_book: %w", errs.ErrToolPrecondition)
		}
		next := progress.NextChapter()
		if progress.Layered {
			outline, outlineErr := t.store.Outline.LoadOutline()
			if outlineErr != nil {
				return nil, fmt.Errorf("load outlined chapters: %w: %w", errs.ErrStoreRead, outlineErr)
			}
			if next <= len(outline) {
				return nil, fmt.Errorf("Dàn ý chi tiết hiện tại còn chương chưa viết (chương kế %d/đã chi tiết hóa %d), không thể hoàn thành; muốn khép sớm hãy đổi sang append_volume với JSON quyển đỉnh tầng kèm \"final\": true tuyên bố quyển kết thúc: %w", next, len(outline), errs.ErrToolPrecondition)
			}
			// Dàn ý phẳng chỉ chứa cung đã triển khai, cung khung vô hình hoàn toàn với phép so ở trên; không chặn riêng một lần,
			// thì cả quyển chưa triển khai cũng tuyên bố hoàn thành được.
			volumes, volErr := t.store.Outline.LoadLayeredOutline()
			if volErr != nil {
				return nil, fmt.Errorf("load layered outline: %w: %w", errs.ErrStoreRead, volErr)
			}
			if skeletons := domain.SkeletonArcs(volumes); len(skeletons) > 0 {
				return nil, fmt.Errorf("Còn %d cung khung chưa triển khai (ví dụ: %s), không thể hoàn thành;"+
					"hãy expand_arc triển khai và viết xong trước, hoặc dùng append_volume kèm \"final\": true tuyên bố quyển kết thúc: %w",
					len(skeletons), skeletons[0], errs.ErrToolPrecondition)
			}
		} else if progress.TotalChapters > 0 && next <= progress.TotalChapters {
			return nil, fmt.Errorf("Trong dàn ý còn chương chưa viết (chương kế %d/tổng %d), không thể hoàn thành; muốn khép sớm hãy đổi sang append_volume với JSON quyển đỉnh tầng kèm \"final\": true tuyên bố quyển kết thúc: %w", next, progress.TotalChapters, errs.ErrToolPrecondition)
		}
		// Tuyến dài còn hoạt động chưa khép thì không thể hoàn thành — khớp trường của OpenThreads tức là "phải khép mới được kết thúc". Đây không phải
		// phán xét lại ngữ nghĩa: nếu thực sự cho là đã khép hết, hãy update_compass dọn open_threads trước rồi hoàn thành, biến
		// "miễn trừ bằng lập luận" thành thao tác ghi đĩa kiểm toán được (thực đo khi tiếp viết sách đã hoàn thành nhập vào, kiến trúc sư trích kinh dẫn điển lách qua
		// điều 3 danh sách hoàn thành mà hoàn thành thẳng, nhu cầu tiếp viết của người dùng bị khóa cứng bởi quy tắc hoàn thành).
		compass, err := t.store.Outline.LoadCompass()
		if err != nil {
			return nil, fmt.Errorf("load compass: %w: %w", errs.ErrStoreRead, err)
		}
		if compass != nil && len(compass.OpenThreads) > 0 {
			return nil, fmt.Errorf("compass còn %d tuyến dài hoạt động chưa khép (ví dụ: %s), không thể hoàn thành. Xác nhận đã khép hết thì update_compass dọn open_threads trước rồi gọi complete_book; vẫn cần triển khai thì append_volume (có thể kèm \"final\": true tuyên bố quyển kết thúc): %w",
				len(compass.OpenThreads), compass.OpenThreads[0], errs.ErrToolPrecondition)
		}
		if err := t.store.Progress.MarkComplete(); err != nil {
			return nil, fmt.Errorf("mark complete: %w: %w", errs.ErrStoreWrite, err)
		}
		result["book_complete"] = true
		result["phase"] = string(domain.PhaseComplete)

	case "update_compass":
		var compass domain.StoryCompass
		if err := decode("compass", &compass); err != nil {
			return nil, err
		}
		// Tầng công cụ ghi đè bắt buộc LastUpdated bằng số chương đã hoàn thành hiện tại, không tin LLM tự điền.
		// LLM thường quên điền hoặc để 0, khiến diag.CompassDrift báo sai, định tuyến Router méo mó.
		p, err := t.store.Progress.Load()
		if err != nil {
			return nil, fmt.Errorf("load progress: %w: %w", errs.ErrStoreRead, err)
		}
		if p != nil {
			compass.LastUpdated = p.LatestCompleted()
		}
		if err := t.store.Outline.SaveCompass(compass); err != nil {
			return nil, fmt.Errorf("save compass: %w: %w", errs.ErrStoreWrite, err)
		}
		result["ending_direction"] = compass.EndingDirection
		result["last_updated"] = compass.LastUpdated
		if err := t.consumeWriterFeedback(); err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("unknown type %q, expected premise/outline/layered_outline/characters/world_rules/expand_arc/append_volume/update_compass/complete_book: %w", a.Type, errs.ErrToolArgs)
	}

	// checkpoint
	scope := domain.GlobalScope()
	if a.Type == "expand_arc" {
		scope = domain.ArcScope(a.Volume, a.Arc)
	} else if a.Type == "append_volume" {
		scope = domain.GlobalScope()
	}
	if _, err := t.store.Checkpoints.AppendArtifact(scope, a.Type, foundationArtifact(a.Type)); err != nil {
		return nil, fmt.Errorf("checkpoint foundation %s: %w: %w", a.Type, errs.ErrStoreWrite, err)
	}

	if volumeEnd {
		t.recordVolumeEndDecision(a.Type, a.Reason, volumeEndFacts, result)
	}

	// Trả về các mục còn chưa hoàn thành. Sau khi tác phẩm khởi tạo đủ vẫn còn lại foundation_audit; chỉ khi
	// audit_foundation trả ready=true cho phiên bản thực tế ghi xuống đĩa, mới cho vào writing.
	remaining, err := t.store.FoundationMissing()
	if err != nil {
		return nil, fmt.Errorf("load foundation state: %w: %w", errs.ErrStoreRead, err)
	}
	ready := len(remaining) == 0
	result["remaining"] = remaining
	result["foundation_ready"] = ready
	return json.Marshal(result)
}

func foundationArtifact(t string) string {
	switch t {
	case "premise":
		return "premise.md"
	case "outline":
		return "outline.json"
	case "layered_outline", "expand_arc", "append_volume":
		return "layered_outline.json"
	case "complete_book":
		return "meta/progress.json"
	case "characters":
		return "characters.json"
	case "world_rules":
		return "world_rules.json"
	case "update_compass":
		return "meta/compass.json"
	default:
		return ""
	}
}

// decodeFoundationJSON phân tích trường content của save_foundation, khi thất bại đính kèm vị trí dòng cột
// và gợi ý sửa phổ biến nhất, để lần thử lại kế tiếp của LLM định vị trực tiếp thay vì đoán mò.
func decodeFoundationJSON(typeName, content string, out any) error {
	err := json.Unmarshal([]byte(content), out)
	if err == nil {
		return nil
	}
	hint := `Nguyên nhân thường gặp: dấu nháy kép trong giá trị chuỗi chưa thoát thành \", xuống dòng chưa thoát thành \n, hoặc thiếu dấu phẩy giữa các trường của đối tượng. Hãy regenerate nguyên đoạn một lần.`
	if se, ok := err.(*json.SyntaxError); ok {
		line, col := offsetToLineCol(content, int(se.Offset))
		return fmt.Errorf("parse %s JSON (line %d col %d): %w — %s", typeName, line, col, err, hint)
	}
	return fmt.Errorf("parse %s JSON: %w — %s", typeName, err, hint)
}

func offsetToLineCol(s string, offset int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(s) {
		offset = len(s)
	}
	line, col := 1, 1
	for i := 0; i < offset; i++ {
		if s[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

func normalizeFoundationContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("content is required: %w", errs.ErrToolArgs)
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}

	if !json.Valid(raw) {
		return "", fmt.Errorf("invalid content: expected Markdown string or valid JSON value: %w", errs.ErrToolArgs)
	}
	return string(raw), nil
}

// recordVolumeEndDecision ghi lý do phán định của ba chọn một cuối quyển (tiếp quyển/kết thúc/hoàn thành) vào kiểm toán phán định.
// best-effort: thay đổi cấu trúc đã ghi xuống đĩa, kiểm toán thất bại chỉ cảnh báo không hoàn tác — báo lỗi sẽ khiến mô hình thử lại thao tác đã hoàn thành
// (thêm quyển lặp lại).
func (t *SaveFoundationTool) recordVolumeEndDecision(action, reason string, facts json.RawMessage, result map[string]any) {
	decision := map[string]any{"action": action}
	if v, ok := result["volume"]; ok {
		decision["volume"] = v
	}
	if _, ok := result["final_volume"]; ok {
		decision["final"] = true
	}
	raw, err := json.Marshal(decision)
	if err != nil {
		slog.Error("phán định cuối quyển tuần tự hóa thất bại", "module", "tools", "action", action, "err", err)
		return
	}
	if _, err := t.store.Decisions.Append(store.DecisionRecord{
		Kind:     "volume_end",
		Decider:  "architect",
		Facts:    facts,
		Decision: raw,
		Reason:   reason,
	}); err != nil {
		slog.Error("phán định cuối quyển ghi kiểm toán xuống đĩa thất bại", "module", "tools", "action", action, "err", err)
	}
}

// consumeWriterFeedback dọn phản hồi quy hoạch đã xử lý sau khi thao tác cấu trúc thành công.
func (t *SaveFoundationTool) consumeWriterFeedback() error {
	if err := t.store.Outline.ClearOutlineFeedback(); err != nil {
		return fmt.Errorf("clear outline feedback: %w: %w", errs.ErrStoreWrite, err)
	}
	return nil
}

// arcPlannedSize lấy quy mô thực của cung: đã triển khai dùng số chương chi tiết, vẫn là khung thì dùng số chương ước tính.
func arcPlannedSize(arc *domain.ArcOutline) int {
	if n := len(arc.Chapters); n > 0 {
		return n
	}
	return arc.EstimatedChapters
}
