package usage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/U109/api-subagents/internal/shared"
)

// Transport 旁观有受信调用上下文的请求；模型目录、更新下载等未标记请求完全不记录。
type Transport struct {
	Base  http.RoundTripper
	Store *Store
}

// WrapClient 复用原连接池、取消和重定向策略，不改变原客户端；重复注入不会叠加记录层。
func WrapClient(client *http.Client, store *Store) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	result := *client
	base := client.Transport
	if previous, ok := base.(*Transport); ok {
		base = previous.Base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	result.Transport = &Transport{Base: base, Store: store}
	return &result
}

// RoundTrip 为每次真实尝试建立唯一记录，重试共用逻辑 ID；存储失败不改变 HTTP 结果。
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	value, ok := req.Context().Value(callKey).(*call)
	if !ok || t.Store == nil {
		return t.Base.RoundTrip(req)
	}
	start := time.Now()
	record := value.record
	record.ID = shared.UUID()
	record.Attempt = value.sequence.Add(1)
	record.StartedAt = start.UnixMilli()
	record.FirstByteMS = -1
	prices, pricesError := t.Store.Prices()
	if prices.Rates == nil || pricesError != nil {
		prices.Rates = map[string]Price{}
	}
	key := Key(record.ConnectionID, record.Model)
	if _, exists := prices.Rates[key]; !exists && pricesError == nil {
		if price, known := OfficialPrice(record.Model); known {
			prices.Rates[key] = price
		}
	}
	response, err := t.Base.RoundTrip(req)
	if err != nil {
		record.DurationMS = time.Since(start).Milliseconds()
		record.Outcome = "failed"
		if errors.Is(req.Context().Err(), context.Canceled) {
			record.Outcome = "cancelled"
		}
		record.Quality = "unreported"
		t.Store.Save(record, prices)
		return response, err
	}
	record.HTTPStatus = response.StatusCode
	valueProtocol, _ := req.Context().Value(protocolKey).(string)
	observer := observer{protocol: valueProtocol, streaming: strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream")}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		observer.overflow = true
		observer.streaming = false
	}
	response.Body = &observedBody{ReadCloser: response.Body, ctx: req.Context(), call: value, store: t.Store, record: record, prices: prices, start: start, observer: observer}
	return response, nil
}

const protocolKey contextKey = 3

// WithProtocol 为观测器标记原始上游格式，不从返回内容猜测协议。
func WithProtocol(ctx context.Context, protocol string) context.Context {
	return context.WithValue(ctx, protocolKey, protocol)
}

type observedBody struct {
	io.ReadCloser
	ctx       context.Context
	call      *call
	store     *Store
	record    Record
	prices    Prices
	start     time.Time
	observer  observer
	once      sync.Once
	readError error
}

// Read 将原字节和错误直接交给原调用者；只记录有限字段，不等待流结束后再交付首包。
func (b *observedBody) Read(buffer []byte) (int, error) {
	n, err := b.ReadCloser.Read(buffer)
	if n > 0 {
		if b.record.FirstByteMS < 0 {
			b.record.FirstByteMS = time.Since(b.start).Milliseconds()
		}
		b.observer.feed(buffer[:n])
	}
	if err != nil && err != io.EOF {
		b.readError = err
	}
	return n, err
}

// Close 幂等结算，不为补用量继续读取或重发请求；失败、取消和超限均保持真实未知字段。
func (b *observedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		b.observer.finish()
		b.record.DurationMS = time.Since(b.start).Milliseconds()
		b.record.Tokens = b.observer.tokens
		if b.observer.serviceTier != "" {
			b.record.ServiceTier = b.observer.serviceTier
			b.record.Fast = b.observer.serviceTier == "priority" || b.observer.serviceTier == "fast"
		}
		b.record.Outcome = b.observer.terminal
		if b.record.Outcome == "" {
			b.record.Outcome = "unknown"
		}
		if b.record.HTTPStatus < 200 || b.record.HTTPStatus >= 300 || b.readError != nil || b.call.failed.Load() {
			b.record.Outcome = "failed"
		}
		if errors.Is(b.ctx.Err(), context.Canceled) {
			b.record.Outcome = "cancelled"
		} else if b.ctx.Err() != nil {
			b.record.Outcome = "failed"
		}
		b.record.Quality = "unreported"
		if b.observer.overflow {
			b.record.Quality = "unreadable"
		} else if b.observer.reported {
			b.record.Quality = "partial"
			if b.record.Input != nil && b.record.Output != nil {
				b.record.Quality = "complete"
			}
		}
		b.store.Save(b.record, b.prices)
	})
	return err
}
