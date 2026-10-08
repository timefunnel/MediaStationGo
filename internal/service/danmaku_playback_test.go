package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type playbackDanmakuBackend struct {
	fakeDanmakuPipeline
	entered chan DanmakuFetchRequest
	release chan struct{}
	matches atomic.Int32
	fetches atomic.Int32
	failure error
}

func newPlaybackDanmakuBackend() *playbackDanmakuBackend {
	return &playbackDanmakuBackend{entered: make(chan DanmakuFetchRequest, 128), release: make(chan struct{})}
}

func (b *playbackDanmakuBackend) MatchDanmaku(context.Context, string) (DanmakuMatchResult, error) {
	b.matches.Add(1)
	return matchedResult(), nil
}

func (b *playbackDanmakuBackend) FetchDanmaku(ctx context.Context, request DanmakuFetchRequest) (DanmakuPayload, error) {
	b.fetches.Add(1)
	b.entered <- request
	select {
	case <-ctx.Done():
		return DanmakuPayload{}, ctx.Err()
	case <-b.release:
	}
	if b.failure != nil {
		return DanmakuPayload{}, b.failure
	}
	return DanmakuPayload{Source: request.Source, EpisodeID: request.EpisodeID,
		OffsetSeconds: request.OffsetSeconds, Count: 1,
		Comments: []DanmakuComment{{CID: "1", M: "prepared", Time: 1}}}, nil
}

func awaitDanmakuFetch(t *testing.T, b *playbackDanmakuBackend) DanmakuFetchRequest {
	t.Helper()
	select {
	case request := <-b.entered:
		return request
	case <-time.After(3 * time.Second):
		t.Fatal("danmaku preparation did not start")
		return DanmakuFetchRequest{}
	}
}

func TestDanmakuPlaybackPreparesCurrentEpisodeAndSharesHandoff(t *testing.T) {
	svc, _ := newDanmakuTestService(t)
	b := newPlaybackDanmakuBackend()
	svc.SetBackendClient(b)
	svc.PrepareForPlayback("media-1")
	if request := awaitDanmakuFetch(t, b); request.MediaID != "media-1" || !request.WithRelated {
		t.Fatalf("wrong preparation request: %+v", request)
	}
	for range 10 {
		svc.PrepareForPlayback("media-1")
	}
	result := make(chan error, 1)
	go func() {
		payload, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{WithRelated: true})
		if err == nil && payload.Count != 1 {
			err = fmt.Errorf("unexpected payload: %+v", payload)
		}
		result <- err
	}()
	select {
	case <-result:
		t.Fatal("returned incomplete danmaku before preparation finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(b.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	first, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{WithRelated: true})
	if err != nil {
		t.Fatal(err)
	}
	first.Comments[0].M = "caller mutation"
	second, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{WithRelated: true})
	if err != nil || second.Comments[0].M != "prepared" {
		t.Fatalf("handoff payload was shared mutably: %+v %v", second, err)
	}
	if b.matches.Load() != 1 || b.fetches.Load() != 1 {
		t.Fatalf("duplicated upstream work: match=%d fetch=%d", b.matches.Load(), b.fetches.Load())
	}
}

func TestDanmakuPlaybackCanceledWaiterDoesNotCancelPreparation(t *testing.T) {
	svc, _ := newDanmakuTestService(t)
	b := newPlaybackDanmakuBackend()
	svc.SetBackendClient(b)
	svc.PrepareForPlayback("media-1")
	awaitDanmakuFetch(t, b)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := svc.Payload(ctx, "media-1", DanmakuOptions{WithRelated: true}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled waiter error: %v", err)
	}
	close(b.release)
	if _, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{WithRelated: true}); err != nil {
		t.Fatal(err)
	}
	if b.fetches.Load() != 1 {
		t.Fatal("waiter cancellation restarted fetching")
	}
}

func TestDanmakuPlaybackFailureIsSharedAndExpires(t *testing.T) {
	svc, _ := newDanmakuTestService(t)
	b := newPlaybackDanmakuBackend()
	b.failure = errors.New("provider rejected request")
	close(b.release)
	svc.SetBackendClient(b)
	svc.PrepareForPlayback("media-1")
	for range 3 {
		if _, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{WithRelated: true}); !errors.Is(err, ErrDanmakuUnavailable) {
			t.Fatalf("failure was hidden: %v", err)
		}
		svc.PrepareForPlayback("media-1")
	}
	if b.fetches.Load() != 1 {
		t.Fatal("failed preparation was retried before cooldown")
	}
	svc.preparation.mu.Lock()
	for _, task := range svc.preparation.tasks {
		task.expiresAt = time.Now().Add(-time.Second)
	}
	svc.preparation.mu.Unlock()
	if _, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{WithRelated: true}); !errors.Is(err, ErrDanmakuUnavailable) {
		t.Fatal(err)
	}
	if b.fetches.Load() != 2 {
		t.Fatal("expired failed preparation could not retry")
	}
}

func TestDanmakuPlaybackHandoffExpiryReturnsToBackendCacheChain(t *testing.T) {
	svc, _ := newDanmakuTestService(t)
	b := newPlaybackDanmakuBackend()
	close(b.release)
	svc.SetBackendClient(b)
	svc.PrepareForPlayback("media-1")
	if _, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{WithRelated: true}); err != nil {
		t.Fatal(err)
	}
	svc.preparation.mu.Lock()
	for _, task := range svc.preparation.tasks {
		task.expiresAt = time.Now().Add(-time.Second)
	}
	svc.preparation.mu.Unlock()
	if _, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{WithRelated: true}); err != nil {
		t.Fatal(err)
	}
	if b.fetches.Load() != 2 || b.matches.Load() != 1 {
		t.Fatal("expired handoff bypassed backend cache chain or rematched existing association")
	}
}

func TestDanmakuPlaybackDifferentOptionsDoNotReusePreparedPayload(t *testing.T) {
	svc, _ := newDanmakuTestService(t)
	b := newPlaybackDanmakuBackend()
	close(b.release)
	svc.SetBackendClient(b)
	svc.PrepareForPlayback("media-1")
	if _, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{WithRelated: true}); err != nil {
		t.Fatal(err)
	}
	awaitDanmakuFetch(t, b)
	for _, options := range []DanmakuOptions{{WithRelated: false}, {WithRelated: true, OffsetSeconds: 2}} {
		if _, err := svc.Payload(t.Context(), "media-1", options); err != nil {
			t.Fatal(err)
		}
		request := awaitDanmakuFetch(t, b)
		if request.WithRelated != options.WithRelated || request.OffsetSeconds != options.OffsetSeconds {
			t.Fatalf("different parameters reused prepared payload: %+v", request)
		}
	}
}

func TestDanmakuPlaybackTaskTableIsBounded(t *testing.T) {
	svc, _ := newDanmakuTestService(t)
	b := newPlaybackDanmakuBackend()
	svc.SetBackendClient(b)
	svc.preparation.mu.Lock()
	for index := range danmakuPreparationTaskLimit {
		key := danmakuPreparationKey{mediaID: fmt.Sprintf("retained-%d", index)}
		svc.preparation.tasks[key] = &danmakuPreparationTask{expiresAt: time.Now().Add(time.Minute)}
	}
	svc.preparation.mu.Unlock()
	if _, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{}); !errors.Is(err, ErrDanmakuUnavailable) {
		t.Fatalf("full task table did not report capacity: %v", err)
	}
	if b.fetches.Load() != 0 || b.matches.Load() != 0 {
		t.Fatal("capacity rejection still started upstream work")
	}
}

func TestDanmakuPlaybackBackgroundHasBoundedConcurrencyAndNoQueue(t *testing.T) {
	svc, _ := newDanmakuTestService(t)
	sqlDB, _ := svc.repos.DB.DB()
	sqlDB.SetMaxOpenConns(1)
	b := newPlaybackDanmakuBackend()
	svc.SetBackendClient(b)
	for index := range danmakuPreparationConcurrency {
		svc.PrepareForPlayback(fmt.Sprintf("media-%d", index))
		awaitDanmakuFetch(t, b)
	}
	svc.PrepareForPlayback("excess-media")
	svc.preparation.mu.Lock()
	count := len(svc.preparation.tasks)
	svc.preparation.mu.Unlock()
	if count != danmakuPreparationConcurrency || b.fetches.Load() != danmakuPreparationConcurrency {
		t.Fatalf("background work was queued or exceeded limit: tasks=%d calls=%d", count, b.fetches.Load())
	}
}

func TestDanmakuPlaybackChangesInvalidatePreparedPayload(t *testing.T) {
	for _, mutation := range []string{"manual", "offset", "clear", "import"} {
		t.Run(mutation, func(t *testing.T) {
			svc, _ := newDanmakuTestService(t)
			b := newPlaybackDanmakuBackend()
			close(b.release)
			svc.SetBackendClient(b)
			svc.PrepareForPlayback("media-1")
			if _, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{WithRelated: true}); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mutation {
			case "manual":
				_, err = svc.SetManual(t.Context(), "media-1", "youku", "123", "title", "episode", 0)
			case "offset":
				_, err = svc.SetOffset(t.Context(), "media-1", 2)
			case "clear":
				err = svc.Clear(t.Context(), "media-1")
			case "import":
				_, _, err = svc.ImportLocal(t.Context(), "media-1", "synthetic", "json", "title")
			}
			if err != nil {
				t.Fatal(err)
			}
			payload, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{WithRelated: true})
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "manual":
				if payload.EpisodeID != "123" || payload.Source != "youku" {
					t.Fatalf("old source reused: %+v", payload)
				}
			case "offset":
				if payload.OffsetSeconds != 2 {
					t.Fatalf("old offset reused: %+v", payload)
				}
			case "clear":
				if b.matches.Load() != 2 {
					t.Fatal("cleared association was reused")
				}
			case "import":
				if payload.Source != DanmakuProviderLocal || b.fetches.Load() != 1 {
					t.Fatal("import did not replace prepared source")
				}
			}
		})
	}
}

func TestDanmakuPlaybackInvalidationCancelsInflightResult(t *testing.T) {
	svc, _ := newDanmakuTestService(t)
	b := newPlaybackDanmakuBackend()
	svc.SetBackendClient(b)
	svc.PrepareForPlayback("media-1")
	awaitDanmakuFetch(t, b)
	svc.preparation.mu.Lock()
	task := svc.preparation.tasks[danmakuPreparationKey{mediaID: "media-1", options: DanmakuOptions{WithRelated: true}}]
	svc.preparation.mu.Unlock()
	if _, err := svc.SetManual(t.Context(), "media-1", "youku", "123", "title", "episode", 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-task.done:
		if !errors.Is(task.err, ErrDanmakuUnavailable) || task.payload.Count != 0 {
			t.Fatal("invalidated preparation returned stale success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inflight task was not canceled")
	}
}

func TestDanmakuPlaybackTimeoutAndServiceCloseStopWork(t *testing.T) {
	for _, mode := range []string{"timeout", "close"} {
		t.Run(mode, func(t *testing.T) {
			svc, _ := newDanmakuTestService(t)
			if mode == "timeout" {
				svc.preparation.timeout = 50 * time.Millisecond
			}
			b := newPlaybackDanmakuBackend()
			svc.SetBackendClient(b)
			svc.PrepareForPlayback("media-1")
			awaitDanmakuFetch(t, b)
			if mode == "close" {
				svc.Close()
			}
			if _, err := svc.Payload(t.Context(), "media-1", DanmakuOptions{WithRelated: true}); !errors.Is(err, ErrDanmakuUnavailable) {
				t.Fatalf("timeout/close did not remain unavailable: %v", err)
			}
		})
	}
}
