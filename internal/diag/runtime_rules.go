package diag

import (
	"fmt"
	"strings"

	"github.com/voocel/ainovel-cli/internal/store"
)

// Ngưỡng phát hiện runtime.
const (
	repeatCritical = 8 // lặp gần cuối đạt chừng này lần thì nâng lên critical
	streamIdleWarn = 3 // ngưỡng cảnh báo cộng dồn stream_idle
)

// RuntimeRuleFunc là chữ ký thống nhất của quy tắc chẩn đoán runtime (tương ứng RuleFunc phía sáng tác).
// Tham số vào là RuntimeCapture sau khi làm mờ và gộp, sinh Finding dạng báo cáo — tất cả AutoNone,
// chỉ chẩn đoán, không sinh Action (kỷ luật người quan sát, xem architecture.md §2.3).
type RuntimeRuleFunc func(rc *RuntimeCapture) []Finding

var runtimeRules = []RuntimeRuleFunc{
	repeatedErrors,
	stuckStep,
	streamIdleStorm,
}

// runtimeFindings chạy toàn bộ quy tắc runtime.
func runtimeFindings(rc *RuntimeCapture) []Finding {
	var out []Finding
	for _, rule := range runtimeRules {
		out = append(out, rule(rc)...)
	}
	return out
}

// Diagnose là cổng vào chẩn đoán đầy đủ của /diag: chẩn đoán sáng tác + tín hiệu runtime + phát hiện runtime,
// trả về Report sau khi gộp và RuntimeCapture thô (để xuất tái sử dụng, tránh chụp lại).
// Finding runtime chỉ được gộp vào Findings để hiển thị, không đổi Actions — giữ thuần quan sát.
func Diagnose(s *store.Store) (Report, RuntimeCapture) {
	rep := Analyze(s)
	rc := CaptureRuntime(s)
	rep.Findings = append(rep.Findings, runtimeFindings(&rc)...)
	sortFindings(rep.Findings)
	return rep, rc
}

// repeatedErrors chỉ phán "lỗi / tham số vô hiệu xuất hiện lặp lại gần cuối" thành Finding.
// Không đụng tới lặp công cụ bình thường — subagent/novel_context/read_chapter v.v. vốn
// tần suất cao một cách tự nhiên khi chạy dài, số lần cộng dồn không phải tín hiệu vòng lặp;
// "lặp mà không tiến" thật sự do stuckStep lo phần chốt.
func repeatedErrors(rc *RuntimeCapture) []Finding {
	var out []Finding
	for _, r := range rc.Repeats {
		var rule, title, sugg string
		switch {
		case strings.Contains(r.Sig, " · err: "):
			rule = "RepeatedToolError"
			title = "Công cụ lặp đi lặp lại báo cùng một lỗi"
			sugg = "Gần cuối cùng một công cụ lặp đi lặp lại trả về cùng một lỗi, phần nhiều do tham số mô hình không hợp lệ hoặc không khớp hợp đồng công cụ; kiểm tra kiểm tra công cụ của agentcore / quy ước tham số prompt (xem #34)."
		case strings.Contains(r.Sig, "(args invalid)"):
			rule = "ArgsInvalidLoop"
			title = "Tham số lặp lại không thể phân tích"
			sugg = "Tham số mô hình gửi tới không thể phân tích mà cứ thử lại liên tục; xem agentcore có ép chuyển kiểu lỏng lẻo cho kiểu này hay không (xem #34)."
		default:
			continue // lặp công cụ bình thường không sinh Finding
		}
		sev := SevWarning
		if r.Count >= repeatCritical {
			sev = SevCritical
		}
		out = append(out, Finding{
			Rule:       rule,
			Category:   CatFlow,
			Severity:   sev,
			Confidence: ConfHigh,
			AutoLevel:  AutoNone,
			Target:     "runtime.flow",
			Title:      title,
			Evidence:   fmt.Sprintf("`%s` ×%d", r.Sig, r.Count),
			Suggestion: sugg,
		})
	}
	return out
}

// stuckStep phát hiện checkpoint dừng liên tiếp ở cùng một step.
func stuckStep(rc *RuntimeCapture) []Finding {
	if rc.StuckStep == "" {
		return nil
	}
	sev := SevWarning
	if rc.StuckCount >= repeatCritical {
		sev = SevCritical
	}
	return []Finding{{
		Rule:       "StuckStep",
		Category:   CatFlow,
		Severity:   sev,
		Confidence: ConfHigh,
		AutoLevel:  AutoNone,
		Target:     "runtime.flow",
		Title:      "checkpoint đình trệ ở cùng một step",
		Evidence:   fmt.Sprintf("dừng liên tiếp ở `%s` ×%d", rc.StuckStep, rc.StuckCount),
		Suggestion: "Cùng một step được ghi lặp đi lặp lại mà không tiến; kết hợp chữ ký trùng lặp phía trên để định vị subagent nào bị kẹt.",
	}}
}

// streamIdleStorm phát hiện gián đoạn streaming dày đặc (#32).
func streamIdleStorm(rc *RuntimeCapture) []Finding {
	n := rc.LogKinds["stream_idle"]
	if n < streamIdleWarn {
		return nil
	}
	return []Finding{{
		Rule:       "StreamIdleStorm",
		Category:   CatFlow,
		Severity:   SevWarning,
		Confidence: ConfHigh,
		AutoLevel:  AutoNone,
		Target:     "runtime.provider",
		Title:      "Gián đoạn streaming dày đặc (stream_idle)",
		Evidence:   fmt.Sprintf("stream_idle ×%d", n),
		Suggestion: "Thượng nguồn lâu không nhả token nên bị watchdog giết nhầm; với mô hình suy nghĩ chậm hãy tăng streamIdleTimeout, hoặc rà độ ổn định kết nối provider (xem #32).",
	}}
}
