package diag

// Severity thể hiện mức độ nghiêm trọng của phát hiện.
type Severity string

const (
	SevCritical Severity = "critical" // chặn tiến độ hoặc hỏng dữ liệu
	SevWarning  Severity = "warning"  // có thể giảm chất lượng hoặc lãng phí token
	SevInfo     Severity = "info"     // mục có thể tối ưu
)

// Category nhóm các phát hiện theo chiều.
type Category string

const (
	CatFlow     Category = "flow"     // nghẽn luồng, trạng thái bất thường, vấn đề phục hồi
	CatQuality  Category = "quality"  // điểm xem xét, tuân thủ hợp đồng, nhất quán
	CatPlanning Category = "planning" // khuyết hụt dàn ý, trôi phục bút, la bàn lỗi thời
	CatContext  Category = "context"  // bất thường nhân vật/dòng thời gian/mối quan hệ
)

// Confidence thể hiện độ tin cậy của phán đoán quy tắc.
type Confidence string

const (
	ConfHigh   Confidence = "high"   // tính chắc chắn cao, đáng tin cậy
	ConfMedium Confidence = "medium" // phán đoán heuristic, có thể sai
	ConfLow    Confidence = "low"    // tín hiệu thô, chỉ để tham khảo
)

// AutoLevel thể hiện Finding có thể chuyển thành hành động tự động hay không.
type AutoLevel string

const (
	AutoNone    AutoLevel = "none"    // chỉ báo cáo, không tự động
	AutoSuggest AutoLevel = "suggest" // đề xuất hành động nhưng cần xác nhận thủ công
	AutoSafe    AutoLevel = "safe"    // có thể tự động thực thi an toàn
)

// Finding là một kết quả chẩn đoán có thể hành động theo.
type Finding struct {
	Rule       string     // tên quy tắc, ví dụ "StaleForeshadow"
	Category   Category   // phân loại
	Severity   Severity   // mức nghiêm trọng
	Confidence Confidence // độ tin cậy phán đoán
	AutoLevel  AutoLevel  // cấp tự động hóa
	Target     string     // phạm vi tác động đề xuất, ví dụ "runtime.flow"
	Title      string     // tóm tắt một dòng
	Evidence   string     // bằng chứng dữ liệu cụ thể
	Suggestion string     // đề xuất cải tiến (chỉ tới prompt/flow/config)
}

// RuleFunc là chữ ký thống nhất của quy tắc chẩn đoán.
type RuleFunc func(snap *Snapshot) []Finding

// ActionKind thể hiện loại hành động chẩn đoán.
type ActionKind string

const (
	ActionEmitNotice      ActionKind = "emit_notice"       // phát thông báo hệ thống
	ActionEnqueueFollowUp ActionKind = "enqueue_follow_up" // sinh đề nghị xử lý tiếp theo
)

// Action là hành động khả thi do Planner sinh ra từ Finding độ tin cậy cao.
type Action struct {
	SourceRule  string     // tên quy tắc nguồn
	Kind        ActionKind // loại hành động
	Severity    Severity   // kế thừa từ Finding
	Summary     string     // mô tả ngắn
	Message     string     // thông điệp truyền cho luồng điều khiển
	Fingerprint string     // dấu vân tay ổn định của Finding nguồn, dùng để khử trùng lặp lúc chạy
}

// Stats là các chỉ số tổng quan hiển thị song song với các phát hiện.
type Stats struct {
	CompletedChapters int
	TotalChapters     int
	TotalWords        int
	AvgWordsPerCh     int
	Phase             string
	Flow              string
	PlanningTier      string
	ReviewCount       int
	RewriteCount      int
	AvgReviewScore    float64
	ForeshadowOpen    int
	ForeshadowStale   int
}

// Report là kết quả đầy đủ của một lần chạy chẩn đoán.
type Report struct {
	Stats    Stats
	Findings []Finding
	Actions  []Action
}
