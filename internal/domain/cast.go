package domain

// CastEntry là một mục trong danh sách nhân vật phụ.
//
// Tách rời khỏi Character (characters.json, hồ sơ lõi do Architect duy trì):
//   - CastEntry do công cụ commit_chapter tự động dồn thêm, ghi lại "nhân vật phụ có tên đã từng xuất hiện"
//   - Character do Architect thiết kế tường minh, ghi lại vòng cung nhân cách/đặc điểm/tier của nhân vật chính và nhân vật phụ trọng yếu
//
// Khi trùng tên thì lấy Character làm chuẩn (nhân vật lõi không vào cast_ledger), tránh trùng lặp.
type CastEntry struct {
	Name string `json:"name"`
	// Aliases hiện chưa có kênh ghi; dành cho công cụ tương lai "gộp biệt danh theo steer của người dùng"
	// (ví dụ khai báo 'Lý chưởng quầy' và 'lão Lý' là cùng một người). MergeAppearances đã hỗ trợ tra cứu biệt danh.
	Aliases          []string `json:"aliases,omitempty"`
	BriefRole        string   `json:"brief_role,omitempty"` // định vị một câu (Writer điền khi xuất hiện lần đầu, có thể bổ sung sau; không bị ghi đè)
	FirstSeenChapter int      `json:"first_seen_chapter"`
	LastSeenChapter  int      `json:"last_seen_chapter"`
	// AppearanceCount suy ra từ len(AppearanceChapters), giữ đồng bộ khi merge.
	// Giữ trường tường minh để UI/JSON đọc trực tiếp, không cần tính lại mỗi lần.
	AppearanceCount    int   `json:"appearance_count"`
	AppearanceChapters []int `json:"appearance_chapters"`
	// Promoted đánh dấu mục này đã được thăng cấp vào characters.json. RecentActive sẽ bỏ qua
	// các mục này, tránh triệu hồi trùng với hồ sơ lõi. Hiện kênh thăng cấp chưa hiện thực,
	// trường này là hook dự phòng.
	Promoted bool `json:"promoted,omitempty"`
}

// CastIntro là lời tuyên bố giới thiệu nhân vật mới xuất hiện mà Writer đưa khi commit_chapter.
// Chỉ được chấp nhận khi tên đó xuất hiện lần đầu hoặc BriefRole trong ledger vẫn còn trống.
type CastIntro struct {
	Name      string `json:"name"`
	BriefRole string `json:"brief_role"`
}
