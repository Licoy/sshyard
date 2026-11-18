package remote

import (
	"context"
	"testing"
	"time"
)

func TestHaltKeepsAlreadyReceivedExit(t *testing.T) {
	errC := make(chan error, 1)
	errC <- nil
	if code, err := (&Runner{}).halt(context.Canceled, nil, mark{}, nil, errC, nil); code != 0 || err != nil {
		t.Fatalf("received success discarded: %d %v", code, err)
	}
}

func TestCommandTimeoutClamp(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "default", want: DefaultCommandTimeout},
		{name: "below min", in: time.Millisecond, want: MinCommandTimeout},
		{name: "min", in: MinCommandTimeout, want: MinCommandTimeout},
		{name: "inside", in: time.Minute, want: time.Minute},
		{name: "above max", in: 31 * time.Minute, want: MaxCommandTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandTimeout(tc.in); got != tc.want {
				t.Fatalf("%s -> %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestRunNilClient(t *testing.T) {
	_, err := New(nil, Options{}).Run(context.Background(), "echo", "", nil, nil)
	if err == nil {
		t.Fatal("nil client")
	}
}

func TestPerCommandTimeoutPreservesTurnDeadline(t *testing.T) {
	r := New(nil, Options{TurnTimeout: 20 * time.Millisecond})
	r.BeginTurn()
	r.mu.Lock()
	end := r.turnEnd
	r.mu.Unlock()
	if got := r.deadline(t.Context(), MaxCommandTimeout); !got.Equal(end) {
		t.Fatalf("deadline %v want %v", got, end)
	}
	// Changing a call's timeout must not start a fresh turn.
	if got := r.deadline(t.Context(), MinCommandTimeout); !got.Equal(end) {
		t.Fatalf("deadline reset %v", got)
	}
}
