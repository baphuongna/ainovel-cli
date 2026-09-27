package host

import (
	"fmt"
	"math"
	"sync/atomic"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

// Máy trạng thái ngân sách: tăng đơn điệu, mỗi lần chuyển trạng thái kích hoạt đúng một tác dụng phụ, không lùi.
// Tăng hạn mức ngân sách = người dùng tái ủy quyền = đổi config rồi khởi động lại/Host mới, không hồi trạng thái trong instance này.
const (
	budgetNormal      int32 = iota // chưa tới mực cảnh báo
	budgetWarned                   // đã phát cảnh báo, chưa vượt ngưỡng
	budgetStopPending              // đã vượt ngưỡng, đợi biên subagent để dừng
	budgetStopped                  // đã thực thi dừng
)

// BudgetSentinel theo dõi chi phí tích lũy, thực thi chính sách ngân sách của người dùng (khối budget trong config).
//
// Định vị chính danh (architecture.md §8.3/§10): không đánh giá hành vi model — dừng khi vượt ngưỡng tương đương
// người dùng thủ công Abort tại khoảnh khắc đó, Host chỉ thay mặt thực thi một lệnh đã ký trước. Nó ảnh hưởng
// luồng điều khiển, nên không phải quan sát viên, định vị là thành phần chính sách của Host ngang flow.Dispatcher; tầng Route/tool không nhận biết.
//
// Thời điểm dừng: mặc định tại biên subagent (Host gọi HandleBoundary đồng bộ), không phí chương in-flight;
// hardStop=true thì dừng ngay khi vượt. Xử lý biên chạy trước khi flow.Dispatcher phân phát bước kế, tầng Route/tool không nhận biết ngân sách.
type BudgetSentinel struct {
	limit     float64
	warnRatio float64
	hardStop  bool

	costNow func() float64              // chi phí tích lũy hiện tại (bọc usage.Totals; có thể tiêm stub để test)
	abort   func(reason string)         // bao dừng máy của Host (kèm sự kiện lý do)
	report  func(level, summary string) // lối xuất cảnh báo (emitEvent + notify, do Host tiêm)

	state atomic.Int32

	// Phát hiện vùng mù tính phí: model không có giá trong registry và provider không tự báo cost thì mỗi lần
	// ghi sổ tăng $0, ngân sách âm thầm mất tác dụng. Xét theo "nhiều lần liên tiếp tăng bằng 0" thay vì total==0 —
	// cách sau bắt không được kịch bản giữa chặng /model chuyển sang model không giá (total đứng ở giá trị lịch sử
	// khác 0 nhưng không tăng nữa). Model miễn phí cũng dính, gợi ý "ngân sách sẽ không kích hoạt" với nó cũng đúng.
	lastTotal   atomic.Uint64 // math.Float64bits(chi phí tích lũy của lần callback trước)
	zeroStreak  atomic.Int32
	blindWarned atomic.Bool
}

// blindZeroStreak bao nhiêu lần ghi sổ tăng bằng 0 liên tiếp thì cảnh báo. Model tính giá bình thường mỗi lần
// tăng nhất định > 0 (cost là float tích lũy không làm tròn), lấy 5 chỉ để tránh gai cực đoan, không phải ngưỡng chính sách hiệu chỉnh được.
const blindZeroStreak = 5

// NewBudgetSentinel tạo sentinel ngân sách; chính sách chưa bật thì trả nil (mọi method nil-safe).
func NewBudgetSentinel(cfg bootstrap.BudgetConfig, costNow func() float64, abort func(reason string), report func(level, summary string)) *BudgetSentinel {
	if !cfg.Enabled() {
		return nil
	}
	return &BudgetSentinel{
		limit:     cfg.BookUSD,
		warnRatio: cfg.WarnRatio,
		hardStop:  cfg.HardStop,
		costNow:   costNow,
		abort:     abort,
		report:    report,
	}
}

// OnCost do UsageTracker gọi sau mỗi lần ghi sổ mang theo chi phí tích lũy mới nhất (ngoài khóa).
// Một lần callback có thể vượt hai cấp liền (normal→warned→stopPending), hai tác dụng phụ mỗi cái kích hoạt một lần.
func (s *BudgetSentinel) OnCost(total float64) {
	if s == nil {
		return
	}
	if prev := s.lastTotal.Swap(math.Float64bits(total)); total == math.Float64frombits(prev) {
		if s.zeroStreak.Add(1) >= blindZeroStreak && s.blindWarned.CompareAndSwap(false, true) {
			s.report("warn", fmt.Sprintf("Vùng mù ngân sách: liên tục ghi sổ nhưng chi phí tích lũy đứng ở $%.2f không tăng nữa (model hiện tại không có giá trong registry và provider không tự báo cost, hoặc là model miễn phí) — hạn mức ngân sách sẽ không kích hoạt", total))
		}
	} else {
		s.zeroStreak.Store(0)
	}
	if total >= s.limit*s.warnRatio && s.state.CompareAndSwap(budgetNormal, budgetWarned) {
		s.report("warn", fmt.Sprintf("Cảnh báo ngân sách: đã chi $%.2f, đạt %.0f%% ngân sách $%.2f", total, s.warnRatio*100, s.limit))
	}
	if total >= s.limit && s.state.CompareAndSwap(budgetWarned, budgetStopPending) {
		if s.hardStop {
			s.report("error", fmt.Sprintf("Hết ngân sách: đã chi $%.2f, vượt ngân sách $%.2f, dừng ngay lập tức", total, s.limit))
			s.stop(total)
			return
		}
		s.report("error", fmt.Sprintf("Hết ngân sách: đã chi $%.2f, vượt ngân sách $%.2f, sẽ dừng sau khi tác vụ subagent hiện tại kết thúc", total, s.limit))
	}
}

// HandleEvent thực thi lần dừng treo tại biên subagent. Subscribe phải trước Dispatcher.
// Không bỏ qua IsError — trả về lỗi cũng là biên, việc dừng không nên bị hoãn vì subagent thất bại.
func (s *BudgetSentinel) HandleEvent(ev agentcore.Event) {
	if s == nil {
		return
	}
	if ev.Type != agentcore.EventToolExecEnd || ev.Tool != "subagent" {
		return
	}
	s.HandleBoundary()
}

func (s *BudgetSentinel) HandleBoundary() bool {
	if s == nil || s.state.Load() != budgetStopPending {
		return false
	}
	s.stop(s.costNow())
	return true
}

func (s *BudgetSentinel) stop(total float64) {
	if s.state.CompareAndSwap(budgetStopPending, budgetStopped) {
		s.abort(fmt.Sprintf("Dừng theo ngân sách: đã chi $%.2f, vượt ngân sách $%.2f; tăng budget.book_usd lên có thể khôi phục viết tiếp", total, s.limit))
	}
}

// Refuse kiểm tra trước khởi động: ngân sách đã vượt thì trả lỗi từ chối (đường khôi phục Start/Resume/Continue gọi).
// Người dùng tăng ngân sách = tái ủy quyền, dưới config mới Refuse tự nhiên cho qua.
func (s *BudgetSentinel) Refuse() error {
	if s == nil {
		return nil
	}
	if cost := s.costNow(); cost >= s.limit {
		return fmt.Errorf("Sách này đã chi $%.2f, đạt hạn mức ngân sách $%.2f; vui lòng tăng cấu hình budget.book_usd rồi thử lại", cost, s.limit)
	}
	return nil
}

// Limit trả hạn mức ngân sách (UI hiển thị); chưa bật trả 0.
func (s *BudgetSentinel) Limit() float64 {
	if s == nil {
		return 0
	}
	return s.limit
}
