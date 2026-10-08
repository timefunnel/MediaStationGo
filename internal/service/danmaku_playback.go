package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	danmakuPreparationConcurrency = 3
	danmakuPreparationTaskLimit   = 64
	danmakuPreparationHandoffTTL  = time.Minute
	danmakuPreparationFailureTTL  = 5 * time.Minute
)

type danmakuPreparationKey struct {
	mediaID string
	options DanmakuOptions
}

type danmakuPreparationTask struct {
	done      chan struct{}
	cancel    context.CancelFunc
	prepared  bool
	expiresAt time.Time
	payload   DanmakuPayload
	err       error
}

// 只保存正在进行的任务和短暂交接结果；持久弹幕缓存仍由独立后端负责。
type danmakuPreparation struct {
	mu      sync.Mutex
	tasks   map[danmakuPreparationKey]*danmakuPreparationTask
	slots   chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	timeout time.Duration
	wg      sync.WaitGroup
	closed  bool
}

func newDanmakuPreparation() danmakuPreparation {
	ctx, cancel := context.WithCancel(context.Background())
	return danmakuPreparation{
		tasks: make(map[danmakuPreparationKey]*danmakuPreparationTask),
		slots: make(chan struct{}, danmakuPreparationConcurrency),
		ctx:   ctx, cancel: cancel, timeout: 120 * time.Second,
	}
}

// PrepareForPlayback 只准备当前媒体，不等待完成、不排队后台任务，也不访问媒体文件。
func (s *DanmakuService) PrepareForPlayback(mediaID string) {
	if !s.Available() || strings.TrimSpace(mediaID) == "" {
		return
	}
	key := danmakuPreparationKey{mediaID: strings.TrimSpace(mediaID), options: DanmakuOptions{WithRelated: true}}
	_, err := s.preparationTask(key, true)
	if err != nil {
		s.log.Warn("danmaku playback preparation skipped", zap.String("media_id", key.mediaID), zap.Error(err))
	}
}

// Payload 与起播准备共享同键任务；客户端取消只停止自身等待，不取消其他客户端的任务。
func (s *DanmakuService) Payload(ctx context.Context, mediaID string, options DanmakuOptions) (DanmakuPayload, error) {
	mediaID = strings.TrimSpace(mediaID)
	if mediaID == "" {
		return DanmakuPayload{}, fmt.Errorf("%w: media id is required", ErrDanmakuInvalidInput)
	}
	if !s.Available() {
		return DanmakuPayload{}, ErrDanmakuUnavailable
	}
	if err := validateDanmakuOffset(options.OffsetSeconds); err != nil {
		return DanmakuPayload{}, err
	}
	if err := ctx.Err(); err != nil {
		return DanmakuPayload{}, err
	}
	task, err := s.preparationTask(danmakuPreparationKey{mediaID: mediaID, options: options}, false)
	if err != nil {
		return DanmakuPayload{}, err
	}
	select {
	case <-ctx.Done():
		return DanmakuPayload{}, ctx.Err()
	case <-task.done:
		payload := task.payload
		if payload.Comments != nil {
			payload.Comments = append([]DanmakuComment{}, payload.Comments...)
		}
		return payload, task.err
	}
}

func (s *DanmakuService) preparationTask(key danmakuPreparationKey, background bool) (*danmakuPreparationTask, error) {
	p := &s.preparation
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("%w: danmaku service is closed", ErrDanmakuUnavailable)
	}
	now := time.Now()
	for candidate, task := range p.tasks {
		if !task.expiresAt.IsZero() && !now.Before(task.expiresAt) {
			delete(p.tasks, candidate)
		}
	}
	if task := p.tasks[key]; task != nil {
		task.prepared = task.prepared || background
		return task, nil
	}
	if len(p.tasks) >= danmakuPreparationTaskLimit {
		return nil, fmt.Errorf("%w: danmaku preparation task limit reached", ErrDanmakuUnavailable)
	}
	if background {
		select {
		case p.slots <- struct{}{}:
		default:
			return nil, fmt.Errorf("%w: danmaku preparation workers are busy", ErrDanmakuUnavailable)
		}
	}
	ctx, cancel := context.WithTimeout(p.ctx, p.timeout)
	task := &danmakuPreparationTask{done: make(chan struct{}), cancel: cancel, prepared: background}
	p.tasks[key] = task
	p.wg.Add(1)
	go s.runPreparation(ctx, key, task, background)
	return task, nil
}

func (s *DanmakuService) runPreparation(ctx context.Context, key danmakuPreparationKey, task *danmakuPreparationTask, hasSlot bool) {
	p := &s.preparation
	defer p.wg.Done()
	defer task.cancel()
	started := time.Now()
	if hasSlot {
		s.log.Info("danmaku playback preparation started", zap.String("media_id", key.mediaID))
	}
	if !hasSlot {
		select {
		case p.slots <- struct{}{}:
			hasSlot = true
		case <-ctx.Done():
			task.err = ctx.Err()
		}
	}
	if hasSlot {
		task.payload, task.err = s.loadPayload(ctx, key.mediaID, key.options)
		<-p.slots
	}
	p.mu.Lock()
	if ctx.Err() != nil {
		task.payload = DanmakuPayload{}
		task.err = fmt.Errorf("%w: preparation stopped: %v", ErrDanmakuUnavailable, ctx.Err())
	} else if p.tasks[key] != task {
		task.payload = DanmakuPayload{}
		task.err = fmt.Errorf("%w: media association changed during preparation", ErrDanmakuUnavailable)
	}
	if p.tasks[key] == task {
		switch {
		case task.err != nil:
			task.expiresAt = time.Now().Add(danmakuPreparationFailureTTL)
		case task.prepared:
			task.expiresAt = time.Now().Add(danmakuPreparationHandoffTTL)
		default:
			delete(p.tasks, key)
		}
	}
	prepared := task.prepared
	close(task.done)
	p.mu.Unlock()
	if prepared {
		fields := []zap.Field{zap.String("media_id", key.mediaID), zap.Duration("duration", time.Since(started))}
		if task.err != nil {
			s.log.Warn("danmaku playback preparation failed", append(fields, zap.Error(task.err))...)
		} else {
			s.log.Info("danmaku playback preparation ready", append(fields, zap.String("source", task.payload.Source), zap.Int("count", task.payload.Count))...)
		}
	}
}

func (s *DanmakuService) invalidatePreparation(mediaID string) {
	p := &s.preparation
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, task := range p.tasks {
		if key.mediaID == mediaID {
			task.cancel()
			delete(p.tasks, key)
		}
	}
}

// Close 停止所有准备任务，避免服务关闭后继续回源或使用数据库。
func (s *DanmakuService) Close() {
	p := &s.preparation
	p.mu.Lock()
	p.closed = true
	p.cancel()
	p.mu.Unlock()
	p.wg.Wait()
}
