// Package userrules là tầng dịch vụ chuẩn hóa quy tắc người dùng: đưa quy tắc ngôn ngữ tự nhiên
// từ nhiều nguồn qua lệnh gọi cấu trúc LLM để chuẩn hóa thành các trường cấu trúc ứng viên,
// rồi do rules.BuildSnapshot hợp nhất tất định thành snapshot của cuốn sách.
//
// Trách nhiệm phân tầng:
//   - package rules: thuần dữ liệu + hợp nhất tất định (Snapshot / Candidate / BuildSnapshot / SystemDefaults)
//   - package này: chuẩn hóa LLM + điều phối + ghi xuống đĩa (phụ thuộc agentcore + store + rules)
//
// Chuẩn hóa là đường tăng cường, không phải điều kiện tiên quyết của sáng tạo chính: bất kỳ
// nguồn nào thất bại cũng hạ cấp thành raw preferences, sáng tạo chính buộc phải tiếp tục.
package userrules

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/voocel/agentcore"
	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/llmcontract"
	"github.com/voocel/ainovel-cli/internal/rules"
)

// normalizeMaxTokens giới hạn đầu ra của một lần chuẩn hóa (token suy nghĩ và đầu ra JSON dùng
// chung ngân sách này). JSON chuẩn hóa vốn rất nhỏ (thường <1k), phần lớn để dành cho ngân sách
// suy nghĩ của "model suy luận không tắt được thinking" — để hẹp thì thinking sẽ lấn chiếm JSON
// gây cắt cụt, phân tích thất bại. max_tokens là giới hạn trên chứ không phải lượng tính phí,
// chỉnh to không tăng chi phí.
const normalizeMaxTokens = 8192

// normalizeContract DTO biên sát kề: mọi trường required, fatigue_words dùng mảng object
// (chế độ strict cấm map có key động), hai chế độ dùng chung cùng một quy ước DTO.
var normalizeContract = llmcontract.Contract{
	Name:        "userrules_normalize",
	Description: "Chuẩn hóa quy tắc viết bằng ngôn ngữ tự nhiên của người dùng thành các trường cấu trúc",
	Schema: schema.Object(
		schema.Property("structured", schema.Object(
			schema.Property("genre", schema.String("Thể loại; nếu không có thì chuỗi rỗng")).Required(),
			schema.Property("forbidden_chars", schema.Array("Ký tự bị cấm xuất hiện", schema.String("ký tự"))).Required(),
			schema.Property("forbidden_phrases", schema.Array("Cụm từ bị cấm xuất hiện (khớp literal chính xác)", schema.String("cụm từ"))).Required(),
			schema.Property("fatigue_words", schema.Array("Từ mệt mỏi và giới hạn xuất hiện mỗi chương", schema.Object(
				schema.Property("word", schema.String("từ mệt mỏi")).Required(),
				schema.Property("max_per_chapter", schema.Int("Giới hạn trên số lần xuất hiện mỗi chương (số nguyên dương)")).Required(),
			))).Required(),
		)).Required(),
		schema.Property("preferences", schema.String("Sở thích phong cách/nhân vật/thẩm mỹ bằng ngôn ngữ tự nhiên; nếu không có thì chuỗi rỗng")).Required(),
		schema.Property("uncertain", schema.Array("Các mục cố ý không nâng lên structured kèm lý do", schema.String("mục"))).Required(),
	),
}

// Normalizer chuẩn hóa quy tắc ngôn ngữ tự nhiên từ một nguồn duy nhất thành rules.Candidate.
type Normalizer struct {
	model agentcore.ChatModel
}

// NewNormalizer dựng bộ chuẩn hóa từ một ChatModel. Chuẩn hóa là công cụ khởi động một lần,
// nên truyền model năng lực mạnh (như model mặc định của ModelSet), không cần theo model yếu
// của tầng viết.
//
// Chuẩn hóa không đè thinking: tắt tường minh cũng là tham số suy luận chỉ một số model hỗ trợ,
// model chat thường sẽ từ chối nó. Theo mặc định provider/model, do normalizeMaxTokens
// dành sẵn ngân sách đầu ra cho model không tắt được thinking.
func NewNormalizer(model agentcore.ChatModel) *Normalizer {
	return &Normalizer{model: model}
}

// Normalize chuẩn hóa một nguồn. Thất bại trả về error (chứa nguyên nhân thật), nơi gọi quyết định
// hạ cấp (Service.normalizeOrDegrade ghi ứng viên degraded) — lỗi kỹ thuật không còn ngụy trang
// thành kết quả bình thường, lỗi chấm dứt (xác thực/quyền...) không thử lại.
func (n *Normalizer) Normalize(ctx context.Context, source, text string) (rules.Candidate, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return rules.Candidate{Source: source}, nil
	}
	if n == nil || n.model == nil {
		return rules.Candidate{}, fmt.Errorf("Model chuẩn hóa chưa được cấu hình")
	}

	out, err := llmcontract.Execute(ctx, n.model, llmcontract.Request[normalizerOutput]{
		Contract:     normalizeContract,
		SystemPrompt: normalizerSystemPrompt,
		Payload:      text,
		Options:      []agentcore.CallOption{agentcore.WithMaxTokens(normalizeMaxTokens)},
		Validate: func(out *normalizerOutput) error {
			_, err := out.toCandidate(source)
			return err
		},
		Agent: "rules",
		Hooks: llmcontract.Hooks{
			Resolved: func(res llmcontract.Resolution) {
				slog.Debug("Lựa chọn giao thức chuẩn hóa quy tắc", "module", "rules", "source", source,
					"contract", normalizeContract.Name, "structured_mode", res.Mode,
					"capability_source", res.Source, "provider", res.Provider, "model", res.Model,
					"schema_fingerprint", normalizeContract.Fingerprint())
			},
			Correction: func(ev llmcontract.Correction) {
				slog.Warn("Tự chữa đầu ra chuẩn hóa quy tắc", "module", "rules", "source", source,
					"attempt", ev.Attempt, "layer", ev.Layer, "structured_mode", ev.Mode, "err", ev.Err)
			},
		},
	})
	if err != nil {
		return rules.Candidate{}, fmt.Errorf("Chuẩn hóa thất bại: %w", err)
	}
	return out.toCandidate(source)
}

// degraded dựng một ứng viên hạ cấp: khi chuẩn hóa thất bại, coi văn bản gốc như sở thích
// phong cách, không trích xuất quy tắc cơ học nào. uncertain ghi nguồn (để hiển thị lại "nguồn
// nào không parse được"), nhưng không chứa chi tiết lỗi kỹ thuật — lỗi kỹ thuật chỉ vào log.
func degraded(source, text string) rules.Candidate {
	return rules.Candidate{
		Source:      source,
		Preferences: text,
		Uncertain:   []string{source + ": chuẩn hóa thất bại, đã xử lý văn bản gốc như sở thích phong cách (không trích quy tắc cơ học)"},
		Degraded:    true,
	}
}

// normalizerOutput là DTO biên của quy ước bộ chuẩn hóa (hai chế độ dùng chung): uncertain
// cố định là mảng chuỗi, fatigue_words cố định là mảng object — hình thái do contract chốt,
// không còn đoán nhiều hình thái.
type normalizerOutput struct {
	Structured  normalizerStructured `json:"structured"`
	Preferences string               `json:"preferences"`
	Uncertain   []string             `json:"uncertain"`
}

type normalizerStructured struct {
	Genre            string             `json:"genre"`
	ForbiddenChars   []string           `json:"forbidden_chars"`
	ForbiddenPhrases []string           `json:"forbidden_phrases"`
	FatigueWords     []fatigueWordEntry `json:"fatigue_words"`
}

type fatigueWordEntry struct {
	Word          string `json:"word"`
	MaxPerChapter int    `json:"max_per_chapter"`
}

// toCandidate kiểm tra DTO biên và chuyển thành ứng viên miền: mục fatigue phải có từ khác
// rỗng, giới hạn là số nguyên dương (lỗi kiểm tra có thể phản hồi cho model sửa), phía miền
// vẫn là map[string]int.
func (o normalizerOutput) toCandidate(source string) (rules.Candidate, error) {
	var fatigue map[string]int
	for _, e := range o.Structured.FatigueWords {
		word := strings.TrimSpace(e.Word)
		if word == "" {
			return rules.Candidate{}, fmt.Errorf("fatigue_words chứa mục từ rỗng")
		}
		if e.MaxPerChapter < 1 {
			return rules.Candidate{}, fmt.Errorf("fatigue_words[%q].max_per_chapter phải là số nguyên dương, got %d", word, e.MaxPerChapter)
		}
		if fatigue == nil {
			fatigue = make(map[string]int, len(o.Structured.FatigueWords))
		}
		fatigue[word] = e.MaxPerChapter
	}
	return rules.Candidate{
		Source: source,
		Structured: rules.Structured{
			Genre:            strings.TrimSpace(o.Structured.Genre),
			ForbiddenChars:   nonEmpty(o.Structured.ForbiddenChars),
			ForbiddenPhrases: nonEmpty(o.Structured.ForbiddenPhrases),
			FatigueWords:     fatigue,
		},
		Preferences: strings.TrimSpace(o.Preferences),
		Uncertain:   nonEmpty(o.Uncertain),
	}, nil
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// normalizerSystemPrompt chỉ mô tả ngữ nghĩa chuẩn hóa, cấu trúc đầu ra do normalizeContract
// duy trì tại một điểm. Đã dùng 10 ví dụ thật (kể cả bẫy bịa ngưỡng) kiểm chứng nguyên tắc
// nâng bảo thủ đứng vững (10/10).
const normalizerSystemPrompt = `Bạn là "Bộ chuẩn hóa quy tắc" của hệ thống viết tiểu thuyết AI. Bạn đọc quy tắc viết dài hạn của người dùng từ một nguồn (ngôn ngữ tự nhiên), nâng các quy tắc rõ ràng và kiểm tra cơ học được lên structured, phần còn lại đưa vào preferences hoặc uncertain.

【Nâng bảo thủ — quan trọng nhất】
- Chỉ ghi vào structured khi người dùng rõ ràng, không mơ hồ.
- forbidden_chars/forbidden_phrases là cấp error: chỉ nâng khi có lệnh cấm rõ ràng kiểu "không xuất hiện X / cấm X / đừng viết X".
- fatigue_words: chỉ nâng khi đồng thời có "từ rõ ràng" và "ngưỡng số lần rõ ràng"; "dùng ít X / đừng lặp X" không kèm số thì đưa vào preferences, tuyệt đối không tự bịa ngưỡng.
- Mong muốn về số chữ/độ dài ("mỗi chương 3000 chữ", "ngắn hơn") luôn đặt vào preferences: độ dài chương là vấn đề nhịp tự sự, do cảm thụ lúc sáng tác quyết định, không kiểm tra cơ học.
- Những gì không kiểm tra cơ học được, không có ngưỡng rõ ràng, phụ thuộc ngữ cảnh, đều đặt vào preferences.
- Nguyên tắc: thà thiếu vào structured còn hơn nâng nhầm (điều đó sẽ báo nhầm mỗi chương).

preferences giữ lại bằng một đoạn ngôn ngữ tự nhiên dễ đọc về sở thích phong cách, nhân vật và thẩm mỹ.
uncertain nêu các mục bạn cố ý không nâng lên structured cùng lý do.`
