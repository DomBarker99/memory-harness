// The harness shell: the part of the image kagent talks to.
//
// kagent's Go helper (app.New) owns everything kagent-specific: the A2A gRPC
// server on port 80, /readyz on 8081, fetching this revision's config, and
// reporting task state back to the controller. This is the helper kagent's docs
// point a bring-your-own image at; the only thing an image supplies is the
// executor below, which decides what happens with one user message.
//
// Here that is: start the loop program (turn.go), and pass on what it reports
// as A2A events:
//
//	text, text_end  the model's reply, streamed as an artifact
//	tool            a "working" status line naming the tool
//	final           the turn is completed
//	a failure       the turn is failed, with the reason
//
// The shell is process 1 and stays up between turns; the loop is a new process
// each turn. If the loop hangs it is killed after TURN_TIMEOUT_SECONDS, because
// a turn that never ends leaves the sandbox running until someone deletes the
// Session.
//
// Based on kagent's own fixture, go/core/test/byoa2a/main.go at v1.0.0-rc3.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/kagent-dev/kagent/go/adk/pkg/app"
	"github.com/kagent-dev/kagent/go/adk/pkg/config"
	"github.com/kagent-dev/kagent/go/pkg/logging"
)

// loopCommand is started once per turn.
var loopCommand = []string{"node", "/app/loop/loop.ts"}

// executor runs turns. It remembers the ones in progress so Cancel can stop them.
type executor struct {
	timeout time.Duration

	mu      sync.Mutex
	running map[a2atype.TaskID]*runningTurn
}

type runningTurn struct {
	stop      context.CancelFunc
	cancelled bool // set by Cancel, so Execute knows not to report a failure
}

// Execute handles one user message.
func (e *executor) Execute(ctx context.Context, request *a2asrv.ExecutorContext) iter.Seq2[a2atype.Event, error] {
	return func(yield func(a2atype.Event, error) bool) {
		if !yield(a2atype.NewSubmittedTask(request, request.Message), nil) {
			return
		}
		if !yield(a2atype.NewStatusUpdateEvent(request, a2atype.TaskStateWorking, nil), nil) {
			return
		}

		turnCtx, stop := context.WithTimeout(ctx, e.timeout)
		defer stop()
		turn := &runningTurn{stop: stop}
		e.mu.Lock()
		e.running[request.TaskID] = turn
		e.mu.Unlock()
		defer func() {
			e.mu.Lock()
			delete(e.running, request.TaskID)
			e.mu.Unlock()
		}()

		// send passes one event to the client. Once the client has gone away
		// there is nobody to tell, so the turn is stopped.
		gone := false
		send := func(event a2atype.Event) {
			if !gone && !yield(event, nil) {
				gone = true
				stop()
			}
		}

		// The model's text streams the way kagent's own harness sends it: a
		// first chunk, appended pieces, then one closing chunk that repeats
		// the whole message. Each model message is its own artifact.
		var artifact a2atype.ArtifactID
		sentText := false
		final, err := runTurn(turnCtx, loopCommand, turnInput{
			ContextID: request.ContextID,
			TaskID:    string(request.TaskID),
			Text:      messageText(request.Message),
		}, func(event loopEvent) {
			switch event.Type {
			case "text":
				if artifact == "" {
					first := a2atype.NewArtifactEvent(request, a2atype.NewTextPart(event.Delta))
					artifact = first.Artifact.ID
					send(first)
				} else {
					send(a2atype.NewArtifactUpdateEvent(request, artifact, a2atype.NewTextPart(event.Delta)))
				}
				sentText = true
			case "text_end":
				closing := a2atype.NewArtifactEvent(request, a2atype.NewTextPart(event.Text))
				if artifact != "" {
					closing.Artifact.ID = artifact
				}
				closing.LastChunk = true
				send(closing)
				artifact = ""
				sentText = true
			case "tool":
				note := fmt.Sprintf("tool %s: %s", event.Name, event.Phase)
				if event.IsError {
					note += " (error)"
				}
				send(a2atype.NewStatusUpdateEvent(request, a2atype.TaskStateWorking,
					a2atype.NewMessageForTask(a2atype.MessageRoleAgent, request, a2atype.NewTextPart(note))))
			}
		})

		e.mu.Lock()
		cancelled := turn.cancelled
		e.mu.Unlock()
		switch {
		case gone || cancelled:
			// Cancel reports the cancelled state itself; a client that left hears nothing.
			return
		case err != nil:
			reason := err.Error()
			if errors.Is(err, context.DeadlineExceeded) {
				reason = fmt.Sprintf("the turn was stopped after %s", e.timeout)
			}
			message := a2atype.NewMessageForTask(a2atype.MessageRoleAgent, request, a2atype.NewTextPart(reason))
			yield(a2atype.NewStatusUpdateEvent(request, a2atype.TaskStateFailed, message), nil)
			return
		}
		// A reply that never streamed (an empty one, or a loop that only sent
		// "final") still has to reach the client as an artifact.
		if !sentText {
			if !yield(a2atype.NewArtifactEvent(request, a2atype.NewTextPart(final)), nil) {
				return
			}
		}
		yield(a2atype.NewStatusUpdateEvent(request, a2atype.TaskStateCompleted, nil), nil)
	}
}

// Cancel stops a turn in progress: the loop and whatever it started are killed.
func (e *executor) Cancel(_ context.Context, request *a2asrv.ExecutorContext) iter.Seq2[a2atype.Event, error] {
	return func(yield func(a2atype.Event, error) bool) {
		e.mu.Lock()
		if turn, ok := e.running[request.TaskID]; ok {
			turn.cancelled = true
			turn.stop()
		}
		e.mu.Unlock()
		yield(a2atype.NewStatusUpdateEvent(request, a2atype.TaskStateCanceled, nil), nil)
	}
}

// messageText joins the text parts of an A2A message; other part kinds are ignored.
func messageText(message *a2atype.Message) string {
	var text strings.Builder
	if message != nil {
		for _, part := range message.Parts {
			text.WriteString(part.Text())
		}
	}
	return text.String()
}

// turnTimeout reads TURN_TIMEOUT_SECONDS (a literal env value on the Harness).
func turnTimeout() time.Duration {
	if seconds, err := strconv.Atoi(os.Getenv("TURN_TIMEOUT_SECONDS")); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 15 * time.Minute
}

func main() {
	// Fetch this revision's compiled config from the controller into /config.
	// The shell reads only its runtime section; the loop reads the rest.
	if err := config.MaterializeFromBootstrap(context.Background(), "/config"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	compiled, err := config.LoadAgentConfig("/config/config.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	runtime := config.ProcessConfig(compiled)
	logger, err := logging.New(os.Stderr, cmp.Or(runtime.LogLevel, "info"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	application, err := app.New(app.AppConfig{
		ControlPlaneURL:  runtime.ControlPlaneUrl,
		MaxContentLength: runtime.MaxContentLength,
		AgentCard: a2atype.AgentCard{
			Name:         "memory-harness",
			Version:      "v1",
			Capabilities: a2atype.AgentCapabilities{Streaming: true},
		},
		Port:    fmt.Sprint(cmp.Or(runtime.GetPort(), 80)),
		AppName: cmp.Or(runtime.Name, "memory-harness"),
		Logger:  logger,
	}, &executor{timeout: turnTimeout(), running: map[a2atype.TaskID]*runningTurn{}})
	if err != nil {
		logger.Error("failed to create A2A application", "error", err)
		os.Exit(1)
	}
	if err := application.Run(); err != nil {
		logger.Error("A2A application stopped", "error", err)
		os.Exit(1)
	}
}
