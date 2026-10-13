// Package usage 只记录白名单用量元数据，独立于配置、任务正文与协议转换。
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	configstore "github.com/U109/api-subagents/internal/config"
	"github.com/U109/api-subagents/internal/shared"
)

// Tokens 用指针区分真实零与未上报；输入、输出采用包含缓存与推理的统一口径。
type Tokens struct {
	Input     *int64 `json:"input"`
	Output    *int64 `json:"output"`
	Total     *int64 `json:"total"`
	Read      *int64 `json:"read"`
	Write     *int64 `json:"write"`
	Reasoning *int64 `json:"reasoning"`
}

// Record 是单次实际上游尝试，不含地址、提示词、回答、工具参数、头或凭据。
type Record struct {
	ID           string `json:"id"`
	LogicalID    string `json:"logicalId"`
	Attempt      int64  `json:"attempt"`
	ConnectionID string `json:"connectionId"`
	Connection   string `json:"connection"`
	Model        string `json:"model"`
	Source       string `json:"source"`
	TaskID       string `json:"taskId,omitempty"`
	Operation    string `json:"operation"`
	StartedAt    int64  `json:"startedAt"`
	DurationMS   int64  `json:"durationMS"`
	FirstByteMS  int64  `json:"firstByteMS"`
	HTTPStatus   int    `json:"httpStatus"`
	Outcome      string `json:"outcome"`
	Quality      string `json:"quality"`
	Tokens
	CostMicroUSD *int64 `json:"costMicroUSD"`
	PriceVersion string `json:"priceVersion,omitempty"`
}

// Price 表示用户明确配置的 USD / 百万 Token 单价，不内置真实或示例模型价格。
type Price struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Read   float64 `json:"read"`
	Write  float64 `json:"write"`
}
type Prices struct {
	Revision string           `json:"revision"`
	Rates    map[string]Price `json:"rates"`
}
type Query struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// Store 按尝试 UUID 原子写入独立文件，多个 worker 无需争用同一个追加文件。
type Store struct {
	Root string
	mu   sync.Mutex
	lost atomic.Int64
}

// New 创建轻量账本句柄，不访问磁盘，不读取连接配置，也不调用模型。
func New(root string) *Store { return &Store{Root: root} }

// Key 使用 JSON 元组避免连接或模型包含斜线时发生模型计价键碰撞。
func Key(connection, model string) string {
	data, _ := json.Marshal([]string{connection, model})
	return string(data)
}

// Prices 返回独立费率文件；不存在表示未定价，损坏文件不默认为零价。
func (s *Store) Prices() (Prices, error) {
	result := Prices{Rates: map[string]Price{}}
	data, err := os.ReadFile(filepath.Join(s.Root, "prices.json"))
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, errors.New("无法读取用量费率配置")
	}
	if len(data) > 256*1024 || json.Unmarshal(data, &result) != nil || result.Rates == nil {
		return result, errors.New("用量费率配置损坏，请重新保存")
	}
	return result, nil
}

// SavePrices 校验完整费率并检查修订号；保存只影响之后开始的请求，不改写历史费用。
func (s *Store) SavePrices(prices Prices) (Prices, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(prices.Rates) > 1000 {
		return prices, errors.New("费率条目过多")
	}
	for key, price := range prices.Rates {
		var identity []string
		if json.Unmarshal([]byte(key), &identity) != nil || len(identity) != 2 || len(key) > 1024 {
			return prices, errors.New("费率模型标识无效")
		}
		for _, value := range []float64{price.Input, price.Output, price.Read, price.Write} {
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 10000 {
				return prices, errors.New("单价须为 0–10000 的有限数值")
			}
		}
	}
	current, err := s.Prices()
	if err != nil {
		return prices, err
	}
	if current.Revision != prices.Revision {
		return prices, errors.New("费率已被其他窗口修改，请刷新后重试")
	}
	prices.Revision = shared.UUID()
	if err := shared.AtomicWrite(filepath.Join(s.Root, "prices.json"), shared.Marshal(prices), 0600); err != nil {
		return prices, errors.New("无法保存用量费率配置")
	}
	return prices, nil
}

// Save 结算一个已结束的尝试；价格快照来自请求开始时，写入失败只计降级，不影响生成。
func (s *Store) Save(record Record, prices Prices) {
	if s == nil || s.Root == "" {
		return
	}
	if record.ID == "" || strings.ContainsAny(record.ID, "/\\") || record.StartedAt < 0 {
		s.lost.Add(1)
		return
	}
	record.CostMicroUSD = cost(record.Tokens, prices.Rates[Key(record.ConnectionID, record.Model)], prices.Rates, Key(record.ConnectionID, record.Model))
	if record.CostMicroUSD != nil {
		record.PriceVersion = prices.Revision
	}
	day := time.UnixMilli(record.StartedAt).UTC().Format("2006-01-02")
	if err := shared.AtomicWrite(filepath.Join(s.Root, "records", day, record.ID+".json"), shared.Marshal(record), 0600); err != nil {
		s.lost.Add(1)
	}
}

// List 按 UTC 时间范围读取白名单记录；不跟随链接，损坏记录标示降级，超大范围要求缩小筛选。
func (s *Store) List(query Query) ([]Record, int, error) {
	now := time.Now().UnixMilli()
	if query.To == 0 {
		query.To = now
	}
	if query.From == 0 {
		query.From = query.To - int64(30*24*time.Hour/time.Millisecond)
	}
	if query.From < 0 || query.To <= query.From || query.To-query.From > int64(31*24*time.Hour/time.Millisecond) || query.To > now+60000 {
		return nil, 0, errors.New("用量查询范围须在 31 天以内")
	}
	rows := []Record{}
	skipped := 0
	for day := time.UnixMilli(query.From).UTC().Truncate(24 * time.Hour); day.UnixMilli() < query.To; day = day.Add(24 * time.Hour) {
		directory := filepath.Join(s.Root, "records", day.Format("2006-01-02"))
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, skipped, errors.New("无法读取本地用量账本")
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, skipped, errors.New("无法读取本地用量账本")
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			info, err := entry.Info()
			if err != nil || info.Size() > 16*1024 {
				skipped++
				continue
			}
			data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
			var record Record
			if err != nil || json.Unmarshal(data, &record) != nil || record.ID == "" || record.LogicalID == "" {
				skipped++
				continue
			}
			if record.StartedAt >= query.From && record.StartedAt < query.To {
				rows = append(rows, record)
			}
			if len(rows) > 10000 {
				return nil, skipped, errors.New("记录超过一万条，请缩小时间范围")
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].StartedAt == rows[j].StartedAt {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].StartedAt > rows[j].StartedAt
	})
	return rows, skipped, nil
}

// Handle 只提供查询与费率保存，不允许前端传入文件路径、SQL 或任意命令。
func (s *Store) Handle(route string, body []byte) (shared.Object, error) {
	if len(body) > 256*1024 {
		return nil, errors.New("用量请求过大")
	}
	if route == "/api/usage/prices" {
		var fields struct {
			Rates map[string]map[string]*float64 `json:"rates"`
		}
		if json.Unmarshal(body, &fields) != nil || fields.Rates == nil {
			return nil, errors.New("费率格式无效")
		}
		for _, price := range fields.Rates {
			for _, field := range []string{"input", "output", "read", "write"} {
				if price[field] == nil {
					return nil, errors.New("请完整填写四项单价，不能将缺失价格当成零")
				}
			}
		}
		var prices Prices
		if json.Unmarshal(body, &prices) != nil || prices.Rates == nil {
			return nil, errors.New("费率格式无效")
		}
		value, err := s.SavePrices(prices)
		return shared.Object{"prices": value}, err
	}
	if route != "/api/usage" {
		return nil, errors.New("未找到用量操作")
	}
	var query Query
	if len(body) > 0 && json.Unmarshal(body, &query) != nil {
		return nil, errors.New("用量查询格式无效")
	}
	rows, skipped, err := s.List(query)
	if err != nil {
		return nil, err
	}
	prices, err := s.Prices()
	if err != nil {
		return nil, err
	}
	return shared.Object{"records": rows, "prices": prices, "skipped": skipped, "lost": s.lost.Load(), "now": time.Now().UnixMilli()}, nil
}

// cost 用整数微美元结算，避免金额浮点累加；缺少必要缓存字段且费率不等价时不冒充完整估算。
func cost(tokens Tokens, price Price, rates map[string]Price, key string) *int64 {
	if _, ok := rates[key]; !ok || tokens.Input == nil || tokens.Output == nil {
		return nil
	}
	read, write := int64(0), int64(0)
	if tokens.Read != nil {
		read = *tokens.Read
	} else if price.Read != price.Input {
		return nil
	}
	if tokens.Write != nil {
		write = *tokens.Write
	} else if price.Write != price.Input {
		return nil
	}
	ordinary := *tokens.Input - read - write
	if ordinary < 0 {
		return nil
	}
	result := new(big.Int)
	for _, term := range []struct {
		count int64
		rate  float64
	}{{ordinary, price.Input}, {read, price.Read}, {write, price.Write}, {*tokens.Output, price.Output}} {
		if term.count < 0 || math.IsNaN(term.rate) || math.IsInf(term.rate, 0) || term.rate < 0 || term.rate > 10000 {
			return nil
		}
		product := new(big.Int).Mul(big.NewInt(term.count), big.NewInt(int64(math.Round(term.rate*1000000))))
		result.Add(result, product)
	}
	result.Quo(result, big.NewInt(1000000))
	if !result.IsInt64() {
		return nil
	}
	value := result.Int64()
	return &value
}

type contextKey int

const sourceKey contextKey = 1
const callKey contextKey = 2

type sourceInfo struct{ source, task, connection string }
type call struct {
	record   Record
	sequence atomic.Int64
	failed   atomic.Bool
}

// WithSource 由受信任的应用入口标记来源和任务；外部请求头不能伪造统计归属。
func WithSource(ctx context.Context, source, task, connection string) context.Context {
	return context.WithValue(ctx, sourceKey, sourceInfo{source, task, connection})
}

// EnsureCall 将连接稳定身份绑定到一轮生成；转换层复用原逻辑请求，不重复创建业务请求。
func EnsureCall(ctx context.Context, profile configstore.Profile, connection, fallback, operation string) context.Context {
	if ctx.Value(callKey) != nil {
		return ctx
	}
	info, _ := ctx.Value(sourceKey).(sourceInfo)
	if info.source == "" {
		info.source = fallback
	}
	if info.connection != "" {
		connection = info.connection
	}
	id := profile.ID
	if id == "" {
		id = connection
	}
	clean := func(text string) string {
		text = configstore.Redact(text, profile)
		if len(text) > 512 {
			text = text[:512]
		}
		return text
	}
	value := &call{record: Record{LogicalID: shared.UUID(), ConnectionID: clean(id), Connection: clean(connection), Model: clean(profile.Model), Source: info.source, TaskID: clean(info.task), Operation: operation}}
	return WithProtocol(context.WithValue(ctx, callKey, value), profile.Protocol)
}

// MarkFailure 在业务解析失败且关闭响应之前标记失败，不把 HTTP 200 自动当成成功生成。
func MarkFailure(ctx context.Context) {
	if value, ok := ctx.Value(callKey).(*call); ok {
		value.failed.Store(true)
	}
}
