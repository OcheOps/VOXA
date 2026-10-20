package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/OcheOps/voxa/server/internal/application"
)

func TestCaptureProvenanceIsStoredAndLabelsRecordings(t *testing.T) {
	f := newMediaFixture(t, application.DefaultMediaLimits())
	handler := f.handler("workspace-a", true)
	meetingID := createMeeting(t, handler)

	created := send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/recordings",
		[]byte(`{"mimeType":"audio/webm;codecs=opus","capture":{"mode":"microphone_and_shared_audio","client":"web","sharedSurface":"browser"}}`), jsonHeaders)
	if created.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", created.Code, created.Body.String())
	}
	body := jsonBody(t, created)
	session := body["recordingSession"].(map[string]any)
	assertKeys(t, session["capture"].(map[string]any), "mode", "client", "sharedSurface")
	want := map[string]any{"mode": "microphone_and_shared_audio", "client": "web", "sharedSurface": "browser"}
	for key, value := range want {
		if session["capture"].(map[string]any)[key] != value || body["mediaAsset"].(map[string]any)["capture"].(map[string]any)[key] != value {
			t.Fatalf("created capture = %v / %v", session["capture"], body["mediaAsset"])
		}
	}
	assetID := body["mediaAsset"].(map[string]any)["id"].(string)
	microphoneOnly := send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/recordings",
		[]byte(`{"mimeType":"audio/webm;codecs=opus","capture":{"mode":"microphone","client":"web"}}`), jsonHeaders)
	if microphoneOnly.Code != http.StatusCreated {
		t.Fatalf("status=%d", microphoneOnly.Code)
	}

	// The meeting's recording list and single-asset reads carry the label.
	items := jsonBody(t, send(t, handler, http.MethodGet, "/api/v1/meetings/"+meetingID+"/media", nil, nil))["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["capture"].(map[string]any)["mode"] != "microphone_and_shared_audio" ||
		items[1].(map[string]any)["capture"].(map[string]any)["mode"] != "microphone" {
		t.Fatalf("items = %v", items)
	}
	got := jsonBody(t, send(t, handler, http.MethodGet, "/api/v1/media/"+assetID, nil, nil))["mediaAsset"].(map[string]any)
	if got["capture"].(map[string]any)["sharedSurface"] != "browser" {
		t.Fatalf("asset = %v", got)
	}
	// Provenance survives finalization (uploads and recovery never change it).
	putChunk(t, handler, assetID, 0, webmChunk(true, "audio"))
	if final := jsonBody(t, finalize(t, handler, assetID, 1))["mediaAsset"].(map[string]any); final["capture"].(map[string]any)["mode"] != "microphone_and_shared_audio" {
		t.Fatalf("finalized = %v", final)
	}
}

func TestCaptureProvenanceValidation(t *testing.T) {
	f := newMediaFixture(t, application.DefaultMediaLimits())
	handler := f.handler("workspace-a", true)
	meetingID := createMeeting(t, handler)
	for _, capture := range []string{
		`{"mode":"screen","client":"web"}`,
		`{"mode":"microphone","client":"browser-extension-v9"}`,
		`{"mode":"shared_audio","client":"web","sharedSurface":"https://meet.google.com/abc"}`,
		`{"mode":"microphone","client":"web","sharedSurface":"browser"}`,
		`{"mode":"microphone","client":"web","userAgent":"Mozilla/5.0"}`,
		`{"mode":"microphone","client":"web","tabTitle":"Weekly sync"}`,
	} {
		response := send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/recordings",
			[]byte(`{"mimeType":"audio/webm;codecs=opus","capture":`+capture+`}`), jsonHeaders)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d", capture, response.Code)
		}
		if strings.Contains(response.Body.String(), "meet.google.com") {
			t.Fatalf("error echoed the rejected value: %s", response.Body.String())
		}
	}
}
