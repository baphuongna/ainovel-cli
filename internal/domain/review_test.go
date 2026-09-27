package domain

import (
	"reflect"
	"testing"
)

func TestRestoreOwnPlants(t *testing.T) {
	cases := []struct {
		name string
		prev []ForeshadowUpdate
		next []ForeshadowUpdate
		want []ForeshadowUpdate
	}{
		{
			name: "viết lại đổi plant của chương thành advance: plant được đưa về đầu hàng đợi",
			prev: []ForeshadowUpdate{{ID: "f_photo", Action: "plant", Description: "泄洪道旧照"}},
			next: []ForeshadowUpdate{{ID: "f_photo", Action: "advance"}},
			want: []ForeshadowUpdate{
				{ID: "f_photo", Action: "plant", Description: "泄洪道旧照"},
				{ID: "f_photo", Action: "advance"},
			},
		},
		{
			name: "viết lại làm mất cả plant của chương: vẫn đưa về đầu",
			prev: []ForeshadowUpdate{{ID: "f_photo", Action: "plant", Description: "泄洪道旧照"}},
			next: nil,
			want: []ForeshadowUpdate{{ID: "f_photo", Action: "plant", Description: "泄洪道旧照"}},
		},
		{
			name: "bản mới đã tự khai báo plant: không bổ sung lặp",
			prev: []ForeshadowUpdate{{ID: "f_photo", Action: "plant", Description: "旧描述"}},
			next: []ForeshadowUpdate{{ID: "f_photo", Action: "plant", Description: "新描述"}},
			want: []ForeshadowUpdate{{ID: "f_photo", Action: "plant", Description: "新描述"}},
		},
		{
			name: "bản cũ chỉ có advance/resolve: không có plant để bổ sung",
			prev: []ForeshadowUpdate{{ID: "f_photo", Action: "advance"}, {ID: "f_key", Action: "resolve"}},
			next: []ForeshadowUpdate{{ID: "f_photo", Action: "resolve"}},
			want: []ForeshadowUpdate{{ID: "f_photo", Action: "resolve"}},
		},
		{
			name: "nhiều plant được bổ về theo thứ tự gốc, và đều xếp trước advance",
			prev: []ForeshadowUpdate{
				{ID: "f_a", Action: "plant", Description: "甲"},
				{ID: "f_b", Action: "plant", Description: "乙"},
			},
			next: []ForeshadowUpdate{{ID: "f_a", Action: "advance"}},
			want: []ForeshadowUpdate{
				{ID: "f_a", Action: "plant", Description: "甲"},
				{ID: "f_b", Action: "plant", Description: "乙"},
				{ID: "f_a", Action: "advance"},
			},
		},
		{
			name: "nộp lần đầu không có bản cũ: trả về nguyên dạng",
			prev: nil,
			next: []ForeshadowUpdate{{ID: "f_photo", Action: "plant", Description: "泄洪道旧照"}},
			want: []ForeshadowUpdate{{ID: "f_photo", Action: "plant", Description: "泄洪道旧照"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RestoreOwnPlants(tc.prev, tc.next)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("RestoreOwnPlants() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
