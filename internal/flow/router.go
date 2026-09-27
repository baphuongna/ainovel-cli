// Package flow hiện thực việc định tuyến: Host dựa trên các sự kiện để quyết
// định bước tiếp theo gọi sub-agent nào làm việc gì.
//
// Nguyên tắc thiết kế:
//   - Route là hàm thuần: đầu vào State, đầu ra *Instruction. Không IO, không
//     gọi Store, có thể kiểm thử đơn lẻ.
//   - State do LoadState (không thuần) dựng từ Store, đọc một lần đủ mọi sự
//     kiện mà định tuyến cần.
//   - Trả về nil là hợp lệ: nghĩa là hiện không có chỉ thị Worker nào suy ra
//     được từ các sự kiện tất định; Engine sẽ xử lý theo trạng thái cuối, phán
//     định bổ trợ lúc khởi động hoặc chờ người dùng can thiệp.
//
// Router chỉ phủ các quyết định "kiểu tra bảng" (bước tiếp theo của mỗi
// chương, hậu xử cuối cung, hàng đợi điều khiển), không phủ các quyết định
// "kiểu hiểu ngữ nghĩa" (chọn kiến trúc sư, xử lý Steer của người dùng, xuất
// tổng kết).
package flow

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// plannerForTier suy ra danh tính kiến trúc sư từ cấp bậc quy hoạch đã lưu
// đĩa: short thuộc kiến trúc sư truyện ngắn, mid/long thuộc kiến trúc sư
// truyện dài (cùng chuẩn lựa chọn với Arbiter khởi động).
func plannerForTier(tier domain.PlanningTier) string {
	if tier == domain.PlanningTierShort {
		return "architect_short"
	}
	return "architect_long"
}

// Instruction chỉ định Worker và tác vụ mà Engine sẽ chạy trực tiếp ở bước
// tiếp theo.
type Instruction struct {
	Agent   string // architect_long / architect_short / writer / editor
	Task    string // mô tả tác vụ giao cho sub-agent
	Reason  string // lý do định tuyến (dùng cho sự kiện, nhật ký và phán định thất bại)
	Chapter int    // số chương mà tác vụ writer liên quan (viết tiếp/viết lại/trau chuốt); 0 nghĩa là không liên quan (tác vụ editor/architect)
}

type AggregateKind string

const (
	AggregateArcReview     AggregateKind = "arc_review"
	AggregateArcSummary    AggregateKind = "arc_summary"
	AggregateVolumeSummary AggregateKind = "volume_summary"
	AggregateGlobalReview  AggregateKind = "global_review"
)

type AggregateRefresh struct {
	Kind         AggregateKind
	Volume       int
	Arc          int
	StartChapter int
	EndChapter   int
}

// State là đầu vào của Route: mọi sự kiện phải khai báo tường minh ở đây, cấm
// Route tự đọc Store bên trong.
type State struct {
	Progress *domain.Progress

	// Số chương lớn nhất trong các chương đã hoàn thành; bằng 0 nghĩa là chưa
	// bắt đầu viết.
	LastCompleted int

	// Thông tin biên giới cung của chương vừa hoàn thành; khi IsArcEnd=false
	// các trường còn lại không có nghĩa. Nên là nil khi LastCompleted=0 hoặc
	// không ở chế độ Layered.
	ArcBoundary *storepkg.ArcBoundary

	// Ba sự kiện hậu xử cuối cung: đã hoàn thành xem xét / tóm tắt cung /
	// tóm tắt tập hay chưa.
	HasArcReview     bool
	HasArcSummary    bool
	HasVolumeSummary bool

	// Các mục còn thiếu của thiết lập nền tảng (tín hiệu bổ sung ở giai đoạn
	// quy hoạch).
	FoundationMissing []string

	// Cấp bậc quy hoạch đã lưu đĩa (được ghi vào RunMeta khi save_foundation
	// lưu scale). Rỗng = lần quy hoạch đầu tiên chưa sinh ra thiết lập nào,
	// chưa xác định được danh tính kiến trúc sư.
	PlanningTier domain.PlanningTier

	// Sách không phân tầng: chương hoàn thành gần nhất đã có bản xem xét toàn
	// cục scope=global hay chưa (chỉ có nghĩa tại điểm kích hoạt ShouldReview;
	// sách phân tầng luôn false).
	HasGlobalReview bool

	// Ảnh hưởng của sửa đổi từ bên ngoài phải được Architect xử lý trước khi
	// viết tiếp. Phản hồi Writer thông thường để dành cho lần thao tác cấu
	// trúc tự nhiên kế tiếp hấp thụ gộp, không phái thêm kiến trúc sư cho từng
	// chương.
	ImmediateFeedbackCount int

	// Artifact cung/tập sớm nhất cần Editor tạo lại sau sửa đổi từ bên ngoài.
	AggregateRefresh *AggregateRefresh

	// Chương đầu hàng viết lại đã có chỉ thị viết lại (chapter_contract) hay
	// chưa. Chỉ đưa writer một câu "viết lại chương N" chẳng khác nào không
	// cho hướng đi: thực tế Editor đánh giá vấn đề nghiêm trọng nhất trong hàng
	// đợi chính là architect_directive_unclear — liệt kê cả đống triệu chứng mà
	// không nói nên viết thành như thế nào.
	RewriteHeadNeedsDirective bool
}

// Route trả về chỉ thị tất định cho bước tiếp theo dựa trên sự kiện; trả nil
// thì Engine xử lý theo ngữ cảnh nơi gọi.
//
// Thứ tự ưu tiên quyết định (độc lập nhau, khớp mục đầu tiên từ trên xuống):
//
//  1. Phase=Complete        → nil (Host xuất tổng kết tất định)
//
//  2. Giai đoạn quy hoạch còn thiếu thiết lập mà kiến trúc sư xác định được
//     → cùng kiến trúc sư đó bổ sung; nếu không → nil (Engine khởi động phán
//     định bổ trợ)
//
//  3. PendingRewrites khác rỗng → writer viết lại/trau chuốt theo hàng đợi
//
//  4. Flow=Reviewing        → nil (dormant: hiện không có bên ghi, kỳ xem xét
//     Flow thật là writing)
//
//  5. Flow=Steering         → nil (đang xử lý can thiệp người dùng)
//
//  6. Sửa đổi ngoài làm artifact tổng hợp mất hiệu lực → editor dựng lại
//
//  7. Sửa đổi ngoài ảnh hưởng quy hoạch phía sau → architect xử lý
//
//  8. Sách phân tầng tới cuối cung → xem xét, tóm tắt, mở cung hoặc nối tập
//
//  9. Sách không phân tầng tới kỳ xem xét toàn cục → editor(global review)
//
//  10. Dàn ý không phân tầng đã cạn → architect (quyết định hoàn thành hay
//     nối tiếp dàn ý)
//
//  11. Còn lại → writer (viết next_chapter)
func Route(s State) *Instruction {
	p := s.Progress
	if p == nil {
		return nil
	}

	// 1. Trạng thái cuối: Host sinh tổng kết tất định dựa trên sự kiện trong store
	if p.Phase == domain.PhaseComplete {
		return nil
	}

	// 2. Bổ sung giai đoạn quy hoạch: quyết định kiểu tra bảng — thiếu gì nằm trong
	//    store, danh tính kiến trúc sư suy từ scale đã lưu đĩa
	//    (short → architect_short, còn lại → architect_long). tier rỗng nghĩa là
	//    lần quy hoạch đầu chưa lưu đĩa thiết lập nào (việc chọn là phán đoán ngữ
	//    nghĩa), do planStartFallback của Engine phán định bổ trợ.
	if p.Phase != domain.PhaseWriting {
		if len(s.FoundationMissing) > 0 && s.PlanningTier != "" {
			task := fmt.Sprintf("Bổ sung thiết lập nền tảng và thông tin tác phẩm còn thiếu mục: %s; book dùng save_book, thiết lập nền tảng còn lại dùng save_foundation để lưu đĩa", strings.Join(s.FoundationMissing, ", "))
			if len(s.FoundationMissing) == 1 && s.FoundationMissing[0] == "foundation_audit" {
				task = "Thiết lập nền tảng đã đủ: gọi lại novel_context đọc toàn bộ artifact đã lưu đĩa và foundation_status.fingerprint, rà soát tính nhất quán ngữ nghĩa giữa các tệp rồi gọi audit_foundation; có vấn đề thì sửa trước và rà soát lại"
			}
			return &Instruction{
				Agent:  plannerForTier(s.PlanningTier),
				Task:   task,
				Reason: "Thiết lập nền tảng còn mục thiếu, theo mục thiếu tiếp tục giao cùng kiến trúc sư",
			}
		}
		return nil
	}

	// 3. Hàng đợi viết lại/trau chuốt được ưu tiên trước (sự kiện đã được tầng
	//    công cụ lưu đĩa, Router chỉ chiếu theo đơn mà giao việc)
	if len(p.PendingRewrites) > 0 {
		ch := p.PendingRewrites[0]
		verb := "Viết lại"
		if p.Flow == domain.FlowPolishing {
			verb = "Trau chuốt"
		}
		task := fmt.Sprintf("%s chương %d", verb, ch)
		if s.RewriteHeadNeedsDirective {
			// Chỉ nói "viết lại chương N" chẳng khác nào không cho hướng đi: thực tế
			// Editor xếp architect_directive_unclear là mục nghiêm trọng nhất trong
			// hàng viết lại. Ở đây không phái thêm kiến trúc sư — chèn thêm một lượt
			// giao việc sẽ khiến hàng đợi không bao giờ rút hết nếu bước quy hoạch
			// thất bại — mà yêu cầu Writer tự ghi hướng thành hợp đồng trước khi
			// đặt bút.
			task += fmt.Sprintf(
				". Chương này chưa có chapter_contract: hãy gọi plan_chapter(chapter=%d) trước để nêu rõ mục tiêu viết lại"+
					" (goal phải trả lời \"viết thành như thế nào\", không phải nhắc lại \"chỗ nào sai\";"+
					" thêm payoff_points / continuity_checks / hook_goal khi cần), rồi dựa vào đó viết lại chính văn", ch)
		}
		return &Instruction{
			Agent:   "writer",
			Task:    task,
			Reason:  fmt.Sprintf("Hàng đợi PendingRewrites còn %d chương", len(p.PendingRewrites)),
			Chapter: ch,
		}
	}

	// 4. Đang xem xét → trao lại cho LLM. Hiện là nhánh dormant: save_review chỉ
	//    đặt Flow thành writing/rewriting/polishing, không đường sản xuất nào
	//    đặt reviewing (kỳ xem xét Flow thật là writing, "xem xét trước khi viết
	//    tiếp" do mức ưu tiên steering của agentcore đảm bảo, không nhờ nhánh
	//    này). Giữ lại cho đối xứng với Steering, và để sau này khi editor đặt
	//    reviewing tường minh trong kỳ xem xét thì định tuyến nhường chỗ cho
	//    LLM.
	if p.Flow == domain.FlowReviewing {
		return nil
	}

	// 5. Đang xử lý can thiệp người dùng: Arbiter đang phán định, Engine không
	//    giành quyền
	if p.Flow == domain.FlowSteering {
		return nil
	}
	if refresh := s.AggregateRefresh; refresh != nil {
		switch refresh.Kind {
		case AggregateArcReview:
			return &Instruction{
				Agent: "editor",
				Task: fmt.Sprintf(
					"Bổ sung bản xem xét cấp cung cho tập %d cung %d (chương %d-%d): gọi novel_context(chapter=%d), save_review dùng scope=arc, chapter=%d",
					refresh.Volume, refresh.Arc, refresh.StartChapter, refresh.EndChapter, refresh.EndChapter, refresh.EndChapter,
				),
				Reason: "Thiếu bản xem xét cấp cung",
			}
		case AggregateArcSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("Tạo tóm tắt cung, ảnh chụp nhân vật và quy tắc viết cho tập %d cung %d (save_arc_summary)", refresh.Volume, refresh.Arc),
				Reason: "Thiếu tóm tắt cấp cung",
			}
		case AggregateVolumeSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("Tạo tóm tắt tập cho tập %d (save_volume_summary)", refresh.Volume),
				Reason: "Thiếu tóm tắt tập",
			}
		case AggregateGlobalReview:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("Bổ sung bản xem xét toàn cục cho %d chương đầu: gọi novel_context(chapter=%d), save_review dùng scope=global, chapter=%d", refresh.EndChapter, refresh.EndChapter, refresh.EndChapter),
				Reason: "Thiếu bản xem xét toàn cục",
			}
		}
	}

	if s.ImmediateFeedbackCount > 0 {
		return &Instruction{
			Agent:  plannerForTier(s.PlanningTier),
			Task:   "Chỉ xử lý writer_feedback sửa đổi ngoài trong novel_context: đối chiếu cốt truyện đã xảy ra với kế hoạch phía sau, cần điều chỉnh thì gọi revise_outline hoặc công cụ cấu trúc tương ứng, không cần điều chỉnh thì gọi resolve_outline_feedback; không được xử lý foundation_status hay quy hoạch khác, sau khi lưu đĩa kết thúc bằng một câu",
			Reason: fmt.Sprintf("Có %d sửa đổi ngoài chưa lan tới quy hoạch phía sau", s.ImmediateFeedbackCount),
		}
	}

	// 8. Hậu xử cuối cung của chế độ phân tầng
	if p.Layered && s.ArcBoundary != nil && s.ArcBoundary.IsArcEnd {
		b := s.ArcBoundary
		switch {
		case !s.HasArcReview:
			return &Instruction{
				Agent: "editor",
				Task: fmt.Sprintf(
					"Thực hiện bản xem xét cấp cung cho tập %d cung %d (chương %d-%d): gọi novel_context(chapter=%d), save_review dùng scope=arc, chapter=%d; issues[].chapters chỉ được nằm trong khoảng này",
					b.Volume, b.Arc, b.StartChapter, b.EndChapter, b.EndChapter, b.EndChapter,
				),
				Reason: "Xem xét cuối cung chưa hoàn thành",
			}
		case !s.HasArcSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("Tạo tóm tắt cung, ảnh chụp nhân vật và quy tắc viết cho tập %d cung %d (save_arc_summary)", b.Volume, b.Arc),
				Reason: "Tóm tắt cung chưa hoàn thành",
			}
		case b.IsVolumeEnd && !s.HasVolumeSummary:
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("Tạo tóm tắt tập cho tập %d (save_volume_summary)", b.Volume),
				Reason: "Tóm tắt tập chưa hoàn thành",
			}
		case b.NeedsExpansion && b.NextArc > 0:
			return &Instruction{
				Agent:  "architect_long",
				Task:   fmt.Sprintf("Mở rộng tập %d, cung %d (save_foundation type=expand_arc)", b.NextVolume, b.NextArc),
				Reason: "Khung cung kế tiếp chờ mở rộng",
			}
		case b.NeedsNewVolume:
			return &Instruction{
				Agent:  "architect_long",
				Task:   "Tạo tập tiếp theo: đánh giá theo danh sách phán định hoàn thành rồi gọi save_foundation — truyện còn tiếp tục → type=append_volume; truyện gần điểm kết → type=append_volume và đỉnh JSON của tập mang \"final\": true (tập chốt hạ, siết toàn bộ mạch truyện cả tập, viết xong tự hoàn thành sách); mọi điều kiện hoàn thành đã thỏa ngay lúc này → type=complete_book. Cả ba lựa chọn đều phải kèm tham số reason nêu rõ căn cứ phán định",
				Reason: "Cuối tập cần quyết định thêm tập mới, tập chốt hạ hay kết thúc toàn sách",
			}
		}
	}

	// 11. Xem xét toàn cục của sách không phân tầng: mỗi ReviewInterval chương
	//     một lần (sự kiện: global review của chương đó chưa lưu đĩa). Trước đây
	//     là tín hiệu review_required trong giá trị trả về của commit_chapter,
	//     giờ suy ra từ sự kiện — giá trị trả về chỉ là hình chiếu của sự kiện,
	//     Route nhìn thẳng cùng sự kiện đó từ store.
	if !p.Layered && s.LastCompleted > 0 {
		if due, reason := domain.ShouldReview(len(p.CompletedChapters)); due && !s.HasGlobalReview {
			return &Instruction{
				Agent:  "editor",
				Task:   fmt.Sprintf("Thực hiện xem xét toàn cục cho %d chương đầu (save_review scope=global, chapter=%d)", s.LastCompleted, s.LastCompleted),
				Reason: reason,
			}
		}
	}

	// 12. Khi dàn ý không phân tầng đã cạn thì không thể tiếp tục giao chương
	// vượt giới hạn. Để Architect dựa trên sự kiện truyện hiện tại quyết định
	// hoàn thành sách, hoặc dùng revise_outline nối tiếp kế hoạch từ chương
	// next.
	next := p.NextChapter()
	if next <= 0 {
		return nil
	}
	if !p.Layered && p.TotalChapters > 0 && next > p.TotalChapters {
		return &Instruction{
			Agent: plannerForTier(s.PlanningTier),
			Task: fmt.Sprintf(
				"Dàn ý không phân tầng đã viết hết (đã hoàn thành %d chương, tổng %d chương): nếu truyện đã khép lại, gọi save_foundation(type=complete_book); nếu vẫn cần tiếp tục, dùng revise_outline nối tiếp kế hoạch từ chương %d",
				len(p.CompletedChapters), p.TotalChapters, next,
			),
			Reason: "Dàn ý không phân tầng đã cạn, cần quyết định hoàn thành hay nối tiếp",
		}
	}

	// 13. Viết tiếp bình thường
	return &Instruction{
		Agent:   "writer",
		Task:    fmt.Sprintf("Viết chương %d", next),
		Reason:  "Viết tiếp chương kế tiếp",
		Chapter: next,
	}
}
