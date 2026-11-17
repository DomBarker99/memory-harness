// One turn of the loop program, seen from the shell.
//
// The shell starts the loop once per user message, writes the message to its
// stdin as one JSON line, and reads JSON lines back from its stdout until it
// exits. This file knows nothing about kagent or A2A; main.go turns what comes
// out of here into A2A events. That split is what lets turn_test.go run the
// same code against small fake loops.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// turnInput is the one line written to the loop's stdin.
type turnInput struct {
	ContextID string `json:"contextId"` // the kagent Session
	TaskID    string `json:"taskId"`    // this turn
	Text      string `json:"text"`      // the user's message
}

// loopEvent is one line read from the loop's stdout. The same shapes are
// declared as LoopEvent in loop/loop.ts; the two must match.
type loopEvent struct {
	Type    string `json:"type"`              // text | text_end | tool | final | error
	Delta   string `json:"delta,omitempty"`   // text: a piece of the model's reply
	Text    string `json:"text,omitempty"`    // text_end: one whole model message; final: the reply
	Name    string `json:"name,omitempty"`    // tool: which tool
	Phase   string `json:"phase,omitempty"`   // tool: start | end
	IsError bool   `json:"isError,omitempty"` // tool: whether it failed
	Message string `json:"message,omitempty"` // error: why the turn failed
}

// runTurn runs the loop program for one turn. Every event except the last is
// passed to onEvent as it arrives. It returns the text of the "final" event,
// or an error if the loop reported one, exited badly, or never sent a final.
//
// When ctx ends (a timeout, or the turn was cancelled) the loop and everything
// it started are killed.
func runTurn(ctx context.Context, command []string, input turnInput, onEvent func(loopEvent)) (string, error) {
	line, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	process := exec.CommandContext(ctx, command[0], command[1:]...)
	process.Stdin = bytes.NewReader(append(line, '\n'))
	process.Stderr = os.Stderr // the loop's logging goes straight to the container log
	// The loop runs shell commands for the model. Putting it in its own process
	// group lets a kill reach those too, not just the loop itself.
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	process.Cancel = func() error { return syscall.Kill(-process.Process.Pid, syscall.SIGKILL) }
	process.WaitDelay = 5 * time.Second
	stdout, err := process.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := process.Start(); err != nil {
		return "", fmt.Errorf("start loop: %w", err)
	}

	var final *string
	var reported string // the loop's own account of a failure, if it gave one
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for lines.Scan() {
		var event loopEvent
		if json.Unmarshal(lines.Bytes(), &event) != nil || event.Type == "" {
			// Not part of the protocol (a stray console.log, say): keep it as a log line.
			fmt.Fprintf(os.Stderr, "loop stdout: %s\n", lines.Bytes())
			continue
		}
		switch event.Type {
		case "final":
			final = &event.Text
		case "error":
			reported = event.Message
		default:
			onEvent(event)
		}
	}
	waitErr := errors.Join(lines.Err(), process.Wait())

	// Say what actually happened, most specific reason first.
	switch {
	case ctx.Err() != nil:
		return "", ctx.Err()
	case reported != "":
		return "", errors.New(reported)
	case waitErr != nil:
		return "", fmt.Errorf("loop failed: %w", waitErr)
	case final == nil:
		return "", errors.New("loop exited without a final event")
	}
	return *final, nil
}
