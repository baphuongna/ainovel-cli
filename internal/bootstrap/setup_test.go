package bootstrap

import (
	"strings"
	"testing"

	"github.com/voocel/ainovel-cli/internal/styles"
)

// styleSelectItems phải giữ nguyên thứ tự và key của styles.StyleOptions:
// runStyleSelect trả về styles.StyleOptions[result.cursor] theo đúng index đó,
// nên lệch thứ tự sẽ khiến người dùng chọn thể loại này nhưng config lưu thể loại khác.
func TestStyleSelectItemsFollowRegistryOrder(t *testing.T) {
	items := styleSelectItems()
	if len(items) != len(styles.StyleOptions) {
		t.Fatalf("số mục chọn thể loại = %d, muốn %d", len(items), len(styles.StyleOptions))
	}
	for i, opt := range styles.StyleOptions {
		item := items[i]
		if item.name != opt.Key {
			t.Errorf("items[%d].name = %q, muốn key %q", i, item.name, opt.Key)
		}
		if !strings.Contains(item.label, opt.Label) {
			t.Errorf("items[%d].label = %q, thiếu nhãn %q", i, item.label, opt.Label)
		}
		if !strings.Contains(item.label, opt.Description) {
			t.Errorf("items[%d].label = %q, thiếu mô tả %q", i, item.label, opt.Description)
		}
		if item.label == "" {
			t.Errorf("items[%d].label rỗng", i)
		}
	}
}

// Wizard phải có mục "Võ hiệp / Tu tiên" map về key bền vững "wuxia"
// (tiêu chí nghiệm thu: chọn nhãn này → config.Style == "wuxia").
func TestStyleSelectItemsContainWuxiaOption(t *testing.T) {
	wuxiaLabel := styles.LabelFor("wuxia")
	if wuxiaLabel == "" {
		t.Fatal("registry không có style wuxia — không thể kiểm thử bước chọn thể loại")
	}
	found := false
	for _, item := range styleSelectItems() {
		if item.name == "wuxia" {
			found = true
			if !strings.Contains(item.label, wuxiaLabel) {
				t.Errorf("mục wuxia hiển thị %q, thiếu nhãn %q", item.label, wuxiaLabel)
			}
		}
	}
	if !found {
		t.Error("danh sách chọn thể loại không có mục wuxia")
	}
}

// setupSelectModel.View phải hiển thị đủ nhãn + mô tả của từng thể loại
// (View là hàm thuần, test được không cần chạy toàn bộ chương trình TUI).
func TestSetupSelectViewRendersStyleLabels(t *testing.T) {
	m := setupSelectModel{title: "[2/6] Chọn Thể Loại Truyện", items: styleSelectItems()}
	view := m.View()
	for _, opt := range styles.StyleOptions {
		if !strings.Contains(view, opt.Label) {
			t.Errorf("View() thiếu nhãn thể loại %q", opt.Label)
		}
		if !strings.Contains(view, opt.Description) {
			t.Errorf("View() thiếu mô tả thể loại %q", opt.Description)
		}
	}
}
