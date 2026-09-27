# Kế hoạch sửa gốc rễ: Hệ thống Style/Thể loại + Đầu đề chương

## Bối cảnh / Triệu chứng

Truyện tu tiên (`E:\work\AI\AI Write Story\novel-moi`) bị:
1. Xưng hô hiện đại lọt vào ("Anh/Em") dù người kể dùng "y" (cổ trang) → trộn hai hệ xưng hô.
2. Chi tiết đời thường hiện đại Việt lọt vào world tu tiên (canh rong, xu, chợ Đông, dép...).
3. Đầu đề chương trên đĩa mất số: `chapters/01.md` = `# Bát canh rong` (không có "Chương 1").

## 4 gốc rễ (đã xác minh bằng grep/đọc source)

| # | Gốc rễ | Bằng chứng |
|---|---|---|
| R1 | Không có bước chọn thể loại — setup hardcode `Style:"default"`; `/config` không có mục style | `internal/bootstrap/setup.go:177`; `internal/entry/tui/command_config.go` `hubFields()` chỉ có protocol/api/key/baseurl/models/test/save |
| R2 | Không có style "tu tiên"; label tiếng Việt chưa map; `assets/styles/zh/` thiếu `wuxia.md` | `ls assets/styles/`; `ls assets/styles/zh/` |
| R3 | Genre không nối với style — `user_rules.genre="tiên hiệp"` bị bỏ rơi | grep `Genre.*Style` → trống |
| R4 | Engine không thêm số chương vào đầu đề | `internal/domain/chapter_heading.go` — `ApplyChapterHeading` render `# {title}`, `FixChapterNumber` chỉ *sửa* số nếu đã có |
| R5 | Writer prompt không neo ngữ vực/xưng hô vào `world_rules`/`preferences` | `assets/prompts/writer.md` không có chỉ thị ràng buộc bối cảnh |

## Quyết định đã chốt với người dùng

1. **Style key giữ `wuxia`** (KHÔNG tạo key mới). Chỉ Việt hóa nhãn: "Võ hiệp / Tu tiên". Map các mô tả tiếng Việt (tu tiên, tiên hiệp, kiếm hiệp, huyền huyễn, xianxia) → `wuxia`.
2. **Định dạng đầu đề: `# Chương N: {title}`** (có dấu hai chấm). Với zh: `# 第 N 章: {title}` — dùng `labelsFor(lang).chapterFmt`.
3. **Làm tới R5.**

## Ràng buộc kỹ thuật

- Repo có ~210 file chưa commit (bản Việt hóa). KHÔNG dùng worktree mode. Chỉ động đúng file cần.
- Không phá vỡ tương thích ngược: `config.Style` cũ ("default"/"fantasy"/...) vẫn phải chạy.
- Mọi thay đổi phải có test. Build Go phải pass (`go build ./...`, `go test ./...`).

---

## Nhiệm vụ

### T1 — Registry style (nguồn chân lý duy nhất)
Tạo registry ánh xạ style key ↔ nhãn tiếng Việt ↔ alias thể loại. Đề xuất đặt trong package `assets` (sở hữu các file `styles/*.md`) hoặc package trung lập mới — **kiểm tra tránh import cycle** trước (assets hiện import `internal/tools`).

```go
type StyleOption struct {
    Key         string   // "wuxia" — khớp tên file assets/styles/<key>.md
    Label       string   // nhãn tiếng Việt hiển thị
    Description string   // mô tả tiếng Việt ngắn
    Aliases     []string // từ khóa thể loại: "tu tiên","tiên hiệp","kiếm hiệp","huyền huyễn","xianxia","wuxia"
}
var StyleOptions = []StyleOption{ default, fantasy, romance, suspense, wuxia }
func ResolveStyleFromGenre(text string) (key string, ok bool)  // khớp alias, không phân biệt hoa thường/dấu
```
- `wuxia`: Label "Võ hiệp / Tu tiên", Aliases gồm "tu tiên","tu tiên","tiên hiệp","kiếm hiệp","huyền huyễn","xianxia","wuxia","võ hiệp".
- `fantasy`: "Kỳ ảo / Fantasy"; `romance`: "Ngôn tình"; `suspense`: "Trinh thám / Kinh dị"; `default`: "Chung (không đặc thù)".

### T2 — Setup wizard: thêm bước chọn thể loại
`internal/bootstrap/setup.go`:
- Thêm `runStyleSelect()` theo mẫu `runLanguageSelect()` (dùng `setupSelectModel`).
- Chèn bước chọn thể loại (đổi tổng số bước 5 → 6, cập nhật nhãn `[x/6]`).
- Thay hardcode `Style: "default"` bằng `Style: selectedStyle.Key`.
- Cập nhật dòng in tóm tắt cuối để hiển thị nhãn thể loại.

### T3 — `/config` TUI: thêm field style
`internal/entry/tui/command_config.go`:
- Thêm `hubField{"style", "Thể loại", <nhãn>}` vào `hubFields()`.
- Thêm step chọn style (màn hình select tương tự chọn provider) + ghi vào config qua đường save hiện có (`bootstrap.SaveConfig`).
- Hiển thị nhãn tiếng Việt, không hiển thị key thô.

### T4 — Map genre → style (R3)
- Khi bắt đầu truyện mới, nếu `cfg.Style == "" || cfg.Style == "default"` và phát hiện genre từ `start_prompt` / `user_rules.genre` → gọi `ResolveStyleFromGenre`, set style.
- Ghi style đã resolve vào `RunMeta.Style` (đã có field, `internal/domain/runtime.go:197`).
- **Quan trọng:** host cần nạp bundle theo style của sách. Hiện `cmd/ainovel-cli/main.go:104` nạp 1 lần từ `cfg.Style`. Cần để host reload bundle theo `RunMeta.Style` khi mở/tạo sách (dùng `assets.LoadWithLanguage(lang, bookStyle, opts)`), fallback `cfg.Style`. Kiểm tra kỹ luồng resume để không phá.
- Nếu cách trên quá rủi ro: tối thiểu set `cfg.Style` + persist config, và ghi rõ giới hạn trong handoff. NHƯNG ưu tiên per-book vì đó là gốc.

### T5 — Đầu đề chương có số (R4)
`internal/domain/chapter_heading.go`:
- `ApplyChapterHeading(content, title, chapter)` render `# <chapterFmt>: {title}` với `chapterFmt` theo ngôn ngữ (vi: `Chương N`, zh: `第 N 章`). Thêm tham số lang HOẶC dùng chuỗi đã format sẵn từ caller.
- Caller: `internal/tools/commit_chapter.go:234` và `:576` — truyền lang + số chương.
- Giữ `FixChapterNumber` (chuẩn hóa số nếu model tự viết), nhưng đảm bảo output cuối luôn có số.
- Cập nhật `internal/domain/chapter_heading_test.go` (đang kỳ vọng `# Chương 9: Đêm Trắng` — giữ nguyên hành vi này, chỉ đảm bảo engine luôn sinh ra nó).
- Kiểm tra không phá `internal/store/labels.go` (`chapterFmt: "Chương %d"` / `"第 %d 章"`).

### T6 — Neo bối cảnh vào writer prompt (R5)
- `assets/prompts/writer.md`: thêm chỉ thị ngữ vực — xưng hô + chi tiết đời sống phải theo `world_rules` + `preferences`; CẤM mặc định văn hóa/đại từ hiện đại khi bối cảnh không phải hiện đại.
- Tương ứng `assets/prompts/zh/writer.md`.
- `assets/voice.md` (+ `voice_zh.md`): thêm mục cùng nội dung (chuẩn hành văn áp mọi chương).
- Bổ sung luật xưng hô cổ trang vào `assets/styles/wuxia.md` nếu cần (đã có dòng "Xưng hô và ngữ vực cổ trang" — kiểm tra đủ mạnh, bổ sung cấm đại từ hiện đại rõ hơn).

### T7 — Tạo `assets/styles/zh/wuxia.md` (bug phụ R2)
- Bản tiếng Trung của `styles/wuxia.md`. Hiện `styles/zh/` thiếu file này → chọn zh+wuxia sẽ không nạp style nào.

---

## Tiêu chí nghiệm thu

1. `go build ./...` và `go test ./...` pass.
2. Setup wizard có bước chọn thể loại; chọn "Võ hiệp / Tu tiên" → `config.Style == "wuxia"`.
3. `/config` sửa được thể loại.
4. `ResolveStyleFromGenre("tiên hiệp") == "wuxia"` (có test).
5. Chương mới commit có đầu đề `# Chương N: {title}` (có test).
6. `assets/styles/zh/wuxia.md` tồn tại.
7. writer prompt + voice có chỉ thị neo bối cảnh.

## Ngoài phạm vi
- KHÔNG sửa 3 chương đã viết trong novel-moi (người dùng tự xử lý sau).
- KHÔNG refactor ngoài các file liệt kê.
