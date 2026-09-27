// Package llmretry là nhân retry tầng request dùng chung cho lời gọi model trực tiếp:
// chỉ retry lỗi được adapter đánh dấu rõ là retryable, tuân thủ Retry-After/exponential backoff,
// và gửi tiến độ vào chuỗi quan sát workbench hiện có qua ToolProgress.
// Lỗi kết thúc như account, auth, permission sẽ trả về ngay;
// lỗi retryable retry liên tục, vòng đời chỉ do context kiểm soát.
package llmretry

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/voocel/agentcore"
)

const maxRetryDelay = 60 * time.Second

// Generator là interface model tối thiểu cần cho request retry.
type Generator interface {
	Generate(context.Context, []agentcore.Message, []agentcore.ToolSpec, ...agentcore.CallOption) (*agentcore.LLMResponse, error)
}

// Event mô tả một lần request retry sắp xảy ra.
type Event struct {
	Attempt int
	Delay   time.Duration
	Err     error
}

// Config cấu hình thông tin quan sát được cho retry, không thay đổi ngữ nghĩa retry.
type Config struct {
	Agent   string
	OnRetry func(Event)
}

// Generate gọi model.Generate. Lỗi retryable sau backoff sẽ retry liên tục cho đến khi thành công hoặc
// context kết thúc; lỗi không retryable trả về ngay.
func Generate(ctx context.Context, model Generator, cfg Config, messages []agentcore.Message, opts ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	for retry := 1; ; retry++ {
		resp, err := model.Generate(ctx, messages, nil, opts...)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || !isRetryable(err) {
			return nil, err
		}

		delay := retryDelay(err, retry-1)
		event := Event{Attempt: retry, Delay: delay, Err: err}
		if cfg.OnRetry != nil {
			cfg.OnRetry(event)
		}
		meta, _ := json.Marshal(struct {
			DelayMS int64 `json:"retry_delay_ms"`
		}{DelayMS: delay.Milliseconds()})
		agentcore.ReportToolProgress(ctx, agentcore.ProgressPayload{
			Kind:    agentcore.ProgressRetry,
			Agent:   cfg.Agent,
			Attempt: retry,
			Message: err.Error(),
			Meta:    meta,
		})
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func isRetryable(err error) bool {
	var retryable agentcore.RetryableError
	return errors.As(err, &retryable) && retryable.Retryable()
}

func retryDelay(err error, attempt int) time.Duration {
	var hinter agentcore.RetryHinter
	if errors.As(err, &hinter) {
		if delay := hinter.RetryAfter(); delay > 0 {
			if delay > maxRetryDelay {
				return maxRetryDelay
			}
			return delay
		}
	}
	delay := time.Second
	for i := 0; i < attempt && delay < maxRetryDelay; i++ {
		delay *= 2
	}
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}
