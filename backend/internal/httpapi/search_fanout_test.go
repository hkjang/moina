package httpapi

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func drainFanOut(t *testing.T) func() {
	t.Helper()
	// Take every slot so the next call has to fall back to sequential.
	held := 0
	for {
		select {
		case searchFanOut <- struct{}{}:
			held++
		default:
			return func() {
				for index := 0; index < held; index++ {
					<-searchFanOut
				}
			}
		}
	}
}

func TestClaimFanOutNeverBlocksWhenExhausted(t *testing.T) {
	release := drainFanOut(t)
	defer release()

	done := make(chan int, 1)
	go func() {
		claimed, releaseClaim := claimFanOut(3)
		releaseClaim()
		done <- claimed
	}()
	select {
	case claimed := <-done:
		if claimed != 0 {
			t.Fatalf("slot이 없는데 %d개를 확보했습니다", claimed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("claimFanOut이 대기했습니다. slot 고갈 시 즉시 반환해야 합니다")
	}
}

func TestClaimFanOutReturnsEveryClaimedSlot(t *testing.T) {
	claimed, release := claimFanOut(3)
	if claimed != 3 {
		t.Fatalf("여유가 있는데 %d개만 확보했습니다", claimed)
	}
	release()
	// All slots must be free again, which the next full drain proves.
	drain := drainFanOut(t)
	if len(searchFanOut) != searchFanOutSlots {
		t.Fatalf("반납 후 확보 가능한 slot이 %d개입니다", len(searchFanOut))
	}
	drain()
}

func TestRunSearchesExecutesEveryLookupConcurrently(t *testing.T) {
	var running, peak int64
	var mu sync.Mutex
	lookups := make([]func(context.Context) error, 0, 4)
	for index := 0; index < 4; index++ {
		lookups = append(lookups, func(context.Context) error {
			current := atomic.AddInt64(&running, 1)
			mu.Lock()
			if current > peak {
				peak = current
			}
			mu.Unlock()
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt64(&running, -1)
			return nil
		})
	}
	if err := runSearches(t.Context(), lookups); err != nil {
		t.Fatal(err)
	}
	if peak < 2 {
		t.Fatalf("최대 동시 실행 %d개. 여유가 있으면 병렬로 실행해야 합니다", peak)
	}
}

func TestRunSearchesFallsBackToSequentialWithoutBudget(t *testing.T) {
	release := drainFanOut(t)
	defer release()

	var running, peak int64
	var mu sync.Mutex
	var order []int
	lookups := make([]func(context.Context) error, 0, 4)
	for index := 0; index < 4; index++ {
		lookups = append(lookups, func(context.Context) error {
			current := atomic.AddInt64(&running, 1)
			mu.Lock()
			if current > peak {
				peak = current
			}
			order = append(order, index)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt64(&running, -1)
			return nil
		})
	}
	if err := runSearches(t.Context(), lookups); err != nil {
		t.Fatal(err)
	}
	if peak != 1 {
		t.Fatalf("slot이 없는데 최대 %d개가 동시에 실행되었습니다", peak)
	}
	if len(order) != 4 {
		t.Fatalf("순차 실행에서 %d개만 실행되었습니다", len(order))
	}
}

func TestRunSearchesReturnsTheFirstError(t *testing.T) {
	wanted := errors.New("검색 실패")
	for _, name := range []string{"concurrent", "sequential"} {
		t.Run(name, func(t *testing.T) {
			if name == "sequential" {
				release := drainFanOut(t)
				defer release()
			}
			var ran atomic.Int64
			lookups := []func(context.Context) error{
				func(context.Context) error { ran.Add(1); return nil },
				func(context.Context) error { ran.Add(1); return wanted },
				func(context.Context) error { ran.Add(1); return nil },
			}
			if err := runSearches(t.Context(), lookups); !errors.Is(err, wanted) {
				t.Fatalf("err=%v, %v를 기대했습니다", err, wanted)
			}
		})
	}
}

func TestRunSearchesHandlesZeroAndOneLookup(t *testing.T) {
	if err := runSearches(t.Context(), nil); err != nil {
		t.Fatalf("빈 목록에서 오류: %v", err)
	}
	called := false
	if err := runSearches(t.Context(), []func(context.Context) error{
		func(context.Context) error { called = true; return nil },
	}); err != nil || !called {
		t.Fatalf("단일 lookup 실행 실패: err=%v called=%t", err, called)
	}
}
