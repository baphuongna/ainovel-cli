package sim

import (
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
)

func textList(description string) map[string]any {
	return schema.Array(description, schema.String(description))
}

var sourceReportContract = llmcontract.Contract{
	Name:        "simulation_source_report",
	Description: "Trích xuất từ một bài ngữ liệu phương pháp viết có thể tái sử dụng mà không sao chép nguyên văn",
	Schema: schema.Object(
		schema.Property("title", llmcontract.Nullable(schema.String("Tiêu đề tùy chọn; để null khi không xác định được"))).Required(),
		schema.Property("summary", schema.String("Tóm tắt giá trị viết của văn bản mẫu")).Required(),
		schema.Property("style_observations", textList("Quan sát góc nhìn kể, cấu trúc câu và chất liệu miêu tả")).Required(),
		schema.Property("common_words", textList("Nhóm từ tần suất cao, hình tượng và từ chuyển cảnh")).Required(),
		schema.Property("plot_patterns", textList("Mẫu đẩy cốt truyện, bước ngoặt và leo thang xung đột")).Required(),
		schema.Property("hook_patterns", textList("Mẫu hook mở đầu, cuối chương và chênh lệch thông tin")).Required(),
		schema.Property("pacing_notes", textList("Mật độ cảnh và nhịp giải phóng thông tin")).Required(),
		schema.Property("reader_appeal", textList("Cách giữ chân người đọc tiếp tục đọc")).Required(),
		schema.Property("reusable_techniques", textList("Kỹ thuật cấu trúc đáng tham khảo")).Required(),
		schema.Property("warnings", textList("Rủi ro sao chép và áp dụng máy móc cần tránh")).Required(),
	),
}

var synthesisContract = llmcontract.Contract{
	Name:        "simulation_synthesis",
	Description: "Tổng hợp hồ sơ hiện có và các báo cáo ngữ liệu thành hồ sơ phương pháp mô phỏng văn phong khả thi",
	Schema: schema.Object(
		schema.Property("style", schema.Object(
			schema.Property("narrative_voice", textList("Ngôi kể, khoảng cách và kiểm soát thông tin")).Required(),
			schema.Property("sentence_rhythm", textList("Nhịp cấu trúc câu")).Required(),
			schema.Property("prose_texture", textList("Chất liệu miêu tả")).Required(),
			schema.Property("perspective", textList("Quy tắc góc nhìn")).Required(),
			schema.Property("mood", textList("Tông cảm xúc")).Required(),
			schema.Property("do_not_copy", textList("Nội dung cấm sao chép")).Required(),
		)).Required(),
		schema.Property("lexicon", schema.Object(
			schema.Property("common_words", textList("Nhóm từ thường dùng")).Required(),
			schema.Property("emotion_words", textList("Nhóm từ cảm xúc")).Required(),
			schema.Property("scene_words", textList("Nhóm từ cảnh")).Required(),
			schema.Property("transition_words", textList("Nhóm từ chuyển cảnh")).Required(),
			schema.Property("signature_phrases", textList("Đặc trưng giọng văn đã trừu tượng hóa, không chứa câu gốc")).Required(),
		)).Required(),
		schema.Property("plot_design", schema.Object(
			schema.Property("opening_patterns", textList("Cách mở đầu")).Required(),
			schema.Property("escalation_patterns", textList("Cách leo thang xung đột")).Required(),
			schema.Property("turning_point_patterns", textList("Thiết kế bước ngoặt")).Required(),
			schema.Property("payoff_patterns", textList("Cách thu hồi và trả nghiệm phục bút (payoff)")).Required(),
		)).Required(),
		schema.Property("hook_design", schema.Object(
			schema.Property("hook_types", textList("Loại hook")).Required(),
			schema.Property("placement", textList("Vị trí đặt hook")).Required(),
			schema.Property("cliffhanger_patterns", textList("Cách tạm dừng bằng hồi hộp (cliffhanger)")).Required(),
			schema.Property("payoff_rules", textList("Quy tắc trả nghiệm hook")).Required(),
		)).Required(),
		schema.Property("pacing_density", schema.Object(
			schema.Property("scene_density", textList("Mật độ thông tin trên một cảnh")).Required(),
			schema.Property("information_release", textList("Nhịp giải phóng thông tin")).Required(),
			schema.Property("dialogue_action_ratio", textList("Tỷ lệ đối thoại, hành động và nội tâm")).Required(),
			schema.Property("compression_rules", textList("Quy tắc triển khai và nén nội dung")).Required(),
		)).Required(),
		schema.Property("reader_engagement", schema.Object(
			schema.Property("methods", textList("Cách thu hút người đọc")).Required(),
			schema.Property("emotional_drivers", textList("Động lực cảm xúc")).Required(),
			schema.Property("progression_rewards", textList("Phần thưởng tiến triển theo giai đoạn")).Required(),
			schema.Property("anti_patterns", textList("Phản mẫu làm giảm sức hút")).Required(),
		)).Required(),
		schema.Property("role_guidance", schema.Object(
			schema.Property("architect", textList("Quy tắc Architect sử dụng hồ sơ")).Required(),
			schema.Property("writer", textList("Quy tắc Writer tham khảo nhưng không sao chép")).Required(),
			schema.Property("editor", textList("Quy tắc Editor kiểm tra hướng đi và rủi ro vi phạm bản quyền")).Required(),
		)).Required(),
	),
}
