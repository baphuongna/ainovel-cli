package store

// mdLabels là các nhãn cố định trong view Markdown dẫn xuất.
//
// Những .md này được novel_context đọc lại vào ngữ cảnh model, nên ngôn ngữ nhãn
// không chỉ là chuyện hiển thị: chính văn tiếng Việt mà mỗi chương vẫn đội nhãn kiểu
// "第 N 章", "核心事件" (nhãn tiếng Trung) thì chẳng khác nào liên tục ám chỉ model
// đang ở ngữ cảnh tiếng Trung, quan sát thực tế cho thấy khiến chính văn lẫn chữ Hán
// (quan sát草木 / Bạo虐 / dấu顿号 kiểu hỗn hợp). Nhãn phải theo ngôn ngữ tác phẩm thì
// mới không kéo co với ngôn ngữ đầu ra của model.
type mdLabels struct {
	bookTitleFmt string
	synopsis     string
	charProfiles string
	charArc      string
	traits       string
	listSep      string
	openParen    string
	closeParen   string
	colon        string

	outline        string
	layeredOutline string
	volumeFmt      string
	arcFmt         string
	chapterFmt     string
	theme          string
	goal           string
	pendingArcFmt  string
	coreEvent      string
	hook           string
	scenes         string

	timeline      string
	foreshadow    string
	resolvedAtFmt string
	plantedAtFmt  string
	relationships string
	atChapterFmt  string
	worldRules    string
	rule          string
	boundary      string
}

var labelsZH = mdLabels{
	bookTitleFmt: "《%s》", synopsis: "简介", charProfiles: "角色档案", charArc: "角色弧线", traits: "特征",
	listSep: "、", openParen: "（", closeParen: "）", colon: "：",

	outline: "大纲", layeredOutline: "分层大纲",
	volumeFmt: "第 %d 卷", arcFmt: "第 %d 弧", chapterFmt: "第 %d 章",
	theme: "主题", goal: "目标", pendingArcFmt: "（待展开，预估 %d 章）",
	coreEvent: "核心事件", hook: "钩子", scenes: "场景",

	timeline: "时间线", foreshadow: "伏笔账本",
	resolvedAtFmt: "已回收（第 %d 章）", plantedAtFmt: "埋设于第 %d 章，状态：%s",
	relationships: "人物关系", atChapterFmt: "（第 %d 章）",
	worldRules: "世界观规则", rule: "规则", boundary: "边界",
}

// labelsVI dùng tên dịch đã định của dự án: tập / cung / chương / đề cương / điểm móc / phục bút.
var labelsVI = mdLabels{
	bookTitleFmt: "%s", synopsis: "Giới thiệu", charProfiles: "Hồ sơ nhân vật", charArc: "Cung nhân vật",
	traits: "Đặc điểm", listSep: ", ", openParen: " (", closeParen: ")", colon: ": ",

	outline: "Đề cương", layeredOutline: "Đề cương phân tầng",
	volumeFmt: "Tập %d", arcFmt: "Cung %d", chapterFmt: "Chương %d",
	theme: "Chủ đề", goal: "Mục tiêu", pendingArcFmt: "*(chưa khai triển, ước %d chương)*",
	coreEvent: "Sự kiện chính", hook: "Điểm móc", scenes: "Cảnh",

	timeline: "Dòng thời gian", foreshadow: "Sổ phục bút",
	resolvedAtFmt: "đã thu ở chương %d", plantedAtFmt: "gieo ở chương %d, trạng thái: %s",
	relationships: "Quan hệ nhân vật", atChapterFmt: " (chương %d)",
	worldRules: "Luật thế giới", rule: "Luật", boundary: "Ranh giới",
}

func labelsFor(lang string) mdLabels {
	if lang == "vi" {
		return labelsVI
	}
	return labelsZH
}
