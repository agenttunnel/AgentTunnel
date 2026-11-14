package orchestrator

import (
	"context"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// soloName is the roster name of a solo thread's single agent.
const soloName = "solo"

// runSoloTurn executes one user turn on a solo thread: a single parentless
// agent on the thread's solo runtime works the request itself. There is no
// orchestrator team, nothing dispatches, and every text line the harness
// prints is conversation. Steering still earns extra passes: the loop runs
// while steering is queued, so a steer typed mid-run is weighed before the
// turn reports done.
func (o *Orchestrator) runSoloTurn(ctx context.Context, threadID contracts.ThreadID, userMessage string, images []providers.ImageInput, rt contracts.LeadRuntime) (err error) {
	// Nothing curates a solo thread — there is no runTask, so no
	// requestCuration — but the wait mirrors RunTurn's so every turn shape
	// keeps the same "no event lands after the turn reports done" invariant.
	defer o.runner.curator.wait(threadID)

	state, err := o.runner.engine.State(ctx, threadID)
	if err != nil {
		return err
	}

	// The title preamble is RunTurn's: a thread that still carries its
	// placeholder name is titled from the first prompt alone, off the turn,
	// and the cycle's summary refines it once the turn lands. titleDone orders
	// the two generations so the summary-informed title is the one that stays.
	// The role feeds only the fallback in generateTitle's runtime resolution;
	// a solo thread resolves its own runtime.
	role, _ := o.runner.roles.Get(contracts.RoleOrchestrator)
	var titleDone chan struct{}
	if isDefaultTitle(state.Title) {
		titleDone = make(chan struct{})
		done := titleDone
		go func() {
			defer close(done)
			o.runner.generateTitle(context.Background(), threadID, role, userMessage, "")
		}()
	}

	// The solo agent rides the lead-seed shape: the lead-zero agent id and the
	// orchestrator role keep it parentless, so usage attribution, the board,
	// the graph fold, and the projector's turn-end stamping all read it as the
	// thread's anchor. DispatchTask carries no variant; the session spec takes
	// it from rt.
	id := leadAgentID(0)
	seed := []contracts.DispatchTask{{
		AgentID: id,
		Name:    soloName,
		Role:    contracts.RoleOrchestrator,
		Task:    userMessage,
		Harness: rt.Harness,
		Model:   rt.Model,
	}}
	if err := o.runner.Dispatch(ctx, threadID, seed); err != nil {
		return err
	}
	o.runner.setStatus(ctx, threadID, id, contracts.AgentRunning, "")

	// Whatever goes wrong mid-turn — failure, engine errors, or a stop landing
	// between passes — the solo agent owes the thread a terminal status.
	// setStatus detaches from the turn context so a cancelled turn can still
	// record it.
	defer func() {
		if err == nil {
			return
		}
		finishCtx := context.WithoutCancel(ctx)
		if ctx.Err() != nil {
			o.runner.setStatus(finishCtx, threadID, id, contracts.AgentCancelled, "turn stopped")
		} else {
			o.runner.setStatus(finishCtx, threadID, id, contracts.AgentFailed, err.Error())
		}
	}()

	var finalText string
	for pass := 0; pass == 0 || o.runner.HasSteering(threadID); pass++ {
		// Steering is drained once per pass, the moment the pass takes it:
		// recorded in the transcript so the client can retire its pending
		// style. Images steering carries join the turn's on the pass that
		// weighs them.
		entries := o.runner.DrainSteering(threadID)
		steering := make([]string, len(entries))
		consumed := make([]string, 0, len(entries))
		var steerImages []providers.ImageInput
		for i, entry := range entries {
			steering[i] = entry.Text
			steerImages = append(steerImages, entry.Images...)
			if entry.MessageID != "" {
				consumed = append(consumed, entry.MessageID)
			}
		}
		if len(steering) > 0 {
			if err := o.runner.engine.Dispatch(ctx, contracts.Command{
				ID:       o.runner.nextID(),
				Type:     contracts.CmdSteeringInjected,
				ThreadID: threadID,
				Injected: &contracts.SteeringInjected{Count: len(steering)},
			}); err != nil {
				return err
			}
		}
		if len(consumed) > 0 {
			if err := o.runner.engine.Dispatch(ctx, contracts.Command{
				ID:              o.runner.nextID(),
				Type:            contracts.CmdSteeringConsume,
				ThreadID:        threadID,
				SteeringConsume: &contracts.SteeringConsumed{MessageIDs: consumed},
			}); err != nil {
				return err
			}
		}
		state, err := o.runner.engine.State(ctx, threadID)
		if err != nil {
			return err
		}
		prompt := BuildSoloPrompt(renderHistory(priorMessages(state, userMessage)), userMessage, steering)
		// The turn's attachments, plus any steering images, reach the agent
		// every pass: each pass is a fresh one-shot process. A harness that
		// cannot take images natively gets them as a path note instead.
		passImages := append(append([]providers.ImageInput(nil), images...), steerImages...)
		if o.runner.registry != nil && o.runner.registry.ImageSupport(rt.Harness) != providers.ImagesSupported {
			prompt += imagesNote(passImages)
		}
		text, err := o.runner.runSoloPass(ctx, threadID, id, rt, prompt, passImages, pass)
		if err != nil {
			return err
		}
		if strings.TrimSpace(text) != "" {
			finalText = text
		}
	}

	o.runner.setStatus(ctx, threadID, id, contracts.AgentSucceeded, "")
	if strings.TrimSpace(finalText) != "" {
		if err := o.runner.engine.Dispatch(ctx, contracts.Command{
			ID:       o.runner.nextID(),
			Type:     contracts.CmdMessageAppend,
			ThreadID: threadID,
			Message:  &contracts.MessageSent{MessageID: o.runner.nextID(), Author: soloName, Text: finalText},
		}); err != nil {
			return err
		}
	}

	// The same first-cycle retitle tail as RunTurn: the second generation
	// waits for the first and overwrites it.
	if titleDone != nil {
		go func() {
			<-titleDone
			o.runner.generateTitle(context.Background(), threadID, role, userMessage, finalText)
		}()
	}
	return nil
}

// soloHeader tells the solo agent what it is: the only agent on the thread,
// with no orchestrator machinery behind it.
const soloHeader = "You are the only agent on this thread. Work the request yourself directly: there is no orchestrator, no team, and nothing to dispatch."

// BuildSoloPrompt assembles the solo agent's pass prompt: the thread's earlier
// conversation, the request, and any steering the user sent while a pass was
// running. There is no orchestrator protocol to document — no roster, no
// dispatch, peer, or tool machinery — the agent works the request itself.
func BuildSoloPrompt(history, userMessage string, steering []string) string {
	var b strings.Builder
	b.WriteString(soloHeader)
	b.WriteString("\n")
	if strings.TrimSpace(history) != "" {
		b.WriteString("\n## Conversation so far\n")
		b.WriteString(history)
		b.WriteString("\nThis is the thread's earlier conversation, oldest first. Use it to resolve references like 'that', 'the steering', or 'the last result'. Act on the request below.")
	}
	b.WriteString("\n\n## Request\n")
	b.WriteString(userMessage)
	if len(steering) > 0 {
		b.WriteString("\n\n## Steering (sent by the user while you were working)\n")
		for _, s := range steering {
			b.WriteString("- ")
			b.WriteString(s)
			b.WriteString("\n")
		}
		b.WriteString("\nWeigh this now and adjust what you do next.")
	}
	return b.String()
}
