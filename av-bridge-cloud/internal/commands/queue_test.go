package commands

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStatus_Terminal(t *testing.T) {
	cases := map[Status]bool{
		StatusPending:    false,
		StatusInProgress: false,
		StatusSucceeded:  true,
		StatusFailed:     true,
		StatusCancelled:  true,
	}
	for s, want := range cases {
		if got := s.Terminal(); got != want {
			t.Errorf("%q.Terminal() = %v, want %v", s, got, want)
		}
	}
}

// TestWaitForTerminal_ReturnsOnTerminal — getter goes pending → in_progress →
// succeeded; WaitForTerminal must exit when the terminal state appears.
func TestWaitForTerminal_ReturnsOnTerminal(t *testing.T) {
	calls := 0
	get := func(ctx context.Context) (Command, error) {
		calls++
		switch calls {
		case 1:
			return Command{ID: "x", Status: StatusPending}, nil
		case 2:
			return Command{ID: "x", Status: StatusInProgress}, nil
		default:
			return Command{ID: "x", Status: StatusSucceeded}, nil
		}
	}
	got, err := WaitForTerminal(context.Background(), get, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusSucceeded {
		t.Fatalf("got %s, want succeeded", got.Status)
	}
	if calls < 3 {
		t.Fatalf("getter called %d times, want >=3", calls)
	}
}

// TestWaitForTerminal_TimeoutReturnsLast — never terminal; return after deadline.
func TestWaitForTerminal_TimeoutReturnsLast(t *testing.T) {
	get := func(ctx context.Context) (Command, error) {
		return Command{ID: "x", Status: StatusInProgress}, nil
	}
	start := time.Now()
	got, err := WaitForTerminal(context.Background(), get, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusInProgress {
		t.Fatalf("got %s, want in_progress", got.Status)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("returned too early: %s", elapsed)
	}
}

// TestWaitForTerminal_ErrorPropagates — getter errors must surface.
func TestWaitForTerminal_ErrorPropagates(t *testing.T) {
	wantErr := errors.New("db down")
	get := func(ctx context.Context) (Command, error) {
		return Command{}, wantErr
	}
	_, err := WaitForTerminal(context.Background(), get, time.Second)
	if !errors.Is(err, wantErr) {
		t.Fatalf("got %v, want %v", err, wantErr)
	}
}

// TestWaitForTerminal_ContextCancel — context cancel exits cleanly.
func TestWaitForTerminal_ContextCancel(t *testing.T) {
	get := func(ctx context.Context) (Command, error) {
		return Command{ID: "x", Status: StatusPending}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	_, err := WaitForTerminal(ctx, get, 5*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
