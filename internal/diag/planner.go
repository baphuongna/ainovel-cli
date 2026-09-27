package diag

import "fmt"

// PlanActions sinh hành động khả thi từ Finding độ tin cậy cao.
// Chỉ Finding có Confidence==high && AutoLevel==safe mới sinh ra Action.
func PlanActions(findings []Finding) []Action {
	var actions []Action
	seen := make(map[string]struct{})

	for _, f := range findings {
		if f.Confidence != ConfHigh || f.AutoLevel != AutoSafe {
			continue
		}
		if _, ok := seen[f.Rule]; ok {
			continue
		}
		seen[f.Rule] = struct{}{}

		actions = append(actions, planRule(f)...)
	}
	return actions
}

func planRule(f Finding) []Action {
	key := findingFingerprint(f)

	switch f.Rule {
	case "PhaseFlowMismatch":
		return []Action{
			{SourceRule: f.Rule, Kind: ActionEmitNotice, Severity: f.Severity, Summary: f.Title, Message: f.Title, Fingerprint: key},
			{SourceRule: f.Rule, Kind: ActionEnqueueFollowUp, Severity: f.Severity, Summary: "Sửa lỗi bộ máy trạng thái", Message: "Bất thường bộ máy trạng thái: " + f.Evidence + ". Hãy kiểm tra và sửa trạng thái phase/flow của progress trước, rồi mới chạy tiếp.", Fingerprint: key},
		}
	case "OutlineExhausted":
		return []Action{
			{SourceRule: f.Rule, Kind: ActionEnqueueFollowUp, Severity: f.Severity, Summary: "Xử lý dàn ý cạn kiệt", Message: "Số chương đã hoàn thành đạt giới hạn đã quy hoạch. Hãy ưu tiên gọi Architect triển khai cung tiếp theo hoặc thêm tập mới, rồi mới viết tiếp.", Fingerprint: key},
		}
	case "OrphanedSteer":
		return []Action{
			{SourceRule: f.Rule, Kind: ActionEnqueueFollowUp, Severity: f.Severity, Summary: "Tiêu thụ can thiệp chưa xử lý của người dùng", Message: "Tồn tại lệnh can thiệp của người dùng chưa được tiêu thụ, hãy ưu tiên xử lý pending steer rồi mới tiếp tục nhiệm vụ hiện tại.", Fingerprint: key},
		}
	default:
		return nil
	}
}

func findingFingerprint(f Finding) string {
	return fmt.Sprintf("%s|%s|%s|%s", f.Rule, f.Target, f.Title, f.Evidence)
}
