package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/auth"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/summary"
)

const apiSummaryKey = "sk-api-summary-SECRET-987654321"

type summaryAPIFixture struct {
	*processingFixture
	summaries   *memory.SummaryRepository
	configs     *memory.ConfigurationRepository
	credentials *memory.CredentialRepository
	registry    *summary.Registry
}

func newSummaryAPIFixture(t *testing.T) *summaryAPIFixture {
	f := &summaryAPIFixture{processingFixture: newProcessingFixture(t), credentials: memory.NewCredentialRepository()}
	f.summaries = memory.NewSummaryRepository(f.jobs)
	defaults := application.DefaultConfiguration()
	defaults.Summary = domain.SummaryDefaults{Provider: "openai", Model: "gpt-test", OutputLanguage: "auto"}
	f.configs = memory.NewConfigurationRepository(defaults)
	_ = f.credentials.Set(context.Background(), "workspace-a", "openai", domain.NewCredentialSecret([]byte(apiSummaryKey)))
	f.registry = summary.NewRegistry([]summary.Spec{{Name: "openai", BaseURL: "https://provider.invalid/v1", CredentialRequired: true}}, summary.NewHTTPClient(), f.credentials)
	return f
}

func (f *summaryAPIFixture) handler(workspace string, summaries bool) http.Handler {
	processing := application.NewProcessingService(f.meetings, f.media, f.jobs, f.transcripts, queueStub{f.jobs}, true)
	options := []api.Option{api.WithMedia(f.service, f.signer), api.WithProcessing(processing)}
	if summaries {
		options = append(options, api.WithSummaries(application.NewSummaryService(f.meetings, f.media, f.jobs, f.transcripts, f.summaries,
			f.configs, f.credentials, f.registry)))
	}
	return api.NewHandler(application.NewMeetingService(f.meetings), application.NewConfigurationService(f.configs, f.credentials),
		application.NewCredentialService(f.credentials), auth.DevelopmentAuthenticator{}, application.NewDevelopmentResolver(domain.WorkspaceID(workspace)),
		"test", nil, options...)
}

func (f *summaryAPIFixture) transcript(t *testing.T, meetingID, assetID string) domain.Transcript {
	t.Helper()
	transcript := domain.Transcript{ID: domain.TranscriptID("trn_" + assetID), WorkspaceID: "workspace-a", MeetingID: domain.MeetingID(meetingID),
		MediaAssetID: domain.MediaAssetID(assetID), ASREngine: "whisper.cpp", ASRModel: "ggml-base.en", CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Segments: []domain.TranscriptSegment{{Sequence: 0, StartSeconds: 0, EndSeconds: 2, Text: "Ship it."}}}
	if err := f.transcripts.Replace(context.Background(), transcript); err != nil {
		t.Fatal(err)
	}
	return transcript
}

func summaryState(t *testing.T, handler http.Handler, meetingID string) map[string]any {
	t.Helper()
	response := send(t, handler, http.MethodGet, "/api/v1/meetings/"+meetingID+"/summary", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("get summary status=%d body=%s", response.Code, response.Body.String())
	}
	return jsonBody(t, response)
}

func TestSummaryGenerateStatesAndResult(t *testing.T) {
	f := newSummaryAPIFixture(t)
	handler := f.handler("workspace-a", true)
	meetingID, assetID := f.readyRecording(t, handler)
	ctx := context.Background()

	if got := summaryState(t, handler, meetingID); got["state"] != "none" || got["reason"] != "no_transcript" || got["summary"] != nil {
		t.Fatalf("before transcript = %v", got)
	}
	if response := send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary", nil, nil); response.Code != http.StatusConflict || reason(t, response) != "transcript_not_ready" {
		t.Fatalf("generate without transcript status=%d body=%s", response.Code, response.Body.String())
	}
	transcript := f.transcript(t, meetingID, assetID)

	// Empty body: workspace configuration is the source of truth.
	requested := send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary", nil, nil)
	if requested.Code != http.StatusAccepted {
		t.Fatalf("generate status=%d body=%s", requested.Code, requested.Body.String())
	}
	job := jsonBody(t, requested)["job"].(map[string]any)
	if job["kind"] != "summary" || job["status"] != "queued" {
		t.Fatalf("job = %v", job)
	}
	body := requested.Body.String()
	for _, leak := range []string{"parameters", "gpt-test", "transcriptId", "checkpoint", apiSummaryKey} {
		if strings.Contains(body, leak) {
			t.Fatalf("job response leaked %q: %s", leak, body)
		}
	}
	if again := send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary", nil, nil); again.Code != http.StatusConflict || reason(t, again) != "already_processing" {
		t.Fatalf("duplicate status=%d", again.Code)
	}
	if got := summaryState(t, handler, meetingID); got["state"] != "queued" || got["job"].(map[string]any)["id"] != job["id"] {
		t.Fatalf("queued = %v", got)
	}

	claimed, _ := f.jobs.Claim(ctx, []domain.ProcessingJobType{domain.JobSummary}, 1, time.Minute, time.Now().Add(time.Second))
	if got := summaryState(t, handler, meetingID); got["state"] != "generating" {
		t.Fatalf("generating = %v", got)
	}
	jobID, asset := claimed[0].ID, domain.MediaAssetID(assetID)
	input := 10
	stored := domain.Summary{ID: "sum_1", WorkspaceID: "workspace-a", MeetingID: domain.MeetingID(meetingID), TranscriptID: transcript.ID, MediaAssetID: &asset,
		ProcessingJobID: &jobID, Provider: "openai", Model: "gpt-test", TemplateID: "standard_meeting", CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Content: []byte(`{"format":"markdown","markdown":"**Summary**\n\nShip it.","title":"Launch","chunkCount":1,"reasoningStripped":false,"normalizationFallback":false}`),
		Usage:   &domain.SummaryUsage{InputTokens: &input, Requests: 1}}
	if err := f.summaries.Promote(ctx, stored, jobID, claimed[0].Attempts); err != nil {
		t.Fatal(err)
	}
	_ = f.jobs.Complete(ctx, jobID, claimed[0].Attempts, time.Now())
	ready := summaryState(t, handler, meetingID)
	if ready["state"] != "ready" || ready["stale"] != false || ready["error"] != nil || ready["currentTranscriptId"] != string(transcript.ID) {
		t.Fatalf("ready = %v", ready)
	}
	result := ready["summary"].(map[string]any)
	assertKeys(t, result, "id", "workspaceId", "meetingId", "transcriptId", "mediaAssetId", "processingJobId", "provider", "model", "templateId",
		"outputLanguage", "content", "createdAt", "updatedAt")
	if result["transcriptId"] != string(transcript.ID) || result["provider"] != "openai" || result["content"].(map[string]any)["markdown"] != "**Summary**\n\nShip it." {
		t.Fatalf("summary = %v", result)
	}

	// Regenerate with a template override; then cancel it. The old summary stays.
	requested = send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary", []byte(`{"templateId":"daily_standup","customPrompt":"Team context"}`), jsonHeaders)
	if requested.Code != http.StatusAccepted {
		t.Fatalf("regenerate status=%d body=%s", requested.Code, requested.Body.String())
	}
	if got := summaryState(t, handler, meetingID); got["state"] != "queued" || got["summary"].(map[string]any)["id"] != "sum_1" {
		t.Fatalf("regenerating = %v", got)
	}
	cancelled := send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary/cancel", nil, nil)
	if cancelled.Code != http.StatusOK || jsonBody(t, cancelled)["job"].(map[string]any)["status"] != "cancelled" {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	if again := send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary/cancel", nil, nil); again.Code != http.StatusConflict || reason(t, again) != "not_running" {
		t.Fatalf("second cancel status=%d", again.Code)
	}
	if got := summaryState(t, handler, meetingID); got["state"] != "cancelled" || got["summary"].(map[string]any)["id"] != "sum_1" {
		t.Fatalf("after cancel = %v", got)
	}

	// Retranscription: the summary is reported as stale, never silently current.
	f.transcripts.Replace(ctx, domain.Transcript{ID: "trn_new", WorkspaceID: "workspace-a", MeetingID: domain.MeetingID(meetingID), MediaAssetID: asset,
		ASREngine: "whisper.cpp", ASRModel: "m", CreatedAt: time.Now(), UpdatedAt: time.Now()})
	if got := summaryState(t, handler, meetingID); got["stale"] != true || got["currentTranscriptId"] != "trn_new" {
		t.Fatalf("after retranscription = %v", got)
	}
}

func TestSummaryFailurePayloadIsSafe(t *testing.T) {
	f := newSummaryAPIFixture(t)
	handler := f.handler("workspace-a", true)
	meetingID, assetID := f.readyRecording(t, handler)
	f.transcript(t, meetingID, assetID)
	send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary", nil, nil)
	claimed, _ := f.jobs.Claim(context.Background(), []domain.ProcessingJobType{domain.JobSummary}, 1, time.Minute, time.Now().Add(time.Second))
	internal := "summary provider auth (HTTP 401): Incorrect API key provided: Bearer " + apiSummaryKey
	_ = f.jobs.Fail(context.Background(), claimed[0].ID, claimed[0].Attempts, "provider_auth_failed", "The summary provider rejected the configured credentials.", internal, nil, time.Now())
	response := send(t, handler, http.MethodGet, "/api/v1/meetings/"+meetingID+"/summary", nil, nil)
	got := jsonBody(t, response)
	if got["state"] != "failed" || got["error"].(map[string]any)["code"] != "provider_auth_failed" {
		t.Fatalf("failed = %v", got)
	}
	for _, leak := range []string{apiSummaryKey, "Incorrect API key", "Bearer", "internal"} {
		if strings.Contains(response.Body.String(), leak) {
			t.Fatalf("summary response leaked %q: %s", leak, response.Body.String())
		}
	}
}

func TestSummaryIsolationAvailabilityAndTemplates(t *testing.T) {
	f := newSummaryAPIFixture(t)
	a := f.handler("workspace-a", true)
	b := f.handler("workspace-b", true)
	meetingID, assetID := f.readyRecording(t, a)
	f.transcript(t, meetingID, assetID)
	if response := send(t, a, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary", nil, nil); response.Code != http.StatusAccepted {
		t.Fatalf("generate status=%d", response.Code)
	}
	for _, response := range []*http.Response{
		send(t, b, http.MethodGet, "/api/v1/meetings/"+meetingID+"/summary", nil, nil).Result(),
		send(t, b, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary", nil, nil).Result(),
		send(t, b, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary/cancel", nil, nil).Result(),
	} {
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("cross-workspace status=%d", response.StatusCode)
		}
	}
	// Workspace B has no credential: its own meetings report why summaries cannot run.
	otherMeeting := createMeeting(t, b)
	if got := summaryState(t, b, otherMeeting); got["state"] != "unavailable" || got["reason"] != "credential_missing" {
		t.Fatalf("workspace b = %v", got)
	}

	if response := send(t, a, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary", []byte(`{"provider":"evil","endpoint":"http://attacker"}`), jsonHeaders); response.Code != http.StatusBadRequest {
		t.Fatalf("raw provider parameters accepted: %d", response.Code)
	}
	if response := send(t, a, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary", []byte(`{"templateId":"nope"}`), jsonHeaders); response.Code != http.StatusBadRequest || reason(t, response) != "templateId" {
		t.Fatalf("unknown template status=%d", response.Code)
	}

	capabilities := jsonBody(t, send(t, a, http.MethodGet, "/api/v1/capabilities", nil, nil))["summary"].(map[string]any)
	if capabilities["available"] != true || capabilities["providers"].([]any)[0] != "openai" {
		t.Fatalf("capabilities = %v", capabilities)
	}
	templates := jsonBody(t, send(t, a, http.MethodGet, "/api/v1/summary-templates", nil, nil))["items"].([]any)
	if len(templates) != len(summary.Templates()) {
		t.Fatalf("templates = %v", templates)
	}
	assertKeys(t, templates[0].(map[string]any), "id", "name", "description")

	off := f.handler("workspace-a", false)
	if got := summaryState(t, off, meetingID); got["state"] != "unavailable" || got["reason"] != "disabled" {
		t.Fatalf("disabled = %v", got)
	}
	if response := send(t, off, http.MethodPost, "/api/v1/meetings/"+meetingID+"/summary", nil, nil); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("generate when disabled status=%d", response.Code)
	}
	if got := jsonBody(t, send(t, off, http.MethodGet, "/api/v1/capabilities", nil, nil))["summary"].(map[string]any); got["available"] != false {
		t.Fatalf("disabled capabilities = %v", got)
	}
	// Credentials remain write-only: nothing exposes the decrypted key.
	for _, path := range []string{"/api/v1/credentials", "/api/v1/configuration", "/api/v1/meetings/" + meetingID + "/summary", "/api/v1/meetings/" + meetingID + "/jobs"} {
		if body := send(t, a, http.MethodGet, path, nil, nil).Body.String(); strings.Contains(body, apiSummaryKey) {
			t.Fatalf("%s exposed the credential", path)
		}
	}
}
