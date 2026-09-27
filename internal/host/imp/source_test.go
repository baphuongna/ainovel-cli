package imp

import (
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestDecodeSourceUTF8(t *testing.T) {
	d, err := decodeSource([]byte("第一章　风起\n正文"))
	if err != nil {
		t.Fatalf("utf-8: %v", err)
	}
	if d.encoding != encodingUTF8 || d.text != "第一章　风起\n正文" {
		t.Fatalf("kết quả utf-8 không khớp: %+v", d)
	}
}

func TestDecodeSourceUTF8BOM(t *testing.T) {
	raw := append(append([]byte{}, utf8BOM...), []byte("楔子")...)
	d, err := decodeSource(raw)
	if err != nil {
		t.Fatalf("bom: %v", err)
	}
	if d.encoding != encodingUTF8BOM || d.text != "楔子" {
		t.Fatalf("kết quả bom không khớp: %+v", d)
	}
}

func TestDecodeSourceGB18030(t *testing.T) {
	want := "第一章　风起\n正文内容"
	gb, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(want))
	if err != nil {
		t.Fatalf("mã hóa dữ liệu kiểm thử GB18030 thất bại: %v", err)
	}
	d, err := decodeSource(gb)
	if err != nil {
		t.Fatalf("gb18030: %v", err)
	}
	if d.encoding != encodingGB18030 || d.text != want {
		t.Fatalf("kết quả gb18030 không khớp: %+v", d)
	}
}

func TestDecodeSourceBOMInvalidBodyFails(t *testing.T) {
	raw := append(append([]byte{}, utf8BOM...), []byte{0xFF, 0xFE}...)
	if _, err := decodeSource(raw); err == nil {
		t.Fatal("khai báo BOM mà nội dung bất hợp lệ phải thất bại")
	}
}

func TestNormalizeLineEndings(t *testing.T) {
	if got := normalize("a\r\nb\rc\nd"); got != "a\nb\nc\nd" {
		t.Fatalf("chuẩn hóa không khớp: %q", got)
	}
	// Dòng trống và thụt đầu dòng phải được giữ.
	if got := normalize("第一章\r\n\r\n\t正文"); got != "第一章\n\n\t正文" {
		t.Fatalf("dòng trống/thụt lề không được giữ: %q", got)
	}
}
