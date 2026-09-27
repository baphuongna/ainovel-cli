package imp

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// Nhãn mã hóa nguồn được hỗ trợ, ghi vào Manifest và sự kiện tiến độ, không tự ý fallback
// vô thanh (RFC §7.1).
const (
	encodingUTF8    = "utf-8"
	encodingUTF8BOM = "utf-8-bom"
	encodingGB18030 = "gb18030"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// decoded là kết quả một lần giải mã: văn bản + mã hóa thực tế được chọn.
type decoded struct {
	text     string
	encoding string
}

// decodeSource giải mã theo thứ tự UTF-8 / UTF-8 BOM / GB18030, trả về mã hóa đã chọn.
// Không giải mã được tin cậy hoặc xuất hiện ký tự thay thế thì thất bại ngay, lỗi kèm kết quả
// dò được, không giấu "thử GB18030" thành fallback vô thanh.
func decodeSource(raw []byte) (decoded, error) {
	if bytes.HasPrefix(raw, utf8BOM) {
		body := raw[len(utf8BOM):]
		if !utf8.Valid(body) {
			return decoded{}, fmt.Errorf("khai báo UTF-8 BOM nhưng nội dung không phải UTF-8 hợp lệ")
		}
		return decoded{text: string(body), encoding: encodingUTF8BOM}, nil
	}
	if utf8.Valid(raw) {
		return decoded{text: string(raw), encoding: encodingUTF8}, nil
	}
	out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil {
		return decoded{}, fmt.Errorf("không phải UTF-8 hợp lệ, giải mã GB18030 cũng thất bại: %w", err)
	}
	if !utf8.Valid(out) {
		return decoded{}, fmt.Errorf("kết quả giải mã GB18030 vẫn không phải UTF-8 hợp lệ, không thể giải mã tin cậy")
	}
	if i := bytes.IndexRune(out, utf8.RuneError); i >= 0 {
		return decoded{}, fmt.Errorf("giải mã GB18030 xuất hiện ký tự thay thế (U+FFFD @ byte %d), không thể giải mã tin cậy; hãy kiểm tra mã hóa tệp", i)
	}
	return decoded{text: string(out), encoding: encodingGB18030}, nil
}

// normalize chỉ làm các chuyển đổi không đổi nội dung văn học: CRLF/CR thống nhất thành LF.
// Giữ dòng trống, thụt đầu dòng, dòng tiêu đề và ký tự chính văn; không xóa văn bản đầu tệp,
// chương rỗng, quảng cáo hay nhiễu cuối tệp cái gọi là (RFC §7.2).
// BOM đã bị bóc ở giai đoạn decodeSource.
func normalize(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

// Ingest đọc tệp nguồn, giải mã, chuẩn hóa, và bằng rename thư mục tạo atomically snapshot
// workspace meta/import/. Trả về handle workspace và Manifest; bên gọi phát sự kiện tiến độ theo đó.
func Ingest(bookDir, sourcePath string, in Intent) (*Workspace, *Manifest, error) {
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, nil, fmt.Errorf("đọc tệp nguồn thất bại: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil, fmt.Errorf("tệp nguồn rỗng: %s", sourcePath)
	}
	dec, err := decodeSource(raw)
	if err != nil {
		return nil, nil, err
	}
	normBytes := []byte(normalize(dec.text))

	m := Manifest{
		Version:          workspaceSchemaVersion,
		SourceName:       filepath.Base(sourcePath),
		RawSHA256:        Digest(raw),
		NormalizedSHA256: Digest(normBytes),
		Encoding:         dec.encoding,
		SizeBytes:        int64(len(raw)),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	}
	if in.Version == 0 {
		in.Version = workspaceSchemaVersion
	}

	ws, err := createWorkspace(bookDir, m, in, normBytes)
	if err != nil {
		return nil, nil, err
	}
	return ws, &m, nil
}

// SourceUnit là tọa độ ổn định mà mô hình có thể tham chiếu (RFC §7.3).
// ID chỉ dùng để hiển thị và để mô hình tham chiếu; mọi phép so thứ tự/bao chứa/tăng đều theo
// thứ tự số (Line, Part), cấm so từ điển trên chuỗi ID.
type SourceUnit struct {
	ID        string `json:"id"`   // L1257; dòng siêu dài tách thành L1257.1, L1257.2
	Line      int    `json:"line"` // đánh từ 1
	Part      int    `json:"part"` // 0=cả dòng; phân mảnh ảo 1..N
	StartByte int    `json:"start_byte"`
	EndByte   int    `json:"end_byte"`
	Text      string `json:"text"`
}

// unitLess định nghĩa thứ tự toàn phần của SourceUnit: Line trước Part sau, đều so số (bản sửa A1).
func unitLess(a, b SourceUnit) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Part < b.Part
}

// buildSourceUnits dựng bảng tọa độ ổn định từ văn bản đã chuẩn hóa.
// Dòng thường một unit; dòng đơn vượt quá maxUnitBytes byte thì chỉ sinh nhiều unit ảo tại
// ranh giới ký tự UTF-8, không ghi lại source.txt, không chèn xuống dòng mềm, không đổi bất kỳ
// ký tự nguồn nào (RFC §7.3). maxUnitBytes<=0 nghĩa là không tách.
func buildSourceUnits(normalized []byte, maxUnitBytes int) []SourceUnit {
	var units []SourceUnit
	n := len(normalized)
	line := 0
	offset := 0
	for offset < n {
		nl := bytes.IndexByte(normalized[offset:], '\n')
		lineEnd := n
		if nl >= 0 {
			lineEnd = offset + nl
		}
		line++
		if maxUnitBytes > 0 && lineEnd-offset > maxUnitBytes {
			part := 0
			s := offset
			for s < lineEnd {
				e := s + maxUnitBytes
				if e >= lineEnd {
					e = lineEnd
				} else {
					for e > s && !utf8.RuneStart(normalized[e]) {
						e--
					}
					if e == s { // fallback cực đoan cho một rune siêu dài duy nhất
						e = s + maxUnitBytes
					}
				}
				part++
				units = append(units, SourceUnit{
					ID: fmt.Sprintf("L%d.%d", line, part), Line: line, Part: part,
					StartByte: s, EndByte: e, Text: string(normalized[s:e]),
				})
				s = e
			}
		} else {
			units = append(units, SourceUnit{
				ID: fmt.Sprintf("L%d", line), Line: line, Part: 0,
				StartByte: offset, EndByte: lineEnd, Text: string(normalized[offset:lineEnd]),
			})
		}
		if nl < 0 {
			break
		}
		offset = lineEnd + 1
	}
	return units
}

// resolveBoundaryByte ánh xạ một quyết định ranh giới thành vị trí byte chính xác:
// không có anchor thì lấy điểm đầu unit; có anchor thì yêu cầu khớp đúng từng chữ, duy nhất
// trong unit đó, rồi ánh xạ thành offset byte (RFC §8.3).
func resolveBoundaryByte(unitByID map[string]SourceUnit, unitID, anchor string) (int, error) {
	u, ok := unitByID[unitID]
	if !ok {
		return 0, fmt.Errorf("ranh giới tham chiếu unit không tồn tại: %s", unitID)
	}
	if anchor == "" {
		return u.StartByte, nil
	}
	switch strings.Count(u.Text, anchor) {
	case 0:
		return 0, fmt.Errorf("anchor %q không nằm trong unit %s", anchor, unitID)
	case 1:
		return u.StartByte + strings.Index(u.Text, anchor), nil
	default:
		return 0, fmt.Errorf("anchor %q không duy nhất trong unit %s", anchor, unitID)
	}
}
