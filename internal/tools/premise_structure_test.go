package tools

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestParsePremiseSections(t *testing.T) {
	premise := `# Premise

## 题材和基调
东方玄幻，冷硬成长。

## 题材定位
东方玄幻升级流，面向追求爽点和关系推进的读者。

## 核心冲突
主角必须在宗门规则与个人良知之间做选择。

## 中期转向
旧有修炼路线失效，必须转向禁术体系。
`

	sections := parsePremiseSections(premise)
	if sections["Thể loại và giọng điệu"] == "" {
		t.Fatalf("expected Thể loại và giọng điệu (zh alias 题材和基调), got %+v", sections)
	}
	if sections["Định vị thể loại"] == "" {
		t.Fatalf("expected Định vị thể loại (zh alias 题材定位), got %+v", sections)
	}
	if sections["Xung đột cốt lõi"] == "" {
		t.Fatalf("expected Xung đột cốt lõi (zh alias 核心冲突), got %+v", sections)
	}
	if sections["Chuyển hướng trung kỳ"] == "" {
		t.Fatalf("expected alias 中期转向 → Chuyển hướng trung kỳ, got %+v", sections)
	}
}

// Truyện tiếng Việt (mặc định sản phẩm): heading đúng như prompt architect yêu cầu,
// kể cả biến thể kèm chú thích "Tên (chú thích)" / "Tên: giải thích" của architect-long.
func TestParsePremiseSectionsVietnamese(t *testing.T) {
	premise := `# Tiền đề cốt truyện

## Định vị thể loại (Độc giả mục tiêu, điểm tiêu thụ cốt lõi)
Huyền giác đô thị, hướng tới độc giả thích phá án siêu nhiên.

## Xung đột cốt lõi
Sự thật và tình thân xung đột trực tiếp.

## Móc câu khác biệt: Điểm độc đáo nhất đáng để độc giả theo dõi cuốn sách này
Nhân vật chính nhìn thấy vết nứt thời gian sau mỗi cơn đau đầu.
`

	sections := parsePremiseSections(premise)
	if sections["Định vị thể loại"] == "" {
		t.Fatalf("heading vi kèm chú thích trong ngoặc phải chuẩn hoá được, got %+v", sections)
	}
	if sections["Xung đột cốt lõi"] == "" {
		t.Fatalf("expected Xung đột cốt lõi, got %+v", sections)
	}
	if sections["Móc câu khác biệt"] == "" {
		t.Fatalf("heading vi kèm chú thích sau ':' phải chuẩn hoá được, got %+v", sections)
	}
}

func TestPremiseStructure(t *testing.T) {
	premise := `## 题材和基调
升级流，偏冷硬。

## 题材定位
升级流

## 核心冲突
冲突

## 主角目标
目标

## 终局方向
终局

## 写作禁区
禁区

## 差异化卖点
卖点

## 差异化钩子
钩子

## 核心兑现承诺
兑现

## 故事引擎
引擎

## 中段转折
转折
`

	structure := premiseStructure(premise, domain.PlanningTierMid)
	if ready, _ := structure["template_ready"].(bool); !ready {
		t.Fatalf("expected template_ready, got %+v", structure)
	}
	missing, _ := structure["missing"].([]string)
	if len(missing) != 0 {
		t.Fatalf("expected no missing headings, got %+v", missing)
	}
}

func TestPremiseStructureShortAcceptsLegacyHeadingAlias(t *testing.T) {
	premise := `## 题材和基调
单卷高压营救。

## 题材定位
短篇高密度冒险。

## 核心冲突
主角必须在一夜内救出人质。

## 主角目标
救出人质并活着离开。

## 结局方向
完成任务但付出代价。

## 写作禁区
不扩展成长期连载。

## 差异化卖点
时限压力与连续反转。

## 差异化钩子
每次选择都缩短救援时间。

## 核心兑现承诺
紧迫感、抉择与反转。

## 本作为什么适合短篇/单卷收束
核心矛盾和人物弧线都能在单次任务中完成。
`

	structure := premiseStructure(premise, domain.PlanningTierShort)
	if ready, _ := structure["template_ready"].(bool); !ready {
		t.Fatalf("expected short template_ready, got %+v", structure)
	}
}
