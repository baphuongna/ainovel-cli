package domain

// StateChange bản ghi biến đổi trạng thái nhân vật/thực thể.
type StateChange struct {
	Chapter  int    `json:"chapter"`
	Entity   string `json:"entity"`              // tên nhân vật hoặc thực thể
	Field    string `json:"field"`               // thuộc tính biến đổi: realm/location/status/power/relation v.v.
	OldValue string `json:"old_value,omitempty"` // trước khi đổi (có thể rỗng khi xuất hiện lần đầu)
	NewValue string `json:"new_value"`           // sau khi đổi
	Reason   string `json:"reason,omitempty"`    // lý do biến đổi
}
