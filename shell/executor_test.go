// Tests for the executor: which A2A events a client sees for a turn. The loop
// is a fake written in sh, and no kagent is involved.
package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

func newExecutor(timeout time.Duration) *executor {
	return &executor{timeout: timeout, running: map[a2atype.TaskID]*runningTurn{}}
}

func request() *a2asrv.ExecutorContext {
	return &a2asrv.ExecutorContext{
		TaskID:    "task-1",
		ContextID: "session-1",
		Message:   a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart("hi")),
	}
}

// describe turns each event into one short line, so a whole turn reads as a list.
func describe(event a2atype.Event) string {
	switch event := event.(type) {
	case *a2atype.Task:
		return "task " + string(event.Status.State)
	case *a2atype.TaskStatusUpdateEvent:
		line := "status " + string(event.Status.State)
		if event.Status.Message != nil && len(event.Status.Message.Parts) > 0 {
			line += ": " + event.Status.Message.Parts[0].Text()
		}
		return line
	case *a2atype.TaskArtifactUpdateEvent:
		kind := "artifact"
		if event.Append {
			kind = "append"
		}
		if event.LastChunk {
			kind = "closing"
		}
		return fmt.Sprintf("%s %q", kind, event.Artifact.Parts[0].Text())
	}
	return fmt.Sprintf("%T", event)
}

func collect(t *testing.T, e *executor, script string) []string {
	t.Helper()
	old := loopCommand
	loopCommand = fakeLoop(script)
	defer func() { loopCommand = old }()
	var lines []string
	for event, err := range e.Execute(context.Background(), request()) {
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, describe(event))
	}
	return lines
}

func expect(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\n\nwanted:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestStreamedReply(t *testing.T) {
	got := collect(t, newExecutor(time.Minute), `
		echo '{"type":"text","delta":"Let me check. "}'
		echo '{"type":"text_end","text":"Let me check. "}'
		echo '{"type":"tool","name":"bash","phase":"start"}'
		echo '{"type":"tool","name":"bash","phase":"end","isError":true}'
		echo '{"type":"text","delta":"Do"}'
		echo '{"type":"text","delta":"ne."}'
		echo '{"type":"text_end","text":"Done."}'
		echo '{"type":"final","text":"Done."}'`)
	expect(t, got,
		"task TASK_STATE_SUBMITTED",
		"status TASK_STATE_WORKING",
		`artifact "Let me check. "`,
		`closing "Let me check. "`,
		"status TASK_STATE_WORKING: tool bash: start",
		"status TASK_STATE_WORKING: tool bash: end (error)",
		`artifact "Do"`,
		`append "ne."`,
		`closing "Done."`,
		"status TASK_STATE_COMPLETED",
	)
}

func TestReplyThatNeverStreamedStillArrives(t *testing.T) {
	got := collect(t, newExecutor(time.Minute), `echo '{"type":"final","text":"Only this."}'`)
	expect(t, got,
		"task TASK_STATE_SUBMITTED",
		"status TASK_STATE_WORKING",
		`artifact "Only this."`,
		"status TASK_STATE_COMPLETED",
	)
}

func TestFailedTurnCarriesTheReason(t *testing.T) {
	got := collect(t, newExecutor(time.Minute), `echo '{"type":"error","message":"401 from the model"}'; exit 1`)
	expect(t, got,
		"task TASK_STATE_SUBMITTED",
		"status TASK_STATE_WORKING",
		"status TASK_STATE_FAILED: 401 from the model",
	)
}

func TestHungTurnFailsAtTheTimeout(t *testing.T) {
	got := collect(t, newExecutor(300*time.Millisecond), `sleep 30`)
	expect(t, got,
		"task TASK_STATE_SUBMITTED",
		"status TASK_STATE_WORKING",
		"status TASK_STATE_FAILED: the turn was stopped after 300ms",
	)
}

// Cancel kills the running loop and reports the cancelled state; Execute then
// ends without calling the turn failed.
func TestCancelStopsTheTurn(t *testing.T) {
	old := loopCommand
	loopCommand = fakeLoop(`sleep 30`)
	defer func() { loopCommand = old }()
	e := newExecutor(time.Minute)

	done := make(chan []string)
	go func() {
		var lines []string
		for event := range e.Execute(context.Background(), request()) {
			lines = append(lines, describe(event))
		}
		done <- lines
	}()
	// Wait until the turn is registered as running.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		e.mu.Lock()
		running := len(e.running)
		e.mu.Unlock()
		if running == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the turn never started")
		}
	}

	var cancelLines []string
	for event := range e.Cancel(context.Background(), request()) {
		cancelLines = append(cancelLines, describe(event))
	}
	expect(t, cancelLines, "status TASK_STATE_CANCELED")

	select {
	case lines := <-done:
		expect(t, lines, "task TASK_STATE_SUBMITTED", "status TASK_STATE_WORKING")
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not end after Cancel")
	}
}
