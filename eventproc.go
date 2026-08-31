// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
	"github.com/go-steer/antigravity-sdk-go/internal/harness"
)

// stepEvent is one item on the processor's queue. Exactly one of its fields is
// meaningful: a step, a turn-ending error, or the idle marker that closes a
// turn.
type stepEvent struct {
	step Step
	err  error
	idle bool
}

// stepKey identifies a step within the session.
type stepKey struct {
	trajectoryID string
	index        uint32
}

// stepTracker deduplicates the repeated updates the harness sends for a single
// step, so hooks fire once and a wait request is answered once.
type stepTracker struct {
	state   pb.StepUpdate_State
	handled map[string]bool
}

// setState clears the handled-request set when a step leaves the waiting
// state, so a later wait on the same step is answered again.
func (t *stepTracker) setState(next pb.StepUpdate_State) {
	if t.state == pb.StepUpdate_STATE_WAITING_FOR_USER && next != pb.StepUpdate_STATE_WAITING_FOR_USER {
		clear(t.handled)
	}
	t.state = next
}

// markHandled records that a request type has been answered, reporting false
// if it already had been.
func (t *stepTracker) markHandled(kind string) bool {
	if t.handled[kind] {
		return false
	}
	if t.handled == nil {
		t.handled = map[string]bool{}
	}
	t.handled[kind] = true
	return true
}

// eventProcessor consumes OutputEvents from the harness, turns step updates
// into [Step] values, and answers the requests the harness makes of the client
// (tool calls, policy decisions, confirmations, questions).
//
// One instance serves a whole session. Its read loop runs on a goroutine
// started by [eventProcessor.start]; work that must not block that loop, such
// as running a user tool, is dispatched to further goroutines tracked by wg.
type eventProcessor struct {
	transport harness.Transport
	tools     map[string]Tool
	enforcer  *Enforcer
	hooks     *hookRunner
	logger    *slog.Logger

	// steps carries parsed steps to whoever is reading the current turn.
	steps chan stepEvent

	// sessionEnd is closed when the harness acknowledges a session-end
	// request.
	sessionEnd     chan struct{}
	sessionEndOnce sync.Once

	// idle is closed and replaced each turn; a reader waits on it to learn
	// that the trajectory has gone quiet.
	idleMu sync.Mutex
	idleCh chan struct{}

	wg sync.WaitGroup

	mu               sync.Mutex
	mainTrajectoryID string
	trackers         map[stepKey]*stepTracker
	cumulativeUsage  UsageMetadata
	trajectoryUsage  map[string]UsageMetadata
	trajectoryDepth  map[string]int
	turnStopReason   StopReason
}

// newEventProcessor builds a processor over an established transport.
func newEventProcessor(t harness.Transport, tools map[string]Tool, enforcer *Enforcer, hooks *hookRunner, logger *slog.Logger) *eventProcessor {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	p := &eventProcessor{
		transport:       t,
		tools:           tools,
		enforcer:        enforcer,
		hooks:           hooks,
		logger:          logger,
		steps:           make(chan stepEvent, 64),
		sessionEnd:      make(chan struct{}),
		idleCh:          make(chan struct{}),
		trackers:        map[stepKey]*stepTracker{},
		trajectoryUsage: map[string]UsageMetadata{},
		trajectoryDepth: map[string]int{},
		// No turn has ended yet, which reads the same as one that ended
		// normally rather than as an empty enum value.
		turnStopReason: StopUnspecified,
	}
	// The session starts idle: nothing is running until a turn is sent.
	close(p.idleCh)
	return p
}

// seedUsage records the usage the harness restored at startup.
func (p *eventProcessor) seedUsage(cumulative *UsageMetadata, perTrajectory map[string]UsageMetadata) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cumulative != nil {
		p.cumulativeUsage = *cumulative
	}
	for id, u := range perTrajectory {
		p.trajectoryUsage[id] = u
	}
}

// start launches the read loop. It returns immediately.
func (p *eventProcessor) start(ctx context.Context) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.readLoop(ctx)
	}()
}

// stop waits for the read loop and every dispatched goroutine to finish.
func (p *eventProcessor) stop() { p.wg.Wait() }

// readLoop pumps events until the harness hangs up or ctx ends.
func (p *eventProcessor) readLoop(ctx context.Context) {
	for {
		ev, err := p.transport.Recv(ctx)
		if err != nil {
			p.finish(err)
			return
		}
		p.handle(ctx, ev)
	}
}

// finish reports the end of the event stream to whoever is reading a turn.
//
// A clean hangup or a cancelled context is not an error the caller needs to
// see as a failure, but it must still release a blocked reader.
func (p *eventProcessor) finish(err error) {
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, context.Canceled):
		p.emit(stepEvent{idle: true})
	default:
		p.emit(stepEvent{err: fmt.Errorf("%w: %w", ErrConnection, err)})
		p.emit(stepEvent{idle: true})
	}
	p.markIdle()
	p.sessionEndOnce.Do(func() { close(p.sessionEnd) })
}

// emit queues an event for the turn reader, dropping it if nobody will ever
// read it. Blocking here would stall the read loop and deadlock the session.
func (p *eventProcessor) emit(ev stepEvent) {
	select {
	case p.steps <- ev:
	case <-p.sessionEnd:
	}
}

// handle routes a single OutputEvent.
func (p *eventProcessor) handle(ctx context.Context, ev *pb.OutputEvent) {
	switch {
	case ev.HasPolicyDecisionRequest():
		p.background(func() { p.handlePolicyDecision(ctx, ev.GetPolicyDecisionRequest()) })

	case ev.HasCallHookRequest():
		p.background(func() { p.handleHookRequest(ctx, ev.GetCallHookRequest()) })

	case ev.HasSessionEndResponse():
		p.sessionEndOnce.Do(func() { close(p.sessionEnd) })

	case ev.HasStepUpdate():
		p.handleStepUpdate(ctx, ev.GetStepUpdate())

	case ev.HasUsageUpdate():
		p.handleUsageUpdate(ev.GetUsageUpdate())

	case ev.HasTrajectoryStateUpdate():
		p.handleTrajectoryState(ev.GetTrajectoryStateUpdate())

	case ev.HasToolCall():
		p.background(func() { p.handleToolCall(ctx, ev.GetToolCall()) })
	}
}

// background runs fn on its own goroutine, tracked so shutdown can wait for
// it.
func (p *eventProcessor) background(fn func()) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		fn()
	}()
}

// ---------------------------------------------------------------------------
// Step updates
// ---------------------------------------------------------------------------

func (p *eventProcessor) handleStepUpdate(ctx context.Context, su *pb.StepUpdate) {
	key := stepKey{su.GetTrajectoryId(), su.GetStepIndex()}

	p.mu.Lock()
	tracker, ok := p.trackers[key]
	if !ok {
		tracker = &stepTracker{handled: map[string]bool{}}
		p.trackers[key] = tracker
	}
	tracker.setState(su.GetState())
	if p.mainTrajectoryID == "" && su.GetTrajectoryId() != "" {
		p.mainTrajectoryID = su.GetTrajectoryId()
	}
	depth := p.trajectoryDepth[su.GetTrajectoryId()]
	p.mu.Unlock()

	step := stepFromProto(su)
	step.Depth = depth

	if p.hooks != nil {
		p.hooks.dispatchStep(ctx, step)
	}

	// A custom tool announced in a step update is also delivered as a separate
	// tool_call event, which is what actually drives execution. Suppressing
	// the duplicate here rather than in stepFromProto keeps history
	// resumption intact, since replayed history carries no tool_call events.
	queued := step
	if p.ownsAnyTool(step.ToolCalls) {
		queued.ToolCalls = nil
	}
	p.emit(stepEvent{step: queued})

	if su.GetState() != pb.StepUpdate_STATE_WAITING_FOR_USER {
		return
	}
	if su.HasQuestionsRequest() && p.claim(key, "questions_request") {
		p.background(func() { p.answerQuestions(ctx, su) })
	}
	if su.HasToolConfirmationRequest() && p.claim(key, "tool_confirmation_request") {
		// Confirmation is auto-accepted: gating happens earlier, through the
		// pre-tool hook and policy path.
		p.background(func() { p.sendToolConfirmation(ctx, su, true) })
	}
}

// claim marks a wait request as handled, reporting whether this caller won.
func (p *eventProcessor) claim(key stepKey, kind string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	tracker, ok := p.trackers[key]
	if !ok {
		return false
	}
	return tracker.markHandled(kind)
}

// ownsAnyTool reports whether any of the calls names a tool this SDK executes.
func (p *eventProcessor) ownsAnyTool(calls []ToolCall) bool {
	for _, c := range calls {
		if _, ok := p.tools[c.Name]; ok {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Usage and trajectory state
// ---------------------------------------------------------------------------

func (p *eventProcessor) handleUsageUpdate(u *pb.UsageUpdate) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if u.HasTotal() {
		if parsed := usageFromProto(u.GetTotal()); parsed != nil {
			p.cumulativeUsage = *parsed
		}
	}
	for _, entry := range u.GetAgents() {
		if entry.GetTrajectoryId() == "" || !entry.HasUsage() {
			continue
		}
		if parsed := usageFromProto(entry.GetUsage()); parsed != nil {
			p.trajectoryUsage[entry.GetTrajectoryId()] = *parsed
		}
	}
}

func (p *eventProcessor) handleTrajectoryState(tsu *pb.TrajectoryStateUpdate) {
	id := tsu.GetTrajectoryId()

	p.mu.Lock()
	if id != "" {
		p.trajectoryDepth[id] = int(tsu.GetDepth())
	}
	main := p.mainTrajectoryID
	p.mu.Unlock()

	// Subagent trajectories are driven entirely by the harness. The client
	// tracks only the main trajectory's idleness, so a subagent going quiet
	// must not end the caller's turn.
	if main != "" && id != main {
		if tsu.GetError() != "" {
			p.logger.Info("subagent trajectory failed", "trajectory", id, "error", tsu.GetError())
		}
		return
	}

	if reason := tsu.GetStopReason(); reason != pb.TrajectoryStateUpdate_STOP_REASON_UNSPECIFIED {
		p.mu.Lock()
		p.turnStopReason = stopReasonFromProto(reason)
		p.mu.Unlock()
	}

	switch tsu.GetState() {
	case pb.TrajectoryStateUpdate_STATE_RUNNING:
		p.markRunning()

	// The idle marker is queued before the gate opens. A caller that sees the
	// trajectory as idle can then rely on the turn's remaining events already
	// being on the queue, rather than racing the goroutine that emits them.
	case pb.TrajectoryStateUpdate_STATE_FULLY_IDLE:
		if msg := tsu.GetError(); msg != "" {
			p.emit(stepEvent{err: fmt.Errorf("%w: %s", ErrExecution, msg)})
		}
		p.emit(stepEvent{idle: true})
		p.markIdle()

	case pb.TrajectoryStateUpdate_STATE_CANCELLED:
		msg := tsu.GetError()
		if msg == "" {
			p.emit(stepEvent{err: ErrCancelled})
		} else {
			p.emit(stepEvent{err: fmt.Errorf("%w: %s", ErrCancelled, msg)})
		}
		p.emit(stepEvent{idle: true})
		p.markIdle()
	}
}

// markRunning reopens the idle gate so waiters block until the turn ends.
func (p *eventProcessor) markRunning() {
	p.idleMu.Lock()
	defer p.idleMu.Unlock()
	select {
	case <-p.idleCh:
		p.idleCh = make(chan struct{})
	default:
	}
}

// markIdle releases anyone waiting for the trajectory to go quiet.
func (p *eventProcessor) markIdle() {
	p.idleMu.Lock()
	defer p.idleMu.Unlock()
	select {
	case <-p.idleCh:
	default:
		close(p.idleCh)
	}
}

// idleSignal returns a channel closed once the trajectory is idle.
func (p *eventProcessor) idleSignal() <-chan struct{} {
	p.idleMu.Lock()
	defer p.idleMu.Unlock()
	return p.idleCh
}

// isIdle reports whether the trajectory is currently quiet.
func (p *eventProcessor) isIdle() bool {
	select {
	case <-p.idleSignal():
		return true
	default:
		return false
	}
}

// hasPending reports whether the queue still holds events nobody has read.
func (p *eventProcessor) hasPending() bool { return len(p.steps) > 0 }

// resetForTurn clears per-turn state and drains any steps left over from a
// turn nobody finished reading.
func (p *eventProcessor) resetForTurn() {
	p.mu.Lock()
	p.mainTrajectoryID = ""
	p.turnStopReason = StopUnspecified
	p.mu.Unlock()

	p.markRunning()

	for {
		select {
		case <-p.steps:
		default:
			return
		}
	}
}

// usage returns the session's cumulative token usage.
func (p *eventProcessor) usage() UsageMetadata {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cumulativeUsage
}

// usageByTrajectory returns a copy of the per-trajectory usage totals.
func (p *eventProcessor) usageByTrajectory() map[string]UsageMetadata {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]UsageMetadata, len(p.trajectoryUsage))
	for id, u := range p.trajectoryUsage {
		out[id] = u
	}
	return out
}

// stopReason returns why the most recent turn ended.
func (p *eventProcessor) stopReason() StopReason {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.turnStopReason
}

// ---------------------------------------------------------------------------
// Tool calls
// ---------------------------------------------------------------------------

// handleToolCall runs a custom tool and returns its result to the harness.
//
// Every failure path still sends a response: a harness left waiting on a tool
// result stalls the whole turn.
func (p *eventProcessor) handleToolCall(ctx context.Context, tc *pb.ToolCall) {
	call := ToolCall{
		ID:   tc.GetId(),
		Name: tc.GetName(),
		Args: rawArgsOrEmpty(tc.GetArgumentsJson()),
	}

	// Announce the call so a caller streaming the turn sees it dispatched.
	p.emit(stepEvent{step: Step{
		ID:        call.ID,
		Index:     1,
		Type:      StepToolCall,
		Source:    SourceModel,
		Target:    TargetEnvironment,
		Status:    StatusActive,
		ToolCalls: []ToolCall{call},
	}})

	tool, ok := p.tools[call.Name]
	if !ok {
		p.logger.Warn("the harness called a tool this client does not provide",
			"tool", call.Name, "id", call.ID)
		p.sendToolError(ctx, call, fmt.Sprintf("no tool named %q is registered with this client", call.Name))
		return
	}

	result, err := p.invokeTool(ctx, tool, call)
	if err != nil {
		p.sendToolError(ctx, call, err.Error())
		return
	}
	p.sendToolResult(ctx, call, result)
}

// invokeTool calls a tool, converting a panic into an ordinary error so one
// misbehaving tool cannot take down the session.
func (p *eventProcessor) invokeTool(ctx context.Context, tool Tool, call ToolCall) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %q panicked: %v", call.Name, r)
			p.logger.Error("tool panicked", "tool", call.Name, "panic", r)
		}
	}()
	return tool.Call(ctx, call.Args)
}

// sendToolResult returns a successful result, forwarding any media as real
// attachments.
func (p *eventProcessor) sendToolResult(ctx context.Context, call ToolCall, value any) {
	cleaned, media := splitMedia(value)
	if len(media) > 0 && cleaned == nil {
		cleaned = fmt.Sprintf("Returned %d media attachment(s).", len(media))
	}

	encoded, err := encodeToolResult(cleaned)
	if err != nil {
		p.sendToolError(ctx, call, fmt.Sprintf("the tool's result could not be encoded: %v", err))
		return
	}

	attachments := make([]*pb.Media, 0, len(media))
	for _, m := range media {
		attachments = append(attachments, pb.Media_builder{
			MimeType:    proto.String(m.MIME()),
			Data:        m.Bytes(),
			Description: proto.String(m.Describe()),
		}.Build())
	}

	p.sendToolResponse(ctx, pb.ToolResponse_builder{
		Id:                proto.String(call.ID),
		ResponseJson:      proto.String(string(encoded)),
		SupplementalMedia: attachments,
	}.Build())
}

// sendToolError reports a failed tool call to the harness so the model can
// react to it.
func (p *eventProcessor) sendToolError(ctx context.Context, call ToolCall, msg string) {
	p.sendToolResponse(ctx, pb.ToolResponse_builder{
		Id:           proto.String(call.ID),
		ErrorMessage: proto.String(msg),
	}.Build())
}

func (p *eventProcessor) sendToolResponse(ctx context.Context, resp *pb.ToolResponse) {
	if resp.GetId() == "" {
		p.logger.Error("refusing to send a tool response with no id, which the harness cannot correlate")
		return
	}
	p.send(ctx, pb.InputEvent_builder{ToolResponse: resp}.Build())
}

// ---------------------------------------------------------------------------
// Confirmations and questions
// ---------------------------------------------------------------------------

func (p *eventProcessor) sendToolConfirmation(ctx context.Context, su *pb.StepUpdate, accepted bool) {
	p.send(ctx, pb.InputEvent_builder{
		ToolConfirmation: pb.ToolConfirmation_builder{
			TrajectoryId: proto.String(su.GetTrajectoryId()),
			StepIndex:    proto.Uint32(su.GetStepIndex()),
			Accepted:     proto.Bool(accepted),
		}.Build(),
	}.Build())
}

// answerQuestions responds to a question request by consulting the
// interaction hooks.
//
// Every question is answered, if only as unanswered: a harness left waiting on
// an answer stalls the turn. Questions the SDK cannot represent, and questions
// no hook replied to, are reported unanswered so the agent can move on.
func (p *eventProcessor) answerQuestions(ctx context.Context, su *pb.StepUpdate) {
	raw := su.GetQuestionsRequest().GetQuestions()

	answers := make([]*pb.UserQuestionAnswer, len(raw))
	for i := range answers {
		answers[i] = unansweredQuestion()
	}

	// Only multiple-choice questions have a representation in the SDK. Their
	// original positions are kept so the replies line up with what was asked.
	var (
		req      QuestionRequest
		askedAt  []int
		anyOther bool
	)
	for i, q := range raw {
		if !q.HasMultipleChoice() {
			anyOther = true
			continue
		}
		mc := q.GetMultipleChoice()
		options := make([]QuestionOption, 0, len(mc.GetChoices()))
		for _, choice := range mc.GetChoices() {
			options = append(options, QuestionOption{Text: choice})
		}
		req.Questions = append(req.Questions, Question{
			Text:        mc.GetQuestion(),
			Options:     options,
			MultiSelect: mc.GetIsMultiSelect(),
		})
		askedAt = append(askedAt, i)
	}
	if anyOther {
		p.logger.Warn("skipping question types this SDK does not understand")
	}

	switch {
	case len(req.Questions) == 0:
		// Nothing to ask about; the unanswered replies above stand.

	case p.hooks == nil || len(p.hooks.interaction) == 0:
		p.logger.Warn("the agent asked the user a question but no interaction hook is registered; skipping",
			"questions", len(req.Questions))

	default:
		if replies := p.hooks.dispatchInteraction(ctx, req); replies != nil {
			for i, answer := range replies.Answers {
				if i >= len(askedAt) {
					break
				}
				answers[askedAt[i]] = questionAnswerProto(answer, req.Questions[i])
			}
		}
	}

	p.sendQuestionAnswers(ctx, su, answers)
}

func unansweredQuestion() *pb.UserQuestionAnswer {
	return pb.UserQuestionAnswer_builder{Unanswered: proto.Bool(true)}.Build()
}

// questionAnswerProto renders one answer, dropping option indices that fall
// outside the question actually asked.
func questionAnswerProto(answer Answer, question Question) *pb.UserQuestionAnswer {
	if answer.Skipped {
		return unansweredQuestion()
	}
	indices := make([]int32, 0, len(answer.SelectedOptions))
	for _, i := range answer.SelectedOptions {
		if i >= 0 && i < len(question.Options) {
			indices = append(indices, int32(i))
		}
	}
	return pb.UserQuestionAnswer_builder{
		MultipleChoiceAnswer: pb.MultipleChoiceAnswer_builder{
			SelectedChoiceIndices: indices,
			FreeformResponse:      proto.String(answer.Text),
		}.Build(),
	}.Build()
}

func (p *eventProcessor) sendQuestionAnswers(ctx context.Context, su *pb.StepUpdate, answers []*pb.UserQuestionAnswer) {
	p.send(ctx, pb.InputEvent_builder{
		QuestionResponse: pb.UserQuestionsResponse_builder{
			TrajectoryId: proto.String(su.GetTrajectoryId()),
			StepIndex:    proto.Uint32(su.GetStepIndex()),
			Response: pb.UserQuestionsResponse_QuestionsResponse_builder{
				Answers: answers,
			}.Build(),
		}.Build(),
	}.Build())
}

// ---------------------------------------------------------------------------
// Dynamic policy decisions
// ---------------------------------------------------------------------------

// handlePolicyDecision evaluates a policy rule the harness deferred to the
// client and returns the verdict.
//
// Every path answers, and every uncertain path denies: a harness waiting on a
// decision would stall, and an unanswerable rule must not become an implicit
// approval.
func (p *eventProcessor) handlePolicyDecision(ctx context.Context, req *pb.PolicyDecisionRequest) {
	ruleID := req.GetRuleId()

	policy, ok := p.dynamicPolicy(ruleID)
	if !ok {
		p.logger.Error("the harness asked about an unknown policy rule", "rule", ruleID)
		p.sendPolicyDecision(ctx, req.GetRequestId(),
			pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_DENY,
			fmt.Sprintf("unknown rule_id %q", ruleID))
		return
	}

	args := req.GetToolArgs()
	call := ToolCall{
		Name:       args.GetToolName(),
		Args:       rawArgsOrEmpty(args.GetArgumentsJson()),
		ServerName: args.GetServerName(),
	}

	if policy.When != nil {
		matched, err := policy.When(ctx, call)
		if err != nil {
			p.sendPolicyDecision(ctx, req.GetRequestId(),
				pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_DENY,
				fmt.Sprintf("policy evaluation failed: %v", err))
			return
		}
		if !matched {
			p.sendPolicyDecision(ctx, req.GetRequestId(),
				pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_NO_MATCH, "")
			return
		}
	}

	switch policy.Decision {
	case DecisionAskUser:
		allow, err := policy.AskUser(ctx, call)
		switch {
		case err != nil:
			p.sendPolicyDecision(ctx, req.GetRequestId(),
				pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_DENY,
				fmt.Sprintf("policy evaluation failed: %v", err))
		case allow:
			p.sendPolicyDecision(ctx, req.GetRequestId(),
				pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_ALLOW, "")
		default:
			p.sendPolicyDecision(ctx, req.GetRequestId(),
				pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_DENY,
				fmt.Sprintf("denied by user (%s).", policy.label()))
		}

	case DecisionDeny:
		p.sendPolicyDecision(ctx, req.GetRequestId(),
			pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_DENY,
			fmt.Sprintf("denied by policy %q.", policy.label()))

	case DecisionApprove:
		p.sendPolicyDecision(ctx, req.GetRequestId(),
			pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_ALLOW, "")

	default:
		p.sendPolicyDecision(ctx, req.GetRequestId(),
			pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_DENY,
			fmt.Sprintf("policy %q has no usable decision", policy.label()))
	}
}

// dynamicPolicy looks up a rule the harness is asking about.
func (p *eventProcessor) dynamicPolicy(ruleID string) (Policy, bool) {
	if p.enforcer == nil {
		return Policy{}, false
	}
	return p.enforcer.byRuleID(ruleID)
}

func (p *eventProcessor) sendPolicyDecision(ctx context.Context, requestID string, outcome pb.PolicyEvaluationOutcome, denyReason string) {
	p.send(ctx, pb.InputEvent_builder{
		PolicyDecisionResponse: pb.PolicyDecisionResponse_builder{
			RequestId:  proto.String(requestID),
			Outcome:    outcome.Enum(),
			DenyReason: proto.String(denyReason),
		}.Build(),
	}.Build())
}

// ---------------------------------------------------------------------------
// Sending
// ---------------------------------------------------------------------------

// send writes an event, logging rather than propagating failures: these calls
// happen on background goroutines with no caller to return an error to, and a
// dead transport is already being reported through the step stream.
func (p *eventProcessor) send(ctx context.Context, ev *pb.InputEvent) {
	if err := p.transport.Send(ctx, ev); err != nil && !errors.Is(err, context.Canceled) {
		p.logger.Error("sending an event to the harness failed", "error", err)
	}
}

// requestSessionEnd asks the harness to shut the session down and waits for
// its acknowledgement.
func (p *eventProcessor) requestSessionEnd(ctx context.Context) error {
	if err := p.transport.Send(ctx, pb.InputEvent_builder{
		SessionEndRequest: proto.Bool(true),
	}.Build()); err != nil {
		return err
	}
	select {
	case <-p.sessionEnd:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// halt asks the harness to stop the current turn.
func (p *eventProcessor) halt(ctx context.Context) error {
	return p.transport.Send(ctx, pb.InputEvent_builder{HaltRequest: proto.Bool(true)}.Build())
}

// sendUserInput delivers a prompt to the harness.
func (p *eventProcessor) sendUserInput(ctx context.Context, prompt []Content) error {
	parts, err := userInputParts(prompt)
	if err != nil {
		return err
	}
	return p.transport.Send(ctx, pb.InputEvent_builder{
		UserInput: pb.UserInput_builder{Parts: parts}.Build(),
	}.Build())
}

// sendTrigger pushes an externally originated message into the agent.
func (p *eventProcessor) sendTrigger(ctx context.Context, message string) error {
	return p.transport.Send(ctx, pb.InputEvent_builder{
		AutomatedTrigger: proto.String(message),
	}.Build())
}

// userInputParts converts public content into wire parts.
func userInputParts(prompt []Content) ([]*pb.UserInput_Part, error) {
	parts := make([]*pb.UserInput_Part, 0, len(prompt))
	for _, c := range prompt {
		switch v := c.(type) {
		case Text:
			parts = append(parts, pb.UserInput_Part_builder{
				Text: proto.String(string(v)),
			}.Build())

		case SlashCommand:
			parts = append(parts, pb.UserInput_Part_builder{
				SlashCommand: pb.UserInput_SlashCommand_builder{
					Name: proto.String(string(v.Name)),
				}.Build(),
			}.Build())

		case Media:
			parts = append(parts, pb.UserInput_Part_builder{
				Media: pb.UserInput_Media_builder{
					MimeType:    proto.String(v.MIME()),
					Description: proto.String(v.Describe()),
					Data:        v.Bytes(),
				}.Build(),
			}.Build())

		default:
			return nil, fmt.Errorf("%w: unsupported content type %T", ErrInvalidPrompt, c)
		}
	}
	return parts, nil
}

// jsonArgs is a convenience for tests and callers that need tool arguments as
// a map.
func jsonArgs(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
