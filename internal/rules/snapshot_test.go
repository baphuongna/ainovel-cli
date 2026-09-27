package rules

import (
	"strings"
	"testing"
)

func TestBuildSnapshot_FieldOverridePrecedence(t *testing.T) {
	// 低→高：defaults 设 修仙，project 覆盖为 都市；高优先级胜出。
	snap := BuildSnapshot([]Candidate{
		{Source: "system_defaults", Structured: Structured{Genre: "修仙"}},
		{Source: "project:a.md", Structured: Structured{Genre: "都市"}},
	})
	if snap.Structured.Genre != "都市" {
		t.Fatalf("期望 project 覆盖 defaults，得到 %q", snap.Structured.Genre)
	}
	if snap.Status != StatusReady {
		t.Fatalf("期望 ready，得到 %s", snap.Status)
	}
	if snap.Version != SnapshotVersion {
		t.Fatalf("version 应为 %d，得到 %d", SnapshotVersion, snap.Version)
	}
}

func TestBuildSnapshot_EmptyAndZeroAreAbsent(t *testing.T) {
	// 归一化器吐占位：genre:""、空串元素——都必须当缺失，不覆盖低优先级真值。
	snap := BuildSnapshot([]Candidate{
		{Source: "system_defaults", Structured: Structured{
			Genre: "修仙",
		}},
		{Source: "startup_prompt", Structured: Structured{
			Genre:            "",                 // 占位空串 → 不覆盖
			ForbiddenPhrases: []string{"", "  "}, // 全空 → 丢弃
		}},
	})
	if snap.Structured.Genre != "修仙" {
		t.Fatalf("空 genre 不应覆盖，期望 修仙，得到 %q", snap.Structured.Genre)
	}
	if len(snap.Structured.ForbiddenPhrases) != 0 {
		t.Fatalf("全空 forbidden_phrases 应被丢弃，得到 %v", snap.Structured.ForbiddenPhrases)
	}
}

func TestBuildSnapshot_PreferencesPrecedenceOrder(t *testing.T) {
	snap := BuildSnapshot([]Candidate{
		{Source: "global:g.md", Preferences: "全局偏好"},
		{Source: "project:p.md", Preferences: "项目偏好"},
	})
	gi := strings.Index(snap.Preferences, "全局偏好")
	pi := strings.Index(snap.Preferences, "项目偏好")
	if gi < 0 || pi < 0 || gi > pi {
		t.Fatalf("preferences 应按优先级低→高拼接（项目在后），得到:\n%s", snap.Preferences)
	}
	if !strings.Contains(snap.Preferences, "## [global:g.md]") {
		t.Fatalf("preferences 应带来源标题，得到:\n%s", snap.Preferences)
	}
}

func TestBuildSnapshot_FatigueWordsMergeByWord(t *testing.T) {
	snap := BuildSnapshot([]Candidate{
		{Source: "system_defaults", Structured: Structured{FatigueWords: map[string]int{"竟然": 1, "仿佛": 2}}},
		{Source: "project:p.md", Structured: Structured{FatigueWords: map[string]int{"仿佛": 5}}},
	})
	if snap.Structured.FatigueWords["竟然"] != 1 {
		t.Fatalf("竟然 应保留 defaults 阈值 1，得到 %d", snap.Structured.FatigueWords["竟然"])
	}
	if snap.Structured.FatigueWords["仿佛"] != 5 {
		t.Fatalf("仿佛 应被 project 覆盖为 5，得到 %d", snap.Structured.FatigueWords["仿佛"])
	}
}

func TestBuildSnapshot_DegradedPropagates(t *testing.T) {
	snap := BuildSnapshot([]Candidate{
		{Source: "system_defaults", Structured: Structured{FatigueWords: map[string]int{"竟然": 1}}},
		{Source: "project:bad.md", Preferences: "原文降级", Degraded: true},
	})
	if snap.Status != StatusDegraded {
		t.Fatalf("任一来源降级则 status=degraded，得到 %s", snap.Status)
	}
	// 降级来源仍以 raw preferences 进入，不阻断；其它来源 structured 照常。
	if len(snap.Structured.FatigueWords) == 0 {
		t.Fatalf("降级不应影响其它来源的 structured")
	}
	if !strings.Contains(snap.Preferences, "原文降级") {
		t.Fatalf("降级来源应作为 raw preferences 保留")
	}
}

func TestSystemDefaults_MatchesLegacyDefaultMD(t *testing.T) {
	d := SystemDefaults().Structured
	// 4 zh (legacy default.md) + 4 vi (sản phẩm mặc định language=vi).
	if len(d.ForbiddenPhrases) != 8 {
		t.Fatalf("số cụm cấm phải là 8 (4 zh + 4 vi), được %d", len(d.ForbiddenPhrases))
	}
	// 16 zh + 14 vi ("khẽ" nới ngưỡng cho văn dịch).
	if len(d.FatigueWords) != 30 {
		t.Fatalf("số từ mệt mỏi phải là 30 (16 zh + 14 vi), được %d", len(d.FatigueWords))
	}
}

// SystemDefaults phải chứa cả bộ tiếng Việt (sản phẩm mặc định language=vi) —
// chuỗi vi chỉ khớp văn Việt, chuỗi zh chỉ khớp văn Trung, cùng tồn tại an toàn.
func TestSystemDefaultsIncludeVietnamese(t *testing.T) {
	def := SystemDefaults()
	found := false
	for _, p := range def.Structured.ForbiddenPhrases {
		if p == "theo một cách nào đó" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ForbiddenPhrases phải có mẫu tiếng Việt, got %v", def.Structured.ForbiddenPhrases)
	}
	if def.Structured.FatigueWords["khẽ"] < 1 {
		t.Fatalf("FatigueWords phải có 'khẽ', got %v", def.Structured.FatigueWords)
	}
}
