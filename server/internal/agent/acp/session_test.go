package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	types "mindfs/server/internal/agent/types"

	acpsdk "github.com/coder/acp-go-sdk"
)

func TestIsExpectedStreamCloseError(t *testing.T) {
	for _, err := range []error{nil, os.ErrClosed, io.ErrClosedPipe, &os.PathError{Op: "read", Path: "|0", Err: os.ErrClosed}} {
		if !isExpectedStreamCloseError(err) {
			t.Fatalf("error %v should be treated as an expected stream close", err)
		}
	}
	if isExpectedStreamCloseError(errors.New("unexpected read failure")) {
		t.Fatal("unexpected read failure was suppressed")
	}
}

func TestACPDSHTokenUsagePreservesPromptUsage(t *testing.T) {
	state := &sessionState{}
	firstRead, firstWrite := 4_000, 1_000
	first := state.tokenUsageForPrompt("dsh", &acpsdk.Usage{
		InputTokens:       5_500,
		OutputTokens:      500,
		CachedReadTokens:  &firstRead,
		CachedWriteTokens: &firstWrite,
	})
	if first == nil || first.InputTokens != 5_500 || first.OutputTokens != 500 {
		t.Fatalf("first usage = %#v", first)
	}

	secondRead, secondWrite := 12_000, 1_500
	second := state.tokenUsageForPrompt("dsh", &acpsdk.Usage{
		InputTokens:       14_000,
		OutputTokens:      1_600,
		CachedReadTokens:  &secondRead,
		CachedWriteTokens: &secondWrite,
	})
	if second == nil || second.InputTokens != 14_000 || second.OutputTokens != 1_600 {
		t.Fatalf("second usage = %#v", second)
	}
	if second.CacheReadTokens == nil || *second.CacheReadTokens != 12_000 {
		t.Fatalf("second cache read = %#v", second.CacheReadTokens)
	}
	if second.CacheWriteTokens == nil || *second.CacheWriteTokens != 1_500 {
		t.Fatalf("second cache write = %#v", second.CacheWriteTokens)
	}
}

func TestACPTokenUsageConvertsCumulativeCountersToTurnDelta(t *testing.T) {
	for _, agentName := range []string{"copilot", "unknown", "deepseek", ""} {
		t.Run(agentName, func(t *testing.T) {
			state := &sessionState{}
			firstRead, firstWrite := 4_000, 1_000
			first := state.tokenUsageForPrompt(agentName, &acpsdk.Usage{
				InputTokens:       5_500,
				OutputTokens:      500,
				CachedReadTokens:  &firstRead,
				CachedWriteTokens: &firstWrite,
			})
			if first == nil || first.InputTokens != 5_500 || first.OutputTokens != 500 {
				t.Fatalf("first usage = %#v", first)
			}

			secondRead, secondWrite := 12_000, 1_500
			second := state.tokenUsageForPrompt(agentName, &acpsdk.Usage{
				InputTokens:       14_000,
				OutputTokens:      1_600,
				CachedReadTokens:  &secondRead,
				CachedWriteTokens: &secondWrite,
			})
			if second == nil || second.InputTokens != 8_500 || second.OutputTokens != 1_100 {
				t.Fatalf("second usage = %#v", second)
			}
			if second.CacheReadTokens == nil || *second.CacheReadTokens != 8_000 {
				t.Fatalf("second cache read = %#v", second.CacheReadTokens)
			}
			if second.CacheWriteTokens == nil || *second.CacheWriteTokens != 500 {
				t.Fatalf("second cache write = %#v", second.CacheWriteTokens)
			}
		})
	}
}

func TestACPTokenUsageSeparatesUncachedInputUsingReportedTotal(t *testing.T) {
	// OMP reports inputTokens excluding cache, and totalTokens covering every
	// token type; the logical input must therefore include cache read/write.
	state := &sessionState{}
	read, write := 200_000, 1_000
	exclusive := state.tokenUsageForPrompt("omp", &acpsdk.Usage{
		InputTokens:       900,
		OutputTokens:      300,
		TotalTokens:       900 + 300 + 200_000 + 1_000,
		CachedReadTokens:  &read,
		CachedWriteTokens: &write,
	})
	if exclusive == nil || exclusive.InputTokens != 201_900 || exclusive.OutputTokens != 300 {
		t.Fatalf("exclusive usage = %#v", exclusive)
	}

	// Agents whose totalTokens is input+output already include cached tokens
	// in inputTokens; adding them again would double count.
	inclusiveRead := 4_000
	inclusive := (&sessionState{}).tokenUsageForPrompt("other", &acpsdk.Usage{
		InputTokens:      5_000,
		OutputTokens:     500,
		TotalTokens:      5_500,
		CachedReadTokens: &inclusiveRead,
	})
	if inclusive == nil || inclusive.InputTokens != 5_000 {
		t.Fatalf("inclusive usage = %#v", inclusive)
	}
}

func TestACPTokenUsageCounterResetAndMissingUsage(t *testing.T) {
	for _, agentName := range []string{"dsh", "copilot"} {
		t.Run(agentName, func(t *testing.T) {
			state := &sessionState{}
			state.tokenUsageForPrompt(agentName, &acpsdk.Usage{InputTokens: 1000, OutputTokens: 100})
			if got := state.tokenUsageForPrompt(agentName, nil); got != nil {
				t.Fatalf("missing usage = %#v", got)
			}
			got := state.tokenUsageForPrompt(agentName, &acpsdk.Usage{InputTokens: 500, OutputTokens: 50})
			if got == nil || got.InputTokens != 500 || got.OutputTokens != 50 {
				t.Fatalf("usage after counter reset = %#v", got)
			}
		})
	}
}

func TestACPUsageUpdateReplacesCurrentContextUsage(t *testing.T) {
	state := &sessionState{contextWindow: types.ContextWindow{
		TotalTokens:        1_911_735,
		ModelContextWindow: 512_000,
	}}

	state.setUsageUpdate(217_991, 512_000)
	got := state.getContextWindow()
	if got.TotalTokens != 217_991 || got.ModelContextWindow != 512_000 {
		t.Fatalf("context window after first update = %#v", got)
	}
	if !state.hasContextUsageUpdate() {
		t.Fatal("usage update was not marked authoritative")
	}

	state.setUsageUpdate(220_104, 512_000)
	got = state.getContextWindow()
	if got.TotalTokens != 220_104 || got.ModelContextWindow != 512_000 {
		t.Fatalf("context window after second update = %#v", got)
	}
}

func TestCloseProcessesConcurrentlyDoesNotSerializeWaits(t *testing.T) {
	procs := []*Process{{agentName: "first"}, {agentName: "second"}}
	started := make(chan string, len(procs))
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		closeProcessesConcurrently(procs, func(proc *Process) error {
			started <- proc.agentLabel()
			<-release
			return nil
		})
		close(done)
	}()

	seen := make(map[string]bool, len(procs))
	for range procs {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatal("process closes were serialized")
		}
	}
	if !seen["first"] || !seen["second"] {
		t.Fatalf("started closes = %v", seen)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("concurrent process closes did not complete")
	}
}

func TestWrapSessionUpdateRecognizesPlan(t *testing.T) {
	update := wrapSessionUpdate("session-1", acpsdk.SessionUpdate{
		Plan: &acpsdk.SessionUpdatePlan{
			Entries: []acpsdk.PlanEntry{{Content: "Inspect files", Status: acpsdk.PlanEntryStatusPending}},
		},
	})
	if update.Type != UpdateTypePlan {
		t.Fatalf("update.Type = %q, want %q", update.Type, UpdateTypePlan)
	}
}

func TestMapModelStateUsesLegacyACPModels(t *testing.T) {
	models := mapModelState(&acpsdk.SessionModelState{
		CurrentModelId: acpsdk.ModelId("gpt-4.1"),
		AvailableModels: []acpsdk.ModelInfo{
			{
				ModelId:     acpsdk.ModelId("gpt-4.1"),
				Name:        "GPT-4.1",
				Description: acpsdk.Ptr("Fast model"),
			},
		},
	})
	if models.CurrentModelID != "gpt-4.1" {
		t.Fatalf("CurrentModelID = %q", models.CurrentModelID)
	}
	if len(models.Models) != 1 {
		t.Fatalf("Models = %#v", models.Models)
	}
	if got := models.Models[0]; got.ID != "gpt-4.1" || got.Name != "GPT-4.1" || got.Description != "Fast model" {
		t.Fatalf("model = %#v", got)
	}
}

func TestConvertEventMapsACPPlanToTodoUpdate(t *testing.T) {
	event := convertEvent(SessionUpdate{
		Type:      UpdateTypePlan,
		SessionID: "session-1",
		Raw: acpsdk.SessionUpdate{
			Plan: &acpsdk.SessionUpdatePlan{
				Entries: []acpsdk.PlanEntry{
					{Content: "Inspect files", Status: acpsdk.PlanEntryStatusPending},
					{Content: "Patch implementation", Status: acpsdk.PlanEntryStatusInProgress},
					{Content: "Run tests", Status: acpsdk.PlanEntryStatusCompleted},
				},
			},
		},
	})
	if event.Type != types.EventTypeTodoUpdate {
		t.Fatalf("event.Type = %q, want %q", event.Type, types.EventTypeTodoUpdate)
	}
	todo, ok := event.Data.(types.TodoUpdate)
	if !ok {
		t.Fatalf("event.Data = %T, want TodoUpdate", event.Data)
	}
	if len(todo.Items) != 3 {
		t.Fatalf("todo.Items = %#v, want 3 items", todo.Items)
	}
	if todo.Items[0].Content != "Inspect files" || todo.Items[0].Status != "pending" {
		t.Fatalf("todo.Items[0] = %#v", todo.Items[0])
	}
	if todo.Items[1].Content != "Patch implementation" || todo.Items[1].Status != "in_progress" {
		t.Fatalf("todo.Items[1] = %#v", todo.Items[1])
	}
	if todo.Items[2].Content != "Run tests" || todo.Items[2].Status != "completed" {
		t.Fatalf("todo.Items[2] = %#v", todo.Items[2])
	}
}

func TestConvertEventPreservesACPHumanReadableToolTitle(t *testing.T) {
	event := convertEvent(SessionUpdate{
		Type:      UpdateTypeToolCall,
		SessionID: "session-1",
		Raw: acpsdk.SessionUpdate{
			ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: "tool-1",
				Title:      "Run the test suite",
				Kind:       acpsdk.ToolKindExecute,
				Status:     acpsdk.ToolCallStatusInProgress,
			},
		},
	})
	toolCall, ok := event.Data.(types.ToolCall)
	if !ok {
		t.Fatalf("event.Data = %T, want ToolCall", event.Data)
	}
	if toolCall.Title != "Run the test suite" {
		t.Fatalf("title = %q, want ACP human-readable title", toolCall.Title)
	}
}

func TestConvertEventMapsACPPlanUpdateMarkdownAndFileToPlanUpdate(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  acpsdk.SessionUpdate
		want types.PlanUpdate
	}{
		{
			name: "markdown",
			raw: acpsdk.SessionUpdate{
				PlanUpdate: &acpsdk.SessionPlanUpdate{
					Plan: acpsdk.PlanUpdateContent{
						Markdown: &acpsdk.PlanUpdateContentMarkdown{
							Id:      "plan-1",
							Content: "## Plan\n\n- Step",
						},
					},
				},
			},
			want: types.PlanUpdate{ID: "plan-1", Content: "## Plan\n\n- Step"},
		},
		{
			name: "file",
			raw: acpsdk.SessionUpdate{
				PlanUpdate: &acpsdk.SessionPlanUpdate{
					Plan: acpsdk.PlanUpdateContent{
						File: &acpsdk.PlanUpdateContentFile{
							Id:  "plan-3",
							Uri: "file:///tmp/PLAN.md",
						},
					},
				},
			},
			want: types.PlanUpdate{ID: "plan-3", Content: "Plan file: file:///tmp/PLAN.md"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := convertEvent(SessionUpdate{
				Type:      UpdateTypePlan,
				SessionID: "session-1",
				Raw:       tc.raw,
			})
			if event.Type != types.EventTypePlanUpdate {
				t.Fatalf("event.Type = %q, want %q", event.Type, types.EventTypePlanUpdate)
			}
			got, ok := event.Data.(types.PlanUpdate)
			if !ok {
				t.Fatalf("event.Data = %T, want PlanUpdate", event.Data)
			}
			if got != tc.want {
				t.Fatalf("plan = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestConvertEventMapsACPPlanUpdateItemsToTodoUpdate(t *testing.T) {
	event := convertEvent(SessionUpdate{
		Type:      UpdateTypePlan,
		SessionID: "session-1",
		Raw: acpsdk.SessionUpdate{
			PlanUpdate: &acpsdk.SessionPlanUpdate{
				Plan: acpsdk.PlanUpdateContent{
					Items: &acpsdk.PlanUpdateContentItems{
						Id: "plan-2",
						Entries: []acpsdk.PlanEntry{
							{Content: "Verify behavior", Status: acpsdk.PlanEntryStatusCompleted},
						},
					},
				},
			},
		},
	})
	if event.Type != types.EventTypeTodoUpdate {
		t.Fatalf("event.Type = %q, want %q", event.Type, types.EventTypeTodoUpdate)
	}
	todo, ok := event.Data.(types.TodoUpdate)
	if !ok {
		t.Fatalf("event.Data = %T, want TodoUpdate", event.Data)
	}
	if len(todo.Items) != 1 || todo.Items[0].Content != "Verify behavior" || todo.Items[0].Status != "completed" {
		t.Fatalf("todo = %#v", todo)
	}
}

func TestConvertEventSuppressesACPTodoWriteToolCards(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  acpsdk.SessionUpdate
		typ  UpdateType
	}{
		{
			name: "pending todowrite",
			typ:  UpdateTypeToolCall,
			raw: acpsdk.SessionUpdate{
				ToolCall: &acpsdk.SessionUpdateToolCall{
					ToolCallId: "call-1",
					Title:      "todowrite",
					Kind:       acpsdk.ToolKindOther,
					Status:     acpsdk.ToolCallStatusPending,
					RawInput:   map[string]any{},
				},
			},
		},
		{
			name: "complete todowrite",
			typ:  UpdateTypeToolUpdate,
			raw: acpsdk.SessionUpdate{
				ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
					ToolCallId: "call-1",
					Title:      acpsdk.Ptr("todowrite"),
					Kind:       acpsdk.Ptr(acpsdk.ToolKindOther),
					Status:     acpsdk.Ptr(acpsdk.ToolCallStatusCompleted),
					RawInput:   map[string]any{"todos": []any{map[string]any{"content": "Inspect", "status": "pending"}}},
				},
			},
		},
		{
			name: "summary todos",
			typ:  UpdateTypeToolUpdate,
			raw: acpsdk.SessionUpdate{
				ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
					ToolCallId: "call-1",
					Title:      acpsdk.Ptr("5 todos"),
					Kind:       acpsdk.Ptr(acpsdk.ToolKindOther),
					Status:     acpsdk.Ptr(acpsdk.ToolCallStatusCompleted),
					RawOutput:  map[string]any{"metadata": map[string]any{"todos": []any{map[string]any{"content": "Inspect", "status": "pending"}}}},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := convertEvent(SessionUpdate{
				Type:      tc.typ,
				SessionID: "session-1",
				Raw:       tc.raw,
			})
			if event.Type != "" {
				t.Fatalf("event = %#v, want suppressed empty event", event)
			}
		})
	}
}

func TestACPUsageUpdateCostAttributesTurnDelta(t *testing.T) {
	state := &sessionState{}
	state.observeUsageCost(&acpsdk.Cost{Amount: 0.25, Currency: "usd"}, true)
	state.observeUsageCost(&acpsdk.Cost{Amount: 0.4, Currency: "USD"}, true)

	extras := state.takeTurnExtras()
	if extras.cost < 0.399 || extras.cost > 0.401 {
		t.Fatalf("turn cost = %v, want 0.4", extras.cost)
	}
	if extras.currency != "USD" {
		t.Fatalf("currency = %q, want USD", extras.currency)
	}
	if next := state.takeTurnExtras(); next.cost != 0 || next.requests != 0 || next.durationMs != 0 {
		t.Fatalf("extras were not consumed: %#v", next)
	}
}

func TestACPUsageUpdateCostCounterResetAttributesFullAmount(t *testing.T) {
	state := &sessionState{}
	state.observeUsageCost(&acpsdk.Cost{Amount: 2, Currency: "USD"}, true)
	state.takeTurnExtras()
	state.observeUsageCost(&acpsdk.Cost{Amount: 0.3, Currency: "USD"}, true)

	if extras := state.takeTurnExtras(); extras.cost < 0.299 || extras.cost > 0.301 {
		t.Fatalf("reset delta = %v, want 0.3", extras.cost)
	}
}

func TestACPResumeCostBaselineIsNotAttributed(t *testing.T) {
	state := &sessionState{costBaselineOnly: true}
	state.observeUsageCost(&acpsdk.Cost{Amount: 12, Currency: "USD"}, true)
	if extras := state.takeTurnExtras(); extras.cost != 0 {
		t.Fatalf("history baseline leaked into turn cost: %#v", extras)
	}
	state.observeUsageCost(&acpsdk.Cost{Amount: 12.5, Currency: "USD"}, true)
	if extras := state.takeTurnExtras(); extras.cost < 0.499 || extras.cost > 0.501 {
		t.Fatalf("turn cost after resume = %v, want 0.5", extras.cost)
	}
}

func TestACPTurnUsageMetaAttachesRequestsDurationAndCost(t *testing.T) {
	state := &sessionState{}
	meta := map[string]any{
		"omp.turnUsage": map[string]any{
			"requests":   float64(3),
			"durationMs": 46515.10949999979,
			"cost":       0.02,
			"currency":   "usd",
		},
	}
	parsed, ok := turnUsageFromMeta(meta)
	if !ok {
		t.Fatal("turn usage meta was not parsed")
	}
	// Meta already carries the exact turn cost, so the cumulative snapshot is
	// only a baseline.
	state.observeUsageCost(&acpsdk.Cost{Amount: 9, Currency: "USD"}, false)
	state.addTurnUsageMeta(parsed)

	usage := attachTurnExtras(nil, state.takeTurnExtras())
	if usage == nil {
		t.Fatal("usage = nil, want requests/duration/cost")
	}
	if usage.Requests != 3 {
		t.Fatalf("requests = %d, want 3", usage.Requests)
	}
	if usage.DurationMs == nil || *usage.DurationMs != 46_515 {
		t.Fatalf("duration = %#v, want 46515", usage.DurationMs)
	}
	if usage.CostAmount == nil || *usage.CostAmount != 0.02 || usage.CostCurrency != "USD" {
		t.Fatalf("cost = %#v %q, want 0.02 USD", usage.CostAmount, usage.CostCurrency)
	}
}

func TestACPTurnUsageMetaParsesRealOMPUsageUpdate(t *testing.T) {
	// Captured from OMP 18.13.1: durationMs is fractional, so an int64 field
	// rejected the payload and silently dropped requests and cost.
	raw := []byte(`{"_meta":{"omp.turnUsage":{"cost":0.04251203749999999,"currency":"USD","durationMs":46515.10949999979,"requests":3}},"cost":{"amount":0.6292931224999998,"currency":"USD"},"sessionUpdate":"usage_update","size":1000000,"used":359694}`)
	var update acpsdk.SessionUpdate
	if err := json.Unmarshal(raw, &update); err != nil {
		t.Fatalf("decode usage update: %v", err)
	}
	if update.UsageUpdate == nil {
		t.Fatal("usage update variant was not decoded")
	}
	meta, ok := turnUsageFromMeta(update.UsageUpdate.Meta)
	if !ok {
		t.Fatal("omp.turnUsage was not parsed")
	}
	if meta.Requests != 3 {
		t.Fatalf("requests = %d, want 3", meta.Requests)
	}
	if meta.DurationMs < 46_515 || meta.DurationMs > 46_516 {
		t.Fatalf("duration = %v, want 46515.1", meta.DurationMs)
	}
	if meta.Cost < 0.0425 || meta.Cost > 0.0426 || meta.Currency != "USD" {
		t.Fatalf("cost = %v %q, want 0.0425 USD", meta.Cost, meta.Currency)
	}
}

func TestACPUsageNotificationFeedsTurnExtras(t *testing.T) {
	// Same payload as a live turn: it must reach turn extras through the
	// notification handler, not only through the meta helper.
	raw := []byte(`{"_meta":{"omp.turnUsage":{"cost":0.04251203749999999,"currency":"USD","durationMs":46515.10949999979,"requests":3}},"cost":{"amount":0.6292931224999998,"currency":"USD"},"sessionUpdate":"usage_update","size":1000000,"used":359694}`)
	var update acpsdk.SessionUpdate
	if err := json.Unmarshal(raw, &update); err != nil {
		t.Fatalf("decode usage update: %v", err)
	}
	state := &sessionState{}
	proc := &Process{sessionsByID: map[string]*sessionState{"session-1": state}}
	state.setOnUpdate(func(SessionUpdate) {})
	client := &mindfsClient{proc: proc}
	if err := client.SessionUpdate(context.Background(), acpsdk.SessionNotification{
		SessionId: "session-1",
		Update:    update,
	}); err != nil {
		t.Fatalf("handle usage update: %v", err)
	}

	usage := attachTurnExtras(&types.TokenUsage{InputTokens: 100, OutputTokens: 10}, state.takeTurnExtras())
	if usage.Requests != 3 {
		t.Fatalf("requests = %d, want 3", usage.Requests)
	}
	if usage.DurationMs == nil || *usage.DurationMs != 46_515 {
		t.Fatalf("duration = %#v, want 46515", usage.DurationMs)
	}
	if usage.CostAmount == nil || *usage.CostAmount < 0.0425 || *usage.CostAmount > 0.0426 {
		t.Fatalf("cost = %#v, want 0.0425", usage.CostAmount)
	}
	if usage.CostCurrency != "USD" {
		t.Fatalf("currency = %q, want USD", usage.CostCurrency)
	}
}

func TestACPTurnUsageMetaIgnoresUnknownShape(t *testing.T) {
	for _, meta := range []map[string]any{
		nil,
		{"omp.turnUsage": "unexpected"},
		{"omp.turnUsage": map[string]any{"requests": "three"}},
	} {
		if _, ok := turnUsageFromMeta(meta); ok {
			t.Fatalf("meta %#v should not parse", meta)
		}
	}
}
