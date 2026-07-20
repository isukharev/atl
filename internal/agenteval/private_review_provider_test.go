package agenteval

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrivateReviewProxyStripsCodexAndClaudeTools(t *testing.T) {
	tests := []struct {
		provider, path string
		request        map[string]any
		response       string
	}{
		{provider: "codex", path: "/v1/responses", request: map[string]any{
			"model": "review-model", "reasoning": map[string]any{"effort": "high"},
			"tools":       []any{map[string]any{"type": "function", "name": "unsafe"}},
			"tool_choice": "auto", "parallel_tool_calls": false, "client_metadata": map[string]any{"origin": "ambient"},
			"input": []any{map[string]any{"type": "additional_tools", "tools": []any{map[string]any{"name": "embedded"}}},
				map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "review"}}}},
		}, response: "data: {\"type\":\"response.completed\"}\n\n"},
		{provider: "claude-code", path: "/v1/messages", request: map[string]any{
			"model": "review-model", "output_config": map[string]any{"effort": "high"},
			"tools": []any{map[string]any{"name": "unsafe"}}, "tool_choice": map[string]any{"type": "auto"},
			"context_management": map[string]any{"edits": []any{}},
			"messages":           []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "review"}}}},
		}, response: "{\"type\":\"message\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}\n"},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			var upstreamCalls atomic.Int64
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				upstreamCalls.Add(1)
				data, err := io.ReadAll(request.Body)
				if err != nil {
					t.Error(err)
					return
				}
				var envelope map[string]any
				if json.Unmarshal(data, &envelope) != nil || countPrivateReviewTools(test.provider, envelope) != 0 ||
					envelope["tool_choice"] != nil || envelope["client_metadata"] != nil || envelope["context_management"] != nil {
					t.Errorf("forwarded envelope retained tools")
				}
				if test.provider == "codex" && envelope["parallel_tool_calls"] != false {
					t.Errorf("Codex false parallel-tool requirement drifted")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.response)
			}))
			defer upstream.Close()
			oldClient := privateReviewHTTPClient
			privateReviewHTTPClient = upstream.Client()
			defer func() { privateReviewHTTPClient = oldClient }()
			proxy, listener, server, err := startPrivateReviewProxy(test.provider, "review-model", "high", upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			defer server.Close()
			data, _ := json.Marshal(test.request)
			request, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+test.path, bytes.NewReader(data))
			request.Header.Set("Authorization", "Bearer synthetic")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			observation := proxy.Observation()
			if response.StatusCode != http.StatusOK || upstreamCalls.Load() != 1 || observation.ModelRequests != 1 ||
				observation.InputTools != 2 && test.provider == "codex" || observation.InputTools != 1 && test.provider == "claude-code" ||
				observation.ForwardedTools != 0 || observation.ToolOutputs != 0 || observation.Unexpected || !observation.AuthenticationSeen {
				t.Fatalf("status=%d calls=%d observation=%+v", response.StatusCode, upstreamCalls.Load(), observation)
			}
		})
	}
}

func TestPrivateReviewProxyRejectsToolOutputAndSecondRequest(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\"}}\n\n")
	}))
	defer upstream.Close()
	oldClient := privateReviewHTTPClient
	privateReviewHTTPClient = upstream.Client()
	defer func() { privateReviewHTTPClient = oldClient }()
	proxy, listener, server, err := startPrivateReviewProxy("codex", "review-model", "high", upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	var aborts atomic.Int64
	proxy.abort = func() { aborts.Add(1) }
	defer listener.Close()
	defer server.Close()
	body := []byte(`{"model":"review-model","reasoning":{"effort":"high"},"parallel_tool_calls":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"review"}]}]}`)
	call := func() int {
		request, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/v1/responses", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer synthetic")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if first, second := call(), call(); first != http.StatusBadGateway || second != http.StatusBadRequest {
		t.Fatalf("statuses=%d,%d", first, second)
	}
	observation := proxy.Observation()
	if observation.ModelRequests != 1 || observation.ToolOutputs != 1 || !observation.Unexpected || aborts.Load() != 2 {
		t.Fatalf("observation=%+v aborts=%d", observation, aborts.Load())
	}
}

func TestPrivateReviewProxyInspectsGzipToolOutput(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/event-stream")
		writer := gzip.NewWriter(w)
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\"}}\n\n")
		_ = writer.Close()
	}))
	defer upstream.Close()
	oldClient := privateReviewHTTPClient
	privateReviewHTTPClient = upstream.Client()
	defer func() { privateReviewHTTPClient = oldClient }()
	proxy, listener, server, err := startPrivateReviewProxy("codex", "review-model", "high", upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer server.Close()
	body := []byte(`{"model":"review-model","reasoning":{"effort":"high"},"parallel_tool_calls":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"review"}]}]}`)
	request, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/v1/responses", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer synthetic")
	request.Header.Set("Accept-Encoding", "gzip")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || proxy.Observation().ToolOutputs != 1 || !proxy.Observation().Unexpected {
		t.Fatalf("status=%d observation=%+v", response.StatusCode, proxy.Observation())
	}
}

func TestPrivateReviewProxyRejectsCapabilityAndIdentityDrift(t *testing.T) {
	tests := []struct {
		name, provider, path string
		request              map[string]any
	}{
		{name: "Codex capability", provider: "codex", path: "/v1/responses", request: map[string]any{
			"model": "review-model", "reasoning": map[string]any{"effort": "high"}, "mcp_servers": []any{},
			"parallel_tool_calls": false, "input": []any{map[string]any{"type": "message"}},
		}},
		{name: "Claude capability", provider: "claude-code", path: "/v1/messages", request: map[string]any{
			"model": "review-model", "output_config": map[string]any{"effort": "high"}, "mcp_servers": []any{},
			"messages": []any{map[string]any{"role": "user", "content": "review"}},
		}},
		{name: "model", provider: "codex", path: "/v1/responses", request: map[string]any{
			"model": "drifted", "reasoning": map[string]any{"effort": "high"}, "parallel_tool_calls": false,
			"input": []any{map[string]any{"type": "message"}},
		}},
		{name: "Codex missing parallel setting", provider: "codex", path: "/v1/responses", request: map[string]any{
			"model": "review-model", "reasoning": map[string]any{"effort": "high"}, "input": []any{map[string]any{"type": "message"}},
		}},
		{name: "Codex parallel enabled", provider: "codex", path: "/v1/responses", request: map[string]any{
			"model": "review-model", "reasoning": map[string]any{"effort": "high"}, "parallel_tool_calls": true,
			"input": []any{map[string]any{"type": "message"}},
		}},
		{name: "reasoning", provider: "claude-code", path: "/v1/messages", request: map[string]any{
			"model": "review-model", "output_config": map[string]any{"effort": "low"},
			"messages": []any{map[string]any{"role": "user", "content": "review"}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var upstreamCalls atomic.Int64
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { upstreamCalls.Add(1) }))
			defer upstream.Close()
			oldClient := privateReviewHTTPClient
			privateReviewHTTPClient = upstream.Client()
			defer func() { privateReviewHTTPClient = oldClient }()
			proxy, listener, server, err := startPrivateReviewProxy(test.provider, "review-model", "high", upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			defer server.Close()
			data, _ := json.Marshal(test.request)
			request, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+test.path, bytes.NewReader(data))
			request.Header.Set("Authorization", "Bearer synthetic")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusBadRequest || upstreamCalls.Load() != 0 || !proxy.Observation().Unexpected {
				t.Fatalf("status=%d calls=%d observation=%+v", response.StatusCode, upstreamCalls.Load(), proxy.Observation())
			}
		})
	}
}

func TestPrivateReviewProxyRejectsProviderRedirect(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		upstreamCalls.Add(1)
		http.Redirect(w, request, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	oldClient := privateReviewHTTPClient
	privateReviewHTTPClient = &http.Client{Transport: upstream.Client().Transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("synthetic redirect rejection")
	}}
	defer func() { privateReviewHTTPClient = oldClient }()
	_, listener, server, err := startPrivateReviewProxy("codex", "review-model", "high", upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer server.Close()
	body := []byte(`{"model":"review-model","reasoning":{"effort":"high"},"parallel_tool_calls":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"review"}]}]}`)
	request, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/v1/responses", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer synthetic")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || upstreamCalls.Load() != 1 {
		t.Fatalf("status=%d calls=%d", response.StatusCode, upstreamCalls.Load())
	}
}

func TestPrivateReviewResponseUsesProviderSpecificTypeAllowlist(t *testing.T) {
	for _, kind := range []string{"function_call", "file_search_call", "code_interpreter_call", "image_generation_call", "apply_patch_call", "shell_call", "server_tool_result"} {
		data := []byte(`{"type":"response.output_item.added","item":{"type":"` + kind + `"}}`)
		if count, err := inspectPrivateReviewResponse("codex", data); err != nil || count != 1 {
			t.Fatalf("kind=%s count=%d err=%v", kind, count, err)
		}
	}
	if count, err := inspectPrivateReviewResponse("codex", []byte(`{"type":"response.output_item.added","item":{"type":"future_quantum_call"}}`)); err == nil || count != 0 {
		t.Fatalf("future type count=%d err=%v", count, err)
	}
	if count, err := inspectPrivateReviewResponse("claude-code", []byte(`{"type":"message","content":[{"type":"text","text":"ok"}]}`)); err != nil || count != 0 {
		t.Fatalf("Claude text count=%d err=%v", count, err)
	}
	benignCodex := []byte("data: {\"type\":\"response.created\",\"response\":{\"text\":{\"format\":{\"type\":\"json_schema\",\"schema\":{\"type\":\"object\"}}}}}\n\n" +
		"data: {\"type\":\"response.metadata\"}\n\n")
	if count, err := inspectPrivateReviewResponse("codex", benignCodex); err != nil || count != 0 {
		t.Fatalf("Codex schema metadata count=%d err=%v", count, err)
	}
}

func TestPrivateReviewReceiptRejectsWrappedCost(t *testing.T) {
	digest := strings.Repeat("a", 64)
	execution := PrivateReviewerExecution{ReviewerID: "reviewer-01", Reasoning: "high", TimeoutSeconds: 30,
		Pricing:                  Pricing{InputMicroUSDPerMillionTokens: math.MaxInt64 / 2, OutputMicroUSDPerMillionTokens: math.MaxInt64 / 2},
		MaxEstimatedCostMicroUSD: 100_000_000}
	cost, err := estimateCost(2, 2, execution.Pricing)
	if err != nil || cost <= execution.MaxEstimatedCostMicroUSD {
		t.Fatalf("cost=%d err=%v", cost, err)
	}
	receipt := privateReviewReceipt{SchemaVersion: privateReviewReceiptSchemaVersion, PlanSHA256: digest, PanelContractSHA256: digest,
		ReviewerID: "reviewer-01", ReviewerKind: "codex", ReviewerModel: "review-model", ReviewerExecutionSHA256: digest,
		AgentIdentity: "binary-sha256:" + digest, Status: "succeeded", ModelRequests: 1, InputTokens: 2, OutputTokens: 2,
		EstimatedCostMicroUSD: 0, CostKnown: true, ReviewSHA256: digest, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if _, err := encodePrivateReviewReceipt(receipt, execution); err == nil {
		t.Fatal("wrapped zero-cost receipt was accepted")
	}
}

func TestPrivateClaudeReviewArgsDisableToolsWithoutStructuredOutputTool(t *testing.T) {
	args := privateClaudeReviewArgs(Reviewer{Kind: "claude-code", Model: "review-model"}, PrivateReviewerExecution{
		Reasoning: "high", MaxEstimatedCostMicroUSD: 10_000,
	})
	joined := strings.Join(args, "\x00")
	for _, required := range []string{"--safe-mode", "--tools\x00", "--allowed-tools\x00", "--prompt-suggestions\x00false"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %q in %q", required, joined)
		}
	}
	if strings.Contains(joined, "--json-schema") {
		t.Fatalf("Claude structured output would introduce a synthetic tool: %q", joined)
	}
}

func TestClaudeReviewRuntimeProjectsOnlyUnexpiredAccessToken(t *testing.T) {
	root := t.TempDir()
	config := t.TempDir()
	if err := os.Chmod(config, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialData, err := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "synthetic-access-token", "refreshToken": "must-not-be-projected",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, ".credentials.json"), credentialData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireOwnerOnly("test Claude config", config, true); err != nil {
		t.Fatal(err)
	}
	runtime, err := newClaudeReviewRuntime(root, filepath.Join(root, "scratch"), []string{
		"PATH=/synthetic/bin", "CLAUDE_CONFIG_DIR=" + config,
	})
	if err != nil {
		t.Fatal(err)
	}
	environment := runtime.Environment()
	if environment["CLAUDE_CODE_OAUTH_TOKEN"] != "synthetic-access-token" || environment["ANTHROPIC_AUTH_TOKEN"] != "" ||
		environment["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] != "1" {
		t.Fatalf("environment=%+v", environment)
	}
	if _, err := os.Stat(filepath.Join(environment["CLAUDE_CONFIG_DIR"], ".credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("ambient credential file was copied: %v", err)
	}
	if err := runtime.Close(); err != nil || len(runtime.environment) != 0 {
		t.Fatalf("close err=%v environment=%+v", err, runtime.environment)
	}
}

func TestClaudeReviewRuntimeRejectsExpiredCredential(t *testing.T) {
	root := t.TempDir()
	config := t.TempDir()
	if err := os.Chmod(config, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialData, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "expired", "expiresAt": time.Now().Add(-time.Minute).UnixMilli(),
	}})
	if err := os.WriteFile(filepath.Join(config, ".credentials.json"), credentialData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newClaudeReviewRuntime(root, filepath.Join(root, "scratch"), []string{"CLAUDE_CONFIG_DIR=" + config}); err == nil {
		t.Fatal("expired Claude credential was accepted")
	}
}

func TestDecodePrivateReviewCandidateAllowsOnlyExactJSONFence(t *testing.T) {
	digest := strings.Repeat("a", 64)
	review := Review{SchemaVersion: ReviewSchemaVersion, RubricID: "rubric", RubricSHA256: digest,
		ScenarioID: "scenario", ResultSHA256: digest, FinalResponseSHA256: digest,
		Reviewer: Reviewer{ID: "reviewer-01", Kind: "claude-code", Model: "review-model"},
		Criteria: []ReviewCriterionScore{{ID: "correctness", Score: 1}}, FindingIDs: []string{}}
	data, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range [][]byte{data, append(append([]byte("```json\n"), data...), []byte("\n```\n")...)} {
		decoded, err := decodePrivateReviewCandidate(candidate)
		if err != nil || decoded.Reviewer != review.Reviewer {
			t.Fatalf("decoded=%+v err=%v", decoded, err)
		}
	}
	if _, err := decodePrivateReviewCandidate(append(append([]byte("result:\n```json\n"), data...), []byte("\n```\n")...)); err == nil {
		t.Fatal("accepted prose around fenced JSON")
	}
}

func TestAutomatedPrivateReviewIsReceiptedTerminalAndAssessable(t *testing.T) {
	fixture, panel, preview, packets := newExecutablePrivateReviewFixture(t)
	oldRunner := privateReviewRunProvider
	var calls atomic.Int64
	privateReviewRunProvider = func(_ context.Context, root, packet, _ string, reviewer Reviewer, _ PrivateReviewerExecution,
		_ []byte, _, _ []byte, rubric Rubric,
	) (privateReviewProviderResult, error) {
		calls.Add(1)
		templateData, err := os.ReadFile(filepath.Join(packet, "review.json"))
		if err != nil {
			return privateReviewProviderResult{}, err
		}
		template, err := DecodeReview(bytes.NewReader(templateData))
		if err != nil || template.Reviewer != reviewer {
			return privateReviewProviderResult{}, errors.New("template")
		}
		for index := range template.Criteria {
			template.Criteria[index].Score = rubric.Criteria[index].Maximum
		}
		encoded, _ := json.Marshal(template)
		review, err := writeCompletedPrivateReview(root, packet, templateWithZeroScores(template), rubric, encoded)
		if err != nil {
			return privateReviewProviderResult{}, err
		}
		return privateReviewProviderResult{Review: review, AgentIdentity: "binary-sha256:" + strings.Repeat("a", 64),
			ModelRequests: 1, InputTools: 4, InputTokens: 10, OutputTokens: 5, CostKnown: true, EstimatedCost: 1}, nil
	}
	defer func() { privateReviewRunProvider = oldRunner }()
	for index, reviewer := range panel.Reviewers {
		summary, err := RunPrivateReview(context.Background(), PrivateReviewRunOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
			PlanID: preview.PlanID, ExpectedPlanSHA256: preview.PlanSHA256, Surface: SurfaceATLMCP,
			ReviewerID: reviewer.ID, AgentBinary: fixture.agent, Confirm: PrivateReviewRunConfirmation, Now: fixture.now})
		if err != nil || summary.Status != "succeeded" || summary.ProviderRequests != 1 || summary.ForwardedTools != 0 || !summary.CostKnown {
			t.Fatalf("run %d summary=%+v err=%v", index, summary, err)
		}
		if index == 0 {
			if _, err := RunPrivateReview(context.Background(), PrivateReviewRunOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
				PlanID: preview.PlanID, ExpectedPlanSHA256: preview.PlanSHA256, Surface: SurfaceATLMCP,
				ReviewerID: reviewer.ID, AgentBinary: fixture.agent, Confirm: PrivateReviewRunConfirmation}); err == nil || !strings.Contains(err.Error(), "review_run_consumed") {
				t.Fatalf("replay err=%v", err)
			}
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
	for index, packet := range packets {
		summary, err := AssessPrivateReview(PrivateReviewAssessOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
			PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: packet.ReviewerID})
		if err != nil {
			t.Fatal(err)
		}
		if index == len(packets)-1 && summary.Status != "assessed" {
			t.Fatalf("summary=%+v", summary)
		}
	}
}

func TestAutomatedPrivateReviewFailureCannotReplayOrAssess(t *testing.T) {
	fixture, panel, preview, packets := newExecutablePrivateReviewFixture(t)
	oldRunner := privateReviewRunProvider
	privateReviewRunProvider = func(context.Context, string, string, string, Reviewer, PrivateReviewerExecution, []byte, []byte, []byte, Rubric) (privateReviewProviderResult, error) {
		return privateReviewProviderResult{AgentIdentity: "binary-sha256:" + strings.Repeat("b", 64), ModelRequests: 1}, errors.New("synthetic terminal failure")
	}
	defer func() { privateReviewRunProvider = oldRunner }()
	options := PrivateReviewRunOptions{Root: fixture.root, RepositoryRoot: fixture.repository, PlanID: preview.PlanID,
		ExpectedPlanSHA256: preview.PlanSHA256, Surface: SurfaceATLMCP, ReviewerID: panel.Reviewers[0].ID,
		AgentBinary: fixture.agent, Confirm: PrivateReviewRunConfirmation, Now: fixture.now}
	summary, err := RunPrivateReview(context.Background(), options)
	if err == nil || summary.Status != "terminal-failed" || summary.CostKnown {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if _, err := RunPrivateReview(context.Background(), options); err == nil || !strings.Contains(err.Error(), "review_cost_unknown") && !strings.Contains(err.Error(), "review_run_consumed") {
		t.Fatalf("replay err=%v", err)
	}
	if _, err := AssessPrivateReview(PrivateReviewAssessOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: packets[0].ReviewerID}); err == nil || !strings.Contains(err.Error(), "review_receipt") {
		t.Fatalf("assessment err=%v", err)
	}
}

func TestAutomatedPrivateReviewRejectsPrefilledTemplateBeforeAttempt(t *testing.T) {
	fixture, panel, preview, packets := newExecutablePrivateReviewFixture(t)
	path := filepath.Join(fixture.root, filepath.FromSlash(packets[0].Packet), "review.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	review, err := DecodeReview(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	review.Criteria[0].Score = 1
	data, _ = json.MarshalIndent(review, "", "  ")
	if err := writePrivateFile(path, append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	_, err = RunPrivateReview(context.Background(), PrivateReviewRunOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, ExpectedPlanSHA256: preview.PlanSHA256, Surface: SurfaceATLMCP,
		ReviewerID: panel.Reviewers[0].ID, AgentBinary: fixture.agent, Confirm: PrivateReviewRunConfirmation})
	if err == nil || !strings.Contains(err.Error(), "review_template_drift") {
		t.Fatalf("prefilled template err=%v", err)
	}
	packet := filepath.Join(fixture.root, filepath.FromSlash(packets[0].Packet))
	if _, statErr := os.Stat(filepath.Join(packet, "execution-attempt.json")); !os.IsNotExist(statErr) {
		t.Fatalf("attempt was consumed before pristine-template check: %v", statErr)
	}
}

func TestAutomatedPrivateReviewReportsUnreceiptedTerminalFailure(t *testing.T) {
	fixture, panel, preview, packets := newExecutablePrivateReviewFixture(t)
	oldRunner := privateReviewRunProvider
	oldCommit := privateReviewCommitReceipt
	privateReviewRunProvider = func(_ context.Context, root, packet, _ string, _ Reviewer, _ PrivateReviewerExecution,
		_ []byte, _, _ []byte, rubric Rubric,
	) (privateReviewProviderResult, error) {
		templateData, err := os.ReadFile(filepath.Join(packet, "review.json"))
		if err != nil {
			return privateReviewProviderResult{}, err
		}
		review, err := DecodeReview(bytes.NewReader(templateData))
		if err != nil {
			return privateReviewProviderResult{}, err
		}
		for index := range review.Criteria {
			review.Criteria[index].Score = rubric.Criteria[index].Maximum
		}
		encoded, _ := json.Marshal(review)
		completed, err := writeCompletedPrivateReview(root, packet, templateWithZeroScores(review), rubric, encoded)
		return privateReviewProviderResult{Review: completed, AgentIdentity: "binary-sha256:" + strings.Repeat("c", 64),
			ModelRequests: 1, InputTokens: 10, OutputTokens: 5, CostKnown: true, EstimatedCost: 1}, err
	}
	privateReviewCommitReceipt = func(string, string, []byte) error { return errors.New("synthetic receipt failure") }
	defer func() {
		privateReviewRunProvider = oldRunner
		privateReviewCommitReceipt = oldCommit
	}()
	summary, err := RunPrivateReview(context.Background(), PrivateReviewRunOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
		PlanID: preview.PlanID, ExpectedPlanSHA256: preview.PlanSHA256, Surface: SurfaceATLMCP,
		ReviewerID: panel.Reviewers[0].ID, AgentBinary: fixture.agent, Confirm: PrivateReviewRunConfirmation})
	if err == nil || summary.Status != "terminal-unreceipted" {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	packet := filepath.Join(fixture.root, filepath.FromSlash(packets[0].Packet))
	if _, statErr := os.Stat(filepath.Join(packet, "execution-attempt.json")); statErr != nil {
		t.Fatalf("terminal attempt missing: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(packet, "execution-receipt.json")); !os.IsNotExist(statErr) {
		t.Fatalf("unexpected receipt: %v", statErr)
	}
}

func newExecutablePrivateReviewFixture(t *testing.T) (privatePlanTestFixture, PrivateQualitativeReviewPanel, PrivatePlanPreview, []PrivateReviewSummary) {
	t.Helper()
	fixture := newPrivatePlanTestFixture(t, false, false)
	panel := privateReviewTestPanel()
	panel.Reviewers[1].Kind = "claude-code"
	for _, reviewer := range panel.Reviewers {
		panel.Executions = append(panel.Executions, PrivateReviewerExecution{ReviewerID: reviewer.ID, Reasoning: "high", TimeoutSeconds: 30,
			Pricing: Pricing{InputMicroUSDPerMillionTokens: 1, OutputMicroUSDPerMillionTokens: 1}, MaxEstimatedCostMicroUSD: 10})
	}
	manifestPath := filepath.Join(fixture.root, PrivateWorkspaceManifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	manifest.RunSets[0].QualitativeReviewRequired = false
	manifest.RunSets[0].QualitativeReviewPanel = &panel
	manifest.RunSets[0].ReviewerReserveMicroUSD = 30
	data, err = EncodePrivateWorkspaceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(manifestPath, data); err != nil {
		t.Fatal(err)
	}
	preview := fixture.createPlan(t)
	if _, err := ExecutePrivatePlan(context.Background(), fixture.executeOptions(preview)); err != nil {
		t.Fatal(err)
	}
	packets := make([]PrivateReviewSummary, 0, len(panel.Reviewers))
	for _, reviewer := range panel.Reviewers {
		packet, err := PreparePrivateReview(PrivateReviewPrepareOptions{Root: fixture.root, RepositoryRoot: fixture.repository,
			PlanID: preview.PlanID, Surface: SurfaceATLMCP, ReviewerID: reviewer.ID})
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, packet)
	}
	return fixture, panel, preview, packets
}

func templateWithZeroScores(review Review) Review {
	for index := range review.Criteria {
		review.Criteria[index].Score = 0
	}
	review.FindingIDs = nil
	return review
}
