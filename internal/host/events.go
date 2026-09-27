package host

import (
	"time"
)

// Event là sự kiện có cấu trúc do TUI tiêu thụ.
//
// Với ba loại sự kiện gọi TOOL / DISPATCH / DECISION, lần bắt đầu và kết thúc của cùng một lần gọi dùng chung một ID:
// lúc bắt đầu phát sự kiện có FinishedAt bằng giá trị zero (TUI render theo kiểu "đang thực hiện");
// lúc kết thúc phát một sự kiện cùng ID, điền FinishedAt + Duration (+ Failed),
// TUI định vị dòng gốc theo ID và cập nhật tại chỗ, tránh thừa "một dòng bắt đầu, xong lại một dòng".
//
// Sự kiện không thuộc loại gọi như SYSTEM / ERROR / CONTEXT có ID rỗng, mỗi dòng được append độc lập.
type Event struct {
	ID         string    // bắt đầu/kết thúc của cùng một lần gọi dùng chung; sự kiện không phải kiểu gọi thì rỗng
	Time       time.Time // thời điểm phát lần đầu (thời khắc bắt đầu)
	FinishedAt time.Time // giá trị zero = đang thực hiện; khác zero = đã hoàn thành
	Failed     bool      // đã hoàn thành nhưng thất bại (chỉ có nghĩa ở trạng thái hoàn thành)
	Category   string    // DISPATCH / TOOL / DECISION / SYSTEM / REVIEW / CHECK / ERROR / CONTEXT
	Agent      string    // agent phát sinh sự kiện
	Summary    string
	Detail     string        // văn bản đầy đủ, ghi vào log không cắt ngắn để điều tra; rỗng thì lùi về Summary. UI chỉ đọc Summary
	Kind       string        // phân loại lỗi (như stream_idle), xuất theo log để lọc/cảnh báo; rỗng thì không xuất
	Level      string        // info / warn / error / success
	Depth      int           // 0 = tầng Engine, 1 = tầng Worker
	Duration   time.Duration // thời gian thực thi khi hoàn thành
	RetryAt    time.Time     // sự kiện kiểu thử lại: thời điểm hết hạn của lần thử kế tiếp; UI dựa đó đếm ngược từng giây, tới điểm thì dọn (yêu cầu đang in-flight)
}

// Running trả về sự kiện có đang thực hiện hay không.
// Chỉ sự kiện kiểu gọi (TOOL / DISPATCH / DECISION có ID) mới có thể đang thực hiện; các loại khác luôn trả false.
func (e Event) Running() bool {
	return e.hasLifecycle() && e.FinishedAt.IsZero()
}

func (e Event) hasLifecycle() bool {
	if e.ID == "" {
		return false
	}
	switch e.Category {
	case "TOOL", "DISPATCH", "DECISION":
		return true
	default:
		return false
	}
}

// UISnapshot là snapshot tổng hợp trạng thái cần cho render TUI.
type UISnapshot struct {
	Provider             string
	BookTitle            string
	ModelName            string
	ModelContextWindow   int // cửa sổ ngữ cảnh của model mặc định hiện tại (resolve theo thời gian thực khi /model chuyển)
	ThinkingLevel        string
	Style                string
	RuntimeState         string // idle / running / pausing / paused / completed
	StatusLabel          string
	Phase                string
	Flow                 string
	CurrentChapter       int
	TotalChapters        int
	CompletedCount       int
	TotalWordCount       int
	InProgressChapter    int
	PendingRewrites      []int
	RewriteReason        string
	PendingSteer         string
	AdvanceMode          string
	AdvancePermitChapter int
	HasAdvanceHold       bool
	AdvanceHoldReason    string
	RecoveryLabel        string
	IsRunning            bool
	Agents               []AgentSnapshot

	// Lượng dùng tích lũy (toàn phiên, xuyên qua mọi agent và lần đổi model)
	TotalInputTokens      int
	TotalOutputTokens     int
	TotalCacheReadTokens  int
	TotalCacheWriteTokens int
	TotalCostUSD          float64
	TotalSavedUSD         float64 // USD tiết kiệm được nhờ hit CacheRead (so với tính toàn bộ input theo giá không cache)
	BudgetLimitUSD        float64 // hạn mức ngân sách (config budget.book_usd); 0 = chưa bật

	// Chẩn đoán cache
	OverallCacheCapable    bool // ít nhất một role từng chạy model hỗ trợ prompt cache (phân biệt "chưa bật" với "0% hit")
	OverallRecentCacheRead int  // tổng cacheRead của N lần gần nhất trong cửa sổ trượt
	OverallRecentInput     int  // tổng input của N lần gần nhất trong cửa sổ trượt
	OverallRecentSamples   int  // số mẫu trong cửa sổ trượt (≤ recentSampleCap)
	TotalCacheBreaks       int  // số lần đứt gãy chuỗi cache phát hiện trên live (tiền tố không ngắn lại mà hit giảm mạnh), xem usage.go noteCacheBreak

	// MissingAssistantUsage > 0 thường nghĩa là upstream streaming không gửi final usage
	// chunk theo protocol OpenAI stream_options.include_usage (thường thấy ở proxy tự dựng),
	// khiến UsageTracker không nhận được bất kỳ dữ liệu tích lũy nào. UI dựa đó báo rõ người dùng điều tra backend,
	// đừng để người dùng tưởng module cache tự nó hỏng.
	MissingAssistantUsage int

	// Chiều cache per-role, giảm dần theo CacheRead, đã lọc role chưa tiêu thụ token
	CachePerAgent []AgentCacheStat
	CachePerModel []AgentCacheStat

	// Thiết lập nền
	Synopsis         string
	Premise          string
	Outline          []OutlineSnapshot
	Characters       []string
	SupportingCount  int      // tổng số nhân vật phụ trong danh sách diễn viên phụ
	RecentSupporting []string // nhân vật phụ hoạt động gần đây (tối đa 5, giảm dần theo LastSeenChapter)
	Layered          bool
	CurrentVolumeArc string
	NextVolumeTitle  string
	CompassDirection string
	CompassScale     string

	// Chi tiết
	LastCommitSummary  string
	LastReviewSummary  string
	LastCheckpointName string
	RecentSummaries    []string
}

// OutlineSnapshot là tóm tắt hiển thị của một mục dàn ý.
type OutlineSnapshot struct {
	Chapter   int
	Title     string
	CoreEvent string
}

// AgentSnapshot là phép chiếu hiển thị trạng thái Agent.
type AgentSnapshot struct {
	Name      string
	State     string
	TaskID    string
	TaskKind  string
	Summary   string
	Tool      string
	Turn      int
	Context   AgentContextSnapshot
	UpdatedAt time.Time
}

// AgentCacheStat là tích lũy hit cache của một agent (chiếu lên cột trái).
// HitRate = CacheRead / Input; Input ở tầng litellm đã thống nhất ngữ nghĩa "gồm CacheRead".
//
// CacheCapable dùng phân biệt hai loại 0% hit:
//   - true  → model hỗ trợ prompt cache, 0% là do thiết kế prompt kém hoặc tiền tố không ổn định, cần tối ưu
//   - false → model/provider không hỗ trợ prompt cache, 0% là đúng như kỳ vọng, không cần điều tra
//
// Recent* là dữ liệu hit của cửa sổ trượt (N lần gọi gần nhất), so với tích lũy để nhận ra "kéo lê giai đoạn đầu" với "tỷ lệ thấp ổn định".
type AgentCacheStat struct {
	Role            string
	Model           string
	Input           int
	Output          int
	CacheRead       int
	CacheWrite      int
	Cost            float64
	Saved           float64
	CacheCapable    bool
	RecentCacheRead int
	RecentInput     int
	RecentSamples   int
}

// AgentContextSnapshot là tình trạng sử dụng ngữ cảnh của Agent.
type AgentContextSnapshot struct {
	Tokens          int
	ContextWindow   int
	Percent         float64
	Scope           string
	Strategy        string
	ActiveMessages  int
	SummaryMessages int
	CompactedCount  int
	KeptCount       int
}

// CoCreateMessage là tin nhắn của cuộc đối thoại đồng sáng tạo.
type CoCreateMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CoCreateReply là phản hồi LLM của cuộc đối thoại đồng sáng tạo. Raw giữ nguyên văn đủ bốn đoạn của model,
// dùng để ghi lại history cho vòng sau model thấy được [DRAFT] của mình vòng trước, từ đó thật sự cập nhật
// cộng dồn trên bản nháp sẵn có (chỉ Message không chứa [DRAFT] sẽ khiến model mỗi vòng tổng kết lại từ đối thoại).
// Suggestions là những gợi ý "tiếp theo bạn có thể muốn nói" do AI chủ động đưa, khi bí ý bấm phím số điền ngay vào ô nhập.
type CoCreateReply struct {
	Message     string
	Prompt      string
	Ready       bool
	Suggestions []string
	Raw         string
}
