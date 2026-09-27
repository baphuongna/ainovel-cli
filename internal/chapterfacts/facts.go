package chapterfacts

import (
	"fmt"
	"strings"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

// Properties trả về các trường JSON Schema dùng chung cho toàn bộ facts chương.
func Properties(includeFeedback bool) []schema.Prop {
	textList := func(description string) map[string]any {
		return schema.Array(description, schema.String(description))
	}
	timeline := schema.Object(
		schema.Property("time", schema.String("thời gian trong truyện")).Required(),
		schema.Property("event", schema.String("sự kiện")).Required(),
		schema.Property("characters", textList("nhân vật liên quan")).Required(),
	)
	foreshadow := schema.Object(
		schema.Property("id", schema.String("ID phục bút")).Required(),
		schema.Property("action", schema.Enum("thao tác", "plant", "advance", "resolve")).Required(),
		schema.Property("description", llmcontract.Nullable(schema.String("mô tả plant, các thao tác khác là null"))).Required(),
	)
	relationship := schema.Object(
		schema.Property("character_a", schema.String("nhân vật A")).Required(),
		schema.Property("character_b", schema.String("nhân vật B")).Required(),
		schema.Property("relation", schema.String("quan hệ khi chương này kết thúc")).Required(),
	)
	stateChange := schema.Object(
		schema.Property("entity", schema.String("thực thể")).Required(),
		schema.Property("field", schema.String("thuộc tính")).Required(),
		schema.Property("old_value", llmcontract.Nullable(schema.String("giá trị trước thay đổi"))).Required(),
		schema.Property("new_value", schema.String("giá trị sau thay đổi")).Required(),
		schema.Property("reason", llmcontract.Nullable(schema.String("nguyên nhân"))).Required(),
	)
	props := []schema.Prop{
		schema.Property("title", schema.String("tiêu đề cuối cùng")).Required(),
		schema.Property("summary", schema.String("tóm tắt chương")).Required(),
		schema.Property("characters", textList("nhân vật xuất hiện")).Required(),
		schema.Property("key_events", textList("sự kiện then chốt")).Required(),
		schema.Property("timeline_events", schema.Array("sự kiện dòng thời gian", timeline)).Required(),
		schema.Property("foreshadow_updates", schema.Array("thao tác phục bút", foreshadow)).Required(),
		schema.Property("relationship_changes", schema.Array("thay đổi quan hệ", relationship)).Required(),
		schema.Property("state_changes", schema.Array("thay đổi trạng thái", stateChange)).Required(),
		schema.Property("cast_intros", schema.Array("nhân vật phụ mới", schema.Object(
			schema.Property("name", schema.String("tên")).Required(),
			schema.Property("brief_role", schema.String("vai trò")).Required(),
		))).Required(),
		schema.Property("hook_type", llmcontract.Nullable(schema.Enum("móc cuối chương", domain.HookTypes()...))).Required(),
		schema.Property("dominant_strand", llmcontract.Nullable(schema.Enum("trục tường thuật chính", domain.DominantStrands()...))).Required(),
	}
	if includeFeedback {
		feedback := schema.Object(
			schema.Property("deviation", schema.String("mô tả lệch khỏi dàn ý")).Required(),
			schema.Property("suggestion", schema.String("đề xuất điều chỉnh dàn ý sau đó")).Required(),
		)
		feedback["description"] = "đối tượng đề xuất cho dàn ý sau; phải truyền trực tiếp JSON object, không truyền JSON đã stringify"
		props = append(props, schema.Property("feedback", llmcontract.Nullable(feedback)).Required())
	}
	return props
}

// Validate kiểm tra ràng buộc xác định dùng chung cho submission thường và sửa tay.
func Validate(facts domain.ChapterFacts) error {
	if strings.TrimSpace(facts.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(facts.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if len(facts.KeyEvents) == 0 {
		return fmt.Errorf("key_events must contain at least one event")
	}
	if err := validateTextItems("characters", facts.Characters); err != nil {
		return err
	}
	if err := validateTextItems("key_events", facts.KeyEvents); err != nil {
		return err
	}
	for i, event := range facts.TimelineEvents {
		if strings.TrimSpace(event.Time) == "" || strings.TrimSpace(event.Event) == "" {
			return fmt.Errorf("timeline_events[%d] requires time and event", i)
		}
		if err := validateTextItems(fmt.Sprintf("timeline_events[%d].characters", i), event.Characters); err != nil {
			return err
		}
	}
	for i, update := range facts.ForeshadowUpdates {
		if strings.TrimSpace(update.ID) == "" {
			return fmt.Errorf("foreshadow_updates[%d].id is required", i)
		}
		if placeholderForeshadowID(update.ID) {
			return fmt.Errorf(
				"foreshadow_updates[%d].id = %q là placeholder, không phải định danh. Mỗi phục bút cần một id duy nhất và ổn định "+
					"(VD FORESHADOW_OBSERVER, jade-pendant), các chương sau dựa vào nó để advance/resolve; "+
					"tái sử dụng placeholder khiến phục bút mới đâm vào bản ghi cũ",
				i, update.ID)
		}
		switch update.Action {
		case "plant":
			if strings.TrimSpace(update.Description) == "" {
				return fmt.Errorf("foreshadow_updates[%d] plant requires description", i)
			}
		case "advance", "resolve":
		default:
			return fmt.Errorf("foreshadow_updates[%d].action invalid: %q", i, update.Action)
		}
	}
	for i, change := range facts.RelationshipChanges {
		if strings.TrimSpace(change.CharacterA) == "" || strings.TrimSpace(change.CharacterB) == "" || strings.TrimSpace(change.Relation) == "" {
			return fmt.Errorf("relationship_changes[%d] requires character_a, character_b and relation", i)
		}
		if change.CharacterA == change.CharacterB {
			return fmt.Errorf("relationship_changes[%d] cannot relate a character to itself", i)
		}
	}
	for i, change := range facts.StateChanges {
		if strings.TrimSpace(change.Entity) == "" || strings.TrimSpace(change.Field) == "" || strings.TrimSpace(change.NewValue) == "" {
			return fmt.Errorf("state_changes[%d] requires entity, field and new_value", i)
		}
	}
	for i, intro := range facts.CastIntros {
		if strings.TrimSpace(intro.Name) == "" || strings.TrimSpace(intro.BriefRole) == "" {
			return fmt.Errorf("cast_intros[%d] requires name and brief_role", i)
		}
	}
	if facts.HookType != "" && !domain.ValidHookType(facts.HookType) {
		return fmt.Errorf("invalid hook_type %q", facts.HookType)
	}
	if facts.DominantStrand != "" && !domain.ValidDominantStrand(facts.DominantStrand) {
		return fmt.Errorf("invalid dominant_strand %q", facts.DominantStrand)
	}
	if facts.Feedback != nil && (strings.TrimSpace(facts.Feedback.Deviation) == "" || strings.TrimSpace(facts.Feedback.Suggestion) == "") {
		return fmt.Errorf("feedback requires deviation and suggestion")
	}
	return nil
}

func validateTextItems(name string, items []string) error {
	for i, item := range items {
		if strings.TrimSpace(item) == "" {
			return fmt.Errorf("%s[%d] cannot be empty", name, i)
		}
	}
	return nil
}

// placeholderForeshadowIDs là các từ placeholder mà model hay viết vào trường id. Chúng không có tác dụng định danh,
// mà id phục bút phải ổn định xuyên chương — thực nghiệm model đặt mọi phục bút mới là "new", từ cái thứ hai trở
// đi đâm vào cái thứ nhất, bị nuốt im lặng: 22 chương plant 6 lần, cuối cùng chỉ vào sổ 2 cái.
var placeholderForeshadowIDs = map[string]struct{}{
	"new": {}, "none": {}, "null": {}, "nil": {}, "tbd": {}, "todo": {},
	"auto": {}, "id": {}, "-": {}, "n/a": {}, "na": {}, "unknown": {}, "?": {},
}

func placeholderForeshadowID(id string) bool {
	_, ok := placeholderForeshadowIDs[strings.ToLower(strings.TrimSpace(id))]
	return ok
}
