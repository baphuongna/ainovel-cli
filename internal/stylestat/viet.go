package stylestat

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Khai thác cụm từ cho văn tiếng Việt (và văn chữ Latin nói chung).
//
// Thuật toán gốc (minePhrases → n-gram 3-6 ký tự) chỉ nhận đoạn thuần chữ Hán, nên với
// sách language=vi mục top_phrases luôn rỗng và Editor mất tín hiệu "câu cửa miệng".
// Tiếng Việt là ngôn ngữ đơn lập: đơn vị nghĩa là âm tiết cách nhau bởi khoảng trắng,
// một từ thường gồm 1-2 âm tiết, nên cụm văn phong đáng chú ý ≈ 3-5 âm tiết
// ("khóe môi khẽ nhếch", "một cảm giác khó tả"). n-gram được đếm theo âm tiết và không
// vượt qua ranh giới câu.

const (
	wordGramMin = 3
	wordGramMax = 5
)

// viEdgeStop hư từ / đại từ / từ nối: cụm có đầu hoặc cuối là các âm tiết này thường
// là mảnh ghép ngữ pháp ("và hắn nói", "của nàng"), không phải cụm văn phong.
var viEdgeStop = func() map[string]struct{} {
	words := strings.Fields(`và là của thì mà đã đang sẽ cũng vẫn còn những các một này đó kia ấy
	với cho trong ngoài trên dưới để khi nếu nhưng rồi lại ra vào không có được bị như
	hắn nàng anh chị em tôi ta nó họ y gã cô ông bà mình chàng lão ngươi
	thế vậy nào gì ai đâu sao lúc về từ đến tới theo bởi vì nên hay hoặc cả rất quá`)
	m := make(map[string]struct{}, len(words))
	for _, w := range words {
		m[w] = struct{}{}
	}
	return m
}()

// hanDominant cho biết văn bản chủ yếu là chữ Hán (dùng thuật toán n-gram ký tự gốc)
// hay chữ Latin (dùng n-gram âm tiết).
func hanDominant(text string) bool {
	han, latin := 0, 0
	for _, r := range text {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF:
			han++
		case unicode.IsLetter(r):
			latin++
		}
	}
	return han > latin
}

// NormalizeText chuẩn hóa về NFC. Văn bản nhập qua /import hay sửa tay (/sync) trên
// macOS / một số editor có thể ở dạng NFD (dấu tổ hợp tách rời), khi đó regex tiếng Việt,
// forbidden_phrases và so khớp câu lặp đều trượt dù nhìn bằng mắt giống hệt.
func NormalizeText(s string) string {
	if norm.NFC.IsNormalString(s) {
		return s
	}
	return norm.NFC.String(s)
}

// syllableSentences tách văn bản thành các câu, mỗi câu là dãy âm tiết đã chữ thường.
func syllableSentences(text string) [][]string {
	var out [][]string
	for _, sentence := range sentenceSplit.Split(NormalizeText(text), -1) {
		// Dấu phẩy, chấm phẩy, gạch ngang, ngoặc cũng cắt cụm: cụm văn phong không vắt qua chúng.
		for _, clause := range strings.FieldsFunc(sentence, func(r rune) bool {
			return strings.ContainsRune(",;:—–()[]\"“”‘’«»…", r)
		}) {
			words := strings.FieldsFunc(strings.ToLower(clause), func(r rune) bool {
				return !(unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r) || r == '-')
			})
			if len(words) >= wordGramMin {
				out = append(out, words)
			}
		}
	}
	return out
}

// nameSyllables tách tên riêng thành âm tiết chữ thường. Tên Việt thường lọt vào văn dưới
// dạng một phần ("Phong" của "Lâm Phong"), nên cụm chứa bất kỳ âm tiết tên nào đều bị lọc —
// giống triết lý stopwordBigrams: thà thiếu một cụm còn hơn để tên người lẫn vào danh sách.
func nameSyllables(stopwords []string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, w := range stopwords {
		for _, s := range strings.Fields(strings.ToLower(NormalizeText(w))) {
			if len([]rune(s)) >= 2 {
				m[s] = struct{}{}
			}
		}
	}
	return m
}

func mineWordPhrases(chapters []string, stopwords []string) []PhraseStat {
	threshold := max(8, len(chapters)/2)
	names := nameSyllables(stopwords)

	counts := make(map[string]int)
	for _, text := range chapters {
		for _, words := range syllableSentences(text) {
			for size := wordGramMin; size <= wordGramMax; size++ {
				for i := 0; i+size <= len(words); i++ {
					gram := words[i : i+size]
					if !validWordGram(gram, names) {
						continue
					}
					counts[strings.Join(gram, " ")]++
				}
			}
		}
	}

	type cand struct {
		text  string
		words int
		count int
	}
	var cands []cand
	for g, c := range counts {
		if c >= threshold {
			cands = append(cands, cand{g, strings.Count(g, " ") + 1, c})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].count != cands[j].count {
			return cands[i].count > cands[j].count
		}
		if cands[i].words != cands[j].words {
			return cands[i].words > cands[j].words
		}
		return cands[i].text < cands[j].text
	})

	var out []PhraseStat
	for _, c := range cands {
		if len(out) >= 8 {
			break
		}
		dup := false
		for _, picked := range out {
			if containsWords(picked.Text, c.text) || containsWords(c.text, picked.Text) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, PhraseStat{Text: c.text, Count: c.count})
		}
	}
	return out
}

func validWordGram(gram []string, names map[string]struct{}) bool {
	if _, ok := viEdgeStop[gram[0]]; ok {
		return false
	}
	if _, ok := viEdgeStop[gram[len(gram)-1]]; ok {
		return false
	}
	for _, w := range gram {
		if _, ok := names[w]; ok {
			return false
		}
		for _, r := range w {
			if unicode.IsDigit(r) {
				return false
			}
		}
	}
	return true
}

// containsWords kiểm tra sub có phải dãy âm tiết con liên tiếp của s (khớp theo ranh giới từ,
// tránh "an" khớp nhầm vào "hoang mang").
func containsWords(s, sub string) bool {
	return strings.Contains(" "+s+" ", " "+sub+" ")
}

// minSentenceRunes ngưỡng độ dài câu để tính "câu lặp nguyên văn". 12 ký tự chữ Hán đã là
// một câu đầy đủ, nhưng 12 ký tự tiếng Việt chỉ khoảng 2-3 âm tiết ("Hắn im lặng.") — lặp
// là chuyện bình thường, không phải bằng chứng lặp phần kể.
func minSentenceRunes(sentence string) int {
	if hanDominant(sentence) {
		return 12
	}
	return 30
}
