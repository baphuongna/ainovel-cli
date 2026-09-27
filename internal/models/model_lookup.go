package models

import "strings"

// SameModelID kiểm tra hai định danh model có trỏ tới cùng một model chuẩn hay không (bỏ qua
// hậu tố ngày, hoa/thường, khác biệt dấu chấm/gạch ngang).
func SameModelID(a, b string) bool {
	return modelLookupMatches(normalizeModelLookupID(a), normalizeModelLookupID(b))
}

func lookupModelEntry(models []ModelEntry, providerName, modelID string) (ModelEntry, bool) {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	targetID := normalizeModelLookupID(modelID)
	for _, m := range models {
		if providerName != "" && !strings.EqualFold(m.Provider, providerName) {
			continue
		}
		if modelLookupMatches(normalizeModelLookupID(m.ID), targetID) {
			return m, true
		}
	}
	return ModelEntry{}, false
}

func normalizeModelLookupID(modelID string) string {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	return strings.ReplaceAll(modelID, ".", "-")
}

// modelLookupMatches khớp chính xác hoặc khớp kèm hậu tố ngày.
// ví dụ "claude-sonnet-4" khớp "claude-sonnet-4-20250514".
func modelLookupMatches(knownID, targetID string) bool {
	if knownID == targetID {
		return true
	}
	if strings.HasPrefix(targetID, knownID) && isDatedModelSuffix(targetID[len(knownID):]) {
		return true
	}
	if strings.HasPrefix(knownID, targetID) && isDatedModelSuffix(knownID[len(targetID):]) {
		return true
	}
	return false
}

// isDatedModelSuffix kiểm tra chuỗi có dạng "-20250514" (gạch ngang + 8 chữ số) hay không.
func isDatedModelSuffix(s string) bool {
	if len(s) != 9 || s[0] != '-' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func hasDatedSuffix(id string) bool {
	if len(id) < 9 {
		return false
	}
	return isDatedModelSuffix(id[len(id)-9:])
}

// substringResolveBetter so sánh hai ứng viên khớp chuỗi con khi Resolve nhận nhiều bản ghi cùng
// khớp một bí danh ngắn. Thứ tự ưu tiên nhằm chọn "bản chuẩn mới nhất" thay vì bản đầu tiên theo
// thứ tự catalog tùy ý:
//  1. ID bắt đầu bằng pattern (prefix) hơn là chỉ chứa;
//  2. phần đuôi sau pattern thuần số (phiên bản/ngày: "-4.5", "-2024-11-20") hơn là đuôi chứa chữ
//     ("-mini", "-search-preview", "-fast" — đó là model KHÁC, không phải phiên bản mới hơn);
//  3. đuôi thuần số lớn hơn thắng (mới hơn: 4.8 > 4.5 > 4; 2024-11-20 > 2024-08-06);
//  4. giữ quy tắc cũ: ưu tiên ID không kèm hậu tố ngày YYYYMMDD.
func substringResolveBetter(a, b ModelEntry, normalizedPattern string) bool {
	ka, kb := substringMatchKind(a, normalizedPattern), substringMatchKind(b, normalizedPattern)
	if ka != kb {
		return ka < kb
	}
	va, ra := versionedRemainder(a, normalizedPattern)
	vb, rb := versionedRemainder(b, normalizedPattern)
	if va != vb {
		return va
	}
	if va {
		if naturalVersionGreater(ra, rb) {
			return true
		}
		if naturalVersionGreater(rb, ra) {
			return false
		}
	}
	return !hasDatedSuffix(a.ID) && hasDatedSuffix(b.ID)
}

// substringMatchKind phân tầng độ khớp: 0 = ID bắt đầu bằng pattern, 1 = ID chứa, 2 = chỉ Name chứa.
func substringMatchKind(e ModelEntry, normalizedPattern string) int {
	id := normalizeModelLookupID(e.ID)
	switch {
	case strings.HasPrefix(id, normalizedPattern):
		return 0
	case strings.Contains(id, normalizedPattern):
		return 1
	default:
		return 2
	}
}

// versionedRemainder trả về (thuần số?, phần đuôi) của ID sau vị trí khớp pattern —
// khớp ở giữa ID ("sonnet" trong claude-sonnet-4.5) cũng tính được đuôi "-4.5"; vị trí
// khớp phải đứng sau ranh giới phân tách (đầu ID hoặc sau '.'/'-') để không bám vào giữa từ.
// Đuôi thuần số (chỉ chứa 0-9, '.', '-') được coi là "phiên bản cùng dòng model"; đuôi chứa chữ là model khác.
func versionedRemainder(e ModelEntry, normalizedPattern string) (bool, string) {
	id := normalizeModelLookupID(e.ID)
	idx := strings.Index(id, normalizedPattern)
	if idx < 0 {
		return false, ""
	}
	if idx > 0 && id[idx-1] != '-' && id[idx-1] != '.' {
		return false, ""
	}
	rem := id[idx+len(normalizedPattern):]
	for _, c := range rem {
		if (c < '0' || c > '9') && c != '.' && c != '-' {
			return false, rem
		}
	}
	return true, rem
}

// naturalVersionGreater so sánh tự nhiên hai đuôi phiên bản thuần số: tách thành các đoạn số,
// so từng cặp theo giá trị; bằng hết thì nhiều đoạn hơn (dài hơn) thắng.
func naturalVersionGreater(a, b string) bool {
	as, bs := digitRuns(a), digitRuns(b)
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		if as[i] != bs[i] {
			return as[i] > bs[i]
		}
	}
	return len(as) > len(bs)
}

func digitRuns(s string) []uint64 {
	var out []uint64
	var cur uint64
	inRun := false
	for _, c := range s {
		if c >= '0' && c <= '9' {
			cur = cur*10 + uint64(c-'0')
			inRun = true
			continue
		}
		if inRun {
			out = append(out, cur)
			cur, inRun = 0, false
		}
	}
	if inRun {
		out = append(out, cur)
	}
	return out
}
