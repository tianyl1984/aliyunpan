package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// 事件类型
const (
	EventJobAdded       = "job.added"
	EventJobState       = "job.state"
	EventJobProgress    = "job.progress"
	EventTaskState      = "task.state"
	EventTaskProgress   = "task.progress"
	EventLog            = "log"
	EventConsoleOutput  = "console.output"
	EventConsoleExit    = "console.exit"
	EventAccountChanged = "account.changed"
	EventPing           = "ping"
)

// Event SSE 事件
type Event struct {
	Id   uint64      `json:"id"`
	Type string      `json:"type"`
	Ts   int64       `json:"ts"`
	Data interface{} `json:"data,omitempty"`
}

// subscriberBuffer 单个订阅者的缓冲区大小。
// 满了就丢弃最旧的事件，绝不阻塞发布方——发布方可能是下载协程。
const subscriberBuffer = 256

type subscriber struct {
	ch chan Event
	mu sync.Mutex
}

// EventBroker 事件广播器
type EventBroker struct {
	mu     sync.RWMutex
	subs   map[*subscriber]struct{}
	nextId atomic.Uint64
	nowFn  func() time.Time
}

func NewEventBroker() *EventBroker {
	return &EventBroker{
		subs:  make(map[*subscriber]struct{}),
		nowFn: time.Now,
	}
}

func (b *EventBroker) Subscribe() (<-chan Event, func()) {
	s := &subscriber{ch: make(chan Event, subscriberBuffer)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	return s.ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[s]; ok {
			delete(b.subs, s)
			close(s.ch)
		}
		b.mu.Unlock()
	}
}

// Publish 向所有订阅者广播。永不阻塞：缓冲区满时丢弃该订阅者最旧的一条。
func (b *EventBroker) Publish(ev Event) {
	ev.Id = b.nextId.Add(1)
	if ev.Ts == 0 {
		ev.Ts = b.nowFn().UnixMilli()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.ch <- ev:
		default:
			// 丢弃最旧的一条腾出空间，保证新事件（尤其是状态变更）能进去
			select {
			case <-s.ch:
			default:
			}
			select {
			case s.ch <- ev:
			default:
			}
		}
	}
}

func (b *EventBroker) subscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// handleEvents SSE 端点。所有事件走这一条多路复用流，避开浏览器每域 6 连接的限制。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return internalError("当前服务端不支持流式响应")
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, cancel := s.events.Subscribe()
	defer cancel()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Id, ev.Type, payload); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}
