package yu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPartitionSchedulerSerializesSameKey(t *testing.T) {
	ps := NewPartitionScheduler(4, func(Request) string { return "same" })

	var mu sync.Mutex
	active, peak := 0, 0
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ps.Dispatch(context.Background(), Request{}, func() (Response, error) {
				mu.Lock()
				active++
				if active > peak {
					peak = active
				}
				mu.Unlock()

				time.Sleep(2 * time.Millisecond)

				mu.Lock()
				active--
				mu.Unlock()
				return Text(200, "ok"), nil
			})
			if err != nil {
				t.Errorf("dispatch: %v", err)
			}
		}()
	}
	wg.Wait()

	if peak != 1 {
		t.Fatalf("same key ran concurrently: peak = %d", peak)
	}
}

func TestPartitionSchedulerRunsDistinctKeysConcurrently(t *testing.T) {
	const workers = 4
	ps := NewPartitionScheduler(workers, func(r Request) string { return r.Path })

	// Pick one key per lane so the test really exercises `workers` goroutines.
	keys := make([]string, 0, workers)
	used := make(map[int]bool, workers)
	for i := 0; len(keys) < workers; i++ {
		if i > 10000 {
			t.Fatal("could not find keys mapping to distinct lanes")
		}
		key := fmt.Sprintf("key-%d", i)
		lane := ps.laneFor(key)
		if !used[lane] {
			used[lane] = true
			keys = append(keys, key)
		}
	}

	started := make(chan struct{}, len(keys))
	release := make(chan struct{})
	var wg sync.WaitGroup

	for _, key := range keys {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := ps.Dispatch(context.Background(), Request{Path: key}, func() (Response, error) {
				started <- struct{}{}
				<-release
				return Text(200, "ok"), nil
			})
			if err != nil {
				t.Errorf("dispatch: %v", err)
			}
		}(key)
	}

	for range keys {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("distinct keys did not run concurrently")
		}
	}

	close(release)
	wg.Wait()
}

func TestPartitionSchedulerTimeoutWhileQueued(t *testing.T) {
	ps := NewPartitionScheduler(1, func(Request) string { return "k" })

	running := make(chan struct{})
	unblock := make(chan struct{})
	firstDone := make(chan struct{})

	go func() {
		defer close(firstDone)
		_, _ = ps.Dispatch(context.Background(), Request{}, func() (Response, error) {
			close(running)
			<-unblock
			return Text(200, "first"), nil
		})
	}()

	<-running

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	secondRan := false
	_, err := ps.Dispatch(ctx, Request{}, func() (Response, error) {
		secondRan = true
		return Text(200, "second"), nil
	})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrRouteTimeout) {
		t.Fatalf("err = %v, want ErrRouteTimeout", err)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("queued dispatch ignored the timeout: %v", elapsed)
	}
	if secondRan {
		t.Fatal("timed-out task must not start before the lane is free")
	}

	close(unblock)
	<-firstDone
}

func TestPartitionSchedulerLaneSurvivesPanic(t *testing.T) {
	ps := NewPartitionScheduler(1, func(Request) string { return "k" })

	_, err := ps.Dispatch(context.Background(), Request{}, func() (Response, error) {
		panic("boom")
	})
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("err = %v, want a recovered panic", err)
	}

	resp, err := ps.Dispatch(context.Background(), Request{}, func() (Response, error) {
		return Text(200, "ok"), nil
	})
	if err != nil {
		t.Fatalf("lane did not survive panic: %v", err)
	}
	if resp.Status != 200 || resp.String() != "ok" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestPartitionSchedulerDefaultsWorkers(t *testing.T) {
	ps := NewPartitionScheduler(0, func(Request) string { return "k" })

	if len(ps.lanes) != 1 {
		t.Fatalf("lanes = %d, want 1", len(ps.lanes))
	}

	resp, err := ps.Dispatch(context.Background(), Request{}, func() (Response, error) {
		return Text(200, "ok"), nil
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp.String() != "ok" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestPartitionSchedulerReturnsTaskError(t *testing.T) {
	ps := NewPartitionScheduler(1, func(Request) string { return "k" })
	want := errors.New("nope")

	_, err := ps.Dispatch(context.Background(), Request{}, func() (Response, error) {
		return Response{}, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}
