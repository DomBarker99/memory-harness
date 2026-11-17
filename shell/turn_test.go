// Tests for runTurn against small fake loops written in sh, so no Node, model
// or kagent is needed:  cd shell && go test ./...
package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeLoop is a loop program that ignores its input and runs the given script.
func fakeLoop(script string) []string {
	return []string{"sh", "-c", "cat >/dev/null; " + script}
}

func run(t *testing.T, ctx context.Context, script string) (string, []loopEvent, error) {
	t.Helper()
	var events []loopEvent
	final, err := runTurn(ctx, fakeLoop(script), turnInput{ContextID: "c1", TaskID: "t1", Text: "hi"},
		func(event loopEvent) { events = append(events, event) })
	return final, events, err
}

func TestEventsArriveInOrderAndFinalIsReturned(t *testing.T) {
	final, events, err := run(t, context.Background(), `
		echo '{"type":"text","delta":"Hel"}'
		echo '{"type":"text","delta":"lo"}'
		echo '{"type":"text_end","text":"Hello"}'
		echo '{"type":"tool","name":"bash","phase":"start"}'
		echo 'not json, a stray log line'
		echo '{"type":"final","text":"Hello"}'`)
	if err != nil {
		t.Fatal(err)
	}
	if final != "Hello" {
		t.Fatalf("final = %q", final)
	}
	var kinds []string
	for _, event := range events {
		kinds = append(kinds, event.Type)
	}
	if got := strings.Join(kinds, ","); got != "text,text,text_end,tool" {
		t.Fatalf("events = %s", got)
	}
}

func TestLoopErrorEventIsTheReason(t *testing.T) {
	_, _, err := run(t, context.Background(), `echo '{"type":"error","message":"the model said no"}'; exit 1`)
	if err == nil || err.Error() != "the model said no" {
		t.Fatalf("err = %v", err)
	}
}

func TestBadExitWithoutAReasonFails(t *testing.T) {
	_, _, err := run(t, context.Background(), `exit 3`)
	if err == nil || !strings.Contains(err.Error(), "loop failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestCleanExitWithoutFinalFails(t *testing.T) {
	_, _, err := run(t, context.Background(), `echo '{"type":"text","delta":"x"}'`)
	if err == nil || !strings.Contains(err.Error(), "without a final") {
		t.Fatalf("err = %v", err)
	}
}

// A loop that hangs, with a child of its own, must be gone soon after the deadline.
func TestHungLoopIsKilledAtTheDeadline(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer stop()
	started := time.Now()
	_, _, err := run(t, ctx, `sleep 30 & sleep 30`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("took %s to stop", elapsed)
	}
}
