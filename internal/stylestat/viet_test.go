package stylestat

import (
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

// Trước đây minePhrases chỉ nhận đoạn thuần chữ Hán nên top_phrases của sách vi luôn rỗng.
func TestMinePhrasesVietnamese(t *testing.T) {
	body := "Lâm Phong đứng dậy. Khóe môi khẽ nhếch lên, hắn bước ra cửa. " +
		"Gió thổi qua sân. Khóe môi khẽ nhếch lên lần nữa.\n"
	chapters := make([]string, 8)
	for i := range chapters {
		chapters[i] = chapterWith(body)
	}
	s := Compute(Input{Chapters: chapters, Stopwords: []string{"Lâm Phong"}})
	if s == nil || len(s.TopPhrases) == 0 {
		t.Fatalf("expected Vietnamese top phrases, got %+v", s)
	}
	found := false
	for _, p := range s.TopPhrases {
		if strings.Contains(p.Text, "phong") || strings.Contains(p.Text, "lâm") {
			t.Errorf("tên nhân vật lọt vào top phrases: %+v", p)
		}
		if strings.Contains(p.Text, "khóe môi khẽ nhếch") {
			found = true
			if p.Count != 16 {
				t.Errorf("count = %d, want 16", p.Count)
			}
		}
		first := strings.Fields(p.Text)[0]
		if _, stop := viEdgeStop[first]; stop {
			t.Errorf("cụm bắt đầu bằng hư từ: %q", p.Text)
		}
	}
	if !found {
		t.Errorf("thiếu cụm 'khóe môi khẽ nhếch': %+v", s.TopPhrases)
	}
}

// Văn bản NFD (dấu tổ hợp tách rời) phải được đếm giống hệt NFC.
func TestComputeNormalizesNFD(t *testing.T) {
	body := "Anh khẽ nhíu mày. Một cảm giác khó tả dâng lên trong lòng anh.\n"
	nfc := make([]string, 6)
	nfd := make([]string, 6)
	for i := range nfc {
		nfc[i] = chapterWith(body)
		nfd[i] = norm.NFD.String(nfc[i])
	}
	if nfd[0] == nfc[0] {
		t.Fatal("fixture NFD phải khác NFC ở mức byte")
	}
	a, b := Compute(Input{Chapters: nfc}), Compute(Input{Chapters: nfd})
	if len(a.Patterns) == 0 || len(a.Patterns) != len(b.Patterns) {
		t.Fatalf("NFC patterns=%+v, NFD patterns=%+v", a.Patterns, b.Patterns)
	}
	for i := range a.Patterns {
		if a.Patterns[i] != b.Patterns[i] {
			t.Errorf("pattern %d: NFC %+v != NFD %+v", i, a.Patterns[i], b.Patterns[i])
		}
	}
	tr := NewTracker()
	for i, c := range nfd {
		tr.Upsert(i+1, c)
	}
	if got := tr.Snapshot(nil, nil); len(got.Patterns) != len(a.Patterns) {
		t.Errorf("Tracker trên NFD: %+v, want %+v", got.Patterns, a.Patterns)
	}
}

// Ngữ cảnh vô hại không bị tính là tic văn phong.
func TestVietnamesePatternExclusions(t *testing.T) {
	body := "Căn phòng im lặng. Sự im lặng kéo dài. Cứ coi như một lời hứa.\n"
	chapters := make([]string, 6)
	for i := range chapters {
		chapters[i] = chapterWith(body)
	}
	s := Compute(Input{Chapters: chapters})
	for _, p := range s.Patterns {
		if strings.HasPrefix(p.Name, "Nhịp im lặng") || strings.HasPrefix(p.Name, "Ví von trực tiếp \"như một") {
			t.Errorf("ngữ cảnh vô hại bị đếm: %+v", p)
		}
	}
}

// Câu tiếng Việt ngắn ("Hắn im lặng.") lặp lại là bình thường, không phải bằng chứng lặp phần kể.
func TestRepeatedSentencesVietnameseThreshold(t *testing.T) {
	counts := chapterSentenceCounts("Hắn gật đầu. Nàng mỉm cười. Hắn chậm rãi bước qua cánh cổng đá phủ đầy rêu xanh.")
	if _, ok := counts["Hắn gật đầu"]; ok {
		t.Errorf("câu quá ngắn bị tính: %+v", counts)
	}
	if len(counts) != 1 {
		t.Errorf("counts = %+v, want chỉ câu dài", counts)
	}
}
