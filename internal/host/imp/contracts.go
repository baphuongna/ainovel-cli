package imp

import (
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

func nullableString(description string) map[string]any {
	return llmcontract.Nullable(schema.String(description))
}

func stringList(description string) map[string]any {
	return schema.Array(description, schema.String(description))
}

var segmentContract = llmcontract.Contract{
	Name:        "import_segment",
	Description: "nhận diện ranh giới chương, tập/phần và văn bản phụ trợ trong văn bản nhập",
	Schema: schema.Object(
		schema.Property("boundaries", schema.Array("ranh giới xếp theo thứ tự nguyên văn", schema.Object(
			schema.Property("unit_id", schema.String("unit id trong khoảng owned")).Required(),
			schema.Property("anchor", nullableString("đoạn định vị trích nguyên văn khi một unit có nhiều ranh giới; ngược lại để null")).Required(),
			schema.Property("kind", schema.Enum("loại ranh giới", kindChapter, kindGroup, kindFrontMatter, kindBackMatter)).Required(),
			schema.Property("title", nullableString("tiêu đề nguyên văn; null khi không có tiêu đề")).Required(),
			schema.Property("uncertain", schema.Bool("có cần người dùng xác nhận hay không")).Required(),
			schema.Property("reason", nullableString("lý do chưa chắc chắn; null khi không cần giải thích")).Required(),
		))).Required(),
	),
}

var analysisContract = llmcontract.Contract{
	Name:        "import_chapter_analysis",
	Description: "trích xuất sự thực truy vết được của các chương liên tiếp",
	Schema: schema.Object(
		schema.Property("chapters", schema.Array("sự thực từng chương theo đúng thứ tự số chương đầu vào", chapterFactsSchema())).Required(),
	),
}

func chapterFactsSchema() map[string]any {
	characterEvidence := schema.Object(
		schema.Property("chapter", schema.Int("chương chứa bằng chứng")).Required(),
		schema.Property("name", schema.String("tên nhân vật")).Required(),
		schema.Property("note", nullableString("sự thực về nhân vật; null nếu không có")).Required(),
	)
	worldEvidence := schema.Object(
		schema.Property("chapter", schema.Int("chương chứa bằng chứng")).Required(),
		schema.Property("category", nullableString("loại sự thực thế giới; null khi không phân loại được")).Required(),
		schema.Property("fact", schema.String("sự thực thế giới mà chính văn tiết lộ rõ ràng")).Required(),
	)
	timelineEvent := schema.Object(
		schema.Property("chapter", schema.Int("số chương")).Required(),
		schema.Property("time", schema.String("thời gian trong truyện")).Required(),
		schema.Property("event", schema.String("sự kiện")).Required(),
		schema.Property("characters", stringList("nhân vật liên quan")).Required(),
	)
	foreshadow := schema.Object(
		schema.Property("id", schema.String("tái sử dụng ID phục bút đã có trong ledger")).Required(),
		schema.Property("action", schema.Enum("hành động phục bút", "plant", "advance", "resolve")).Required(),
		schema.Property("description", nullableString("mô tả phục bút khi plant; các trường hợp khác có thể null")).Required(),
	)
	relationship := schema.Object(
		schema.Property("character_a", schema.String("nhân vật A")).Required(),
		schema.Property("character_b", schema.String("nhân vật B")).Required(),
		schema.Property("relation", schema.String("quan hệ thay đổi")).Required(),
		schema.Property("chapter", schema.Int("số chương")).Required(),
	)
	stateChange := schema.Object(
		schema.Property("chapter", schema.Int("số chương")).Required(),
		schema.Property("entity", schema.String("nhân vật hoặc thực thể")).Required(),
		schema.Property("field", schema.String("thuộc tính bị thay đổi")).Required(),
		schema.Property("old_value", nullableString("trạng thái trước thay đổi; null khi xuất hiện lần đầu")).Required(),
		schema.Property("new_value", schema.String("trạng thái sau thay đổi")).Required(),
		schema.Property("reason", nullableString("nguyên nhân thay đổi; null khi chính văn không nói rõ")).Required(),
	)
	return schema.Object(
		schema.Property("chapter", schema.Int("số chương")).Required(),
		schema.Property("title", schema.String("tiêu đề chương")).Required(),
		schema.Property("summary", schema.String("tóm tắt chương này")).Required(),
		schema.Property("key_events", stringList("sự kiện chính")).Required(),
		schema.Property("core_event", schema.String("sự kiện quan trọng nhất của chương")).Required(),
		schema.Property("hook", nullableString("móc treo cuối chương; null nếu không có")).Required(),
		schema.Property("scenes", stringList("trình tự cảnh")).Required(),
		schema.Property("characters", stringList("nhân vật xuất hiện")).Required(),
		schema.Property("character_evidence", schema.Array("bằng chứng nhân vật", characterEvidence)).Required(),
		schema.Property("world_evidence", schema.Array("bằng chứng sự thực thế giới", worldEvidence)).Required(),
		schema.Property("timeline_events", schema.Array("sự kiện dòng thời gian", timelineEvent)).Required(),
		schema.Property("foreshadow_updates", schema.Array("phục bút tăng thêm", foreshadow)).Required(),
		schema.Property("relationship_changes", schema.Array("thay đổi quan hệ", relationship)).Required(),
		schema.Property("state_changes", schema.Array("thay đổi trạng thái", stateChange)).Required(),
		schema.Property("hook_type", schema.Enum("loại móc treo cuối chương", domain.HookTypes()...)).Required(),
		schema.Property("dominant_strand", schema.Enum("mạch truyện chủ đạo", domain.DominantStrands()...)).Required(),
	)
}

var rangeContract = llmcontract.Contract{
	Name:        "import_range_digest",
	Description: "quy nạp cốt truyện và sự thực của một khoảng chương liên tiếp",
	Schema: schema.Object(
		schema.Property("start_chapter", schema.Int("chương đầu khoảng")).Required(),
		schema.Property("end_chapter", schema.Int("chương cuối khoảng")).Required(),
		schema.Property("plot", schema.String("tiến triển trục chính xuyên chương")).Required(),
		schema.Property("characters", stringList("nhân vật có tiến triển thực chất")).Required(),
		schema.Property("world_facts", stringList("sự thực thế giới đã được xác lập")).Required(),
		schema.Property("opened_threads", stringList("tuyến dài mới mở trong khoảng này")).Required(),
		schema.Property("resolved_threads", stringList("tuyến dài được khép lại trong khoảng này")).Required(),
	),
}

var synthesisContract = llmcontract.Contract{
	Name:        "import_book_synthesis",
	Description: "tổng hợp sự thực toàn sách và đưa ra phạm vi tập/cung liên tục hoàn chỉnh",
	Schema: schema.Object(
		schema.Property("title", nullableString("tên sách chính thức trong chính văn; null khi không xác nhận được")).Required(),
		schema.Property("synopsis", schema.String("lời giới thiệu không spoiler dành cho độc giả")).Required(),
		schema.Property("premise", schema.String("mô tả tiền đề truyện bằng Markdown")).Required(),
		schema.Property("characters", schema.Array("nhân vật chính", schema.Object(
			schema.Property("name", schema.String("tên nhân vật")).Required(),
			schema.Property("aliases", stringList("biệt danh và xưng hô")).Required(),
			schema.Property("role", schema.String("vai trò tự sự")).Required(),
			schema.Property("description", schema.String("mô tả nhân vật")).Required(),
			schema.Property("arc", schema.String("cung nhân vật")).Required(),
			schema.Property("traits", stringList("đặc điểm nhân vật")).Required(),
			schema.Property("tier", nullableString("tầng nhân vật; null khi không phán đoán được")).Required(),
		))).Required(),
		schema.Property("world_rules", schema.Array("quy tắc thế giới được chính văn xác lập", schema.Object(
			schema.Property("category", schema.String("loại quy tắc")).Required(),
			schema.Property("rule", schema.String("mô tả quy tắc")).Required(),
			schema.Property("boundary", schema.String("ranh giới không được vi phạm")).Required(),
		))).Required(),
		schema.Property("structure", schema.Array("phạm vi chương liên tục của tập và cung", schema.Object(
			schema.Property("title", schema.String("tiêu đề tập")).Required(),
			schema.Property("theme", schema.String("xung đột hoặc chủ đề lõi của tập")).Required(),
			schema.Property("arcs", schema.Array("cung truyện trong tập", schema.Object(
				schema.Property("title", schema.String("tiêu đề cung")).Required(),
				schema.Property("goal", schema.String("mục tiêu cung")).Required(),
				schema.Property("start_chapter", schema.Int("chương bắt đầu")).Required(),
				schema.Property("end_chapter", schema.Int("chương kết thúc")).Required(),
			))).Required(),
		))).Required(),
		schema.Property("compass", schema.Object(
			schema.Property("ending_direction", schema.String("hướng kết cục")).Required(),
			schema.Property("open_threads", stringList("tuyến dài chưa khép lại")).Required(),
			schema.Property("estimated_scale", nullableString("quy mô mơ hồ; null khi không phán đoán được")).Required(),
			schema.Property("last_updated", llmcontract.Nullable(schema.Int("số chương mới nhất làm căn cứ; null khi không cần điền"))).Required(),
		)).Required(),
		schema.Property("planning_tier", schema.Enum("tầng quy hoạch", "short", "mid", "long")).Required(),
		schema.Property("story_status", schema.Enum("truyện đã hoàn thành hay chưa", storyOpen, storyClosed, storyUncertain)).Required(),
		schema.Property("status_reason", nullableString("lý do phán định trạng thái")).Required(),
	),
}
