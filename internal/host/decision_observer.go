package host

import "time"

// runObservedDecision bổ xung vòng đời quan sát được cho một lần phán định Arbiter trọn vẹn.
// Arbiter vẫn là hàm LLM không streaming; đây chỉ tái dùng cơ chế cập nhật tại chỗ bắt đầu/kết thúc
// của ID sự kiện hiện có, không nạp thêm trạng thái, cũng không trộn JSON có cấu trúc vào panel xuất
func runObservedDecision[T any](o *observer, label string, call func() (T, error)) (T, error) {
	if o == nil {
		return call()
	}
	started := time.Now()
	id := nextEventID()
	o.emitAndLog(Event{
		ID:       id,
		Time:     started,
		Category: "DECISION",
		Agent:    "arbiter",
		Summary:  label,
		Level:    "info",
	})

	result, err := call()
	finished := time.Now()
	ev := Event{
		ID:         id,
		Time:       started,
		FinishedAt: finished,
		Failed:     err != nil,
		Category:   "DECISION",
		Agent:      "arbiter",
		Summary:    label,
		Level:      "success",
		Duration:   finished.Sub(started),
	}
	if err != nil {
		ev.Level = "error"
		ev.Detail = err.Error()
		ev.Kind = errorKind(err, err.Error())
	}
	o.emitEv(ev)
	o.persistEvent(ev)
	return result, err
}
