package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/auth"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/mediastore"
)

// webmChunk returns bytes starting with the EBML magic when first is set.
func webmChunk(first bool, body string) []byte {
	if first {
		return append([]byte{0x1a, 0x45, 0xdf, 0xa3}, []byte(body)...)
	}
	return []byte(body)
}

type mediaFixture struct {
	t        *testing.T
	root     string
	meetings *memory.MeetingRepository
	media    *memory.MediaRepository
	store    *mediastore.Filesystem
	service  *application.MediaService
	signer   *api.PlaybackSigner
	limits   application.MediaLimits
}

func newMediaFixture(t *testing.T, limits application.MediaLimits) *mediaFixture {
	t.Helper()
	root := t.TempDir()
	store, err := mediastore.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := api.NewPlaybackSigner(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f := &mediaFixture{t: t, root: root, meetings: memory.NewMeetingRepository(), media: memory.NewMediaRepository(), store: store, signer: signer, limits: limits}
	f.service = application.NewMediaService(f.meetings, f.media, store, limits)
	return f
}

// handler builds a development-auth handler for one workspace sharing the fixture's storage.
func (f *mediaFixture) handler(workspace string, withMedia bool) http.Handler {
	credentials := memory.NewCredentialRepository()
	var options []api.Option
	if withMedia {
		options = append(options, api.WithMedia(f.service, f.signer))
	}
	return api.NewHandler(
		application.NewMeetingService(f.meetings),
		application.NewConfigurationService(memory.NewConfigurationRepository(application.DefaultConfiguration()), credentials),
		application.NewCredentialService(credentials),
		auth.DevelopmentAuthenticator{}, application.NewDevelopmentResolver(domain.WorkspaceID(workspace)), "test", nil, options...)
}

func send(t *testing.T, handler http.Handler, method, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, r)
	return recorder
}

func jsonBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return value
}

var jsonHeaders = map[string]string{"Content-Type": "application/json"}
var octetHeaders = map[string]string{"Content-Type": "application/octet-stream"}

func createMeeting(t *testing.T, handler http.Handler) string {
	t.Helper()
	response := send(t, handler, http.MethodPost, "/api/v1/meetings", []byte(`{"title":"Standup"}`), jsonHeaders)
	return jsonBody(t, response)["meeting"].(map[string]any)["id"].(string)
}

func startRecording(t *testing.T, handler http.Handler, meetingID string) string {
	t.Helper()
	response := send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/recordings", []byte(`{"mimeType":"audio/webm;codecs=opus"}`), jsonHeaders)
	if response.Code != http.StatusCreated {
		t.Fatalf("create recording status=%d body=%s", response.Code, response.Body.String())
	}
	return jsonBody(t, response)["mediaAsset"].(map[string]any)["id"].(string)
}

func putChunk(t *testing.T, handler http.Handler, assetID string, index int, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	return send(t, handler, http.MethodPut, fmt.Sprintf("/api/v1/media/%s/chunks/%d", assetID, index), data, octetHeaders)
}

func finalize(t *testing.T, handler http.Handler, assetID string, count int) *httptest.ResponseRecorder {
	t.Helper()
	return send(t, handler, http.MethodPost, "/api/v1/media/"+assetID+"/finalize", []byte(fmt.Sprintf(`{"chunkCount":%d,"durationSeconds":42.5}`, count)), jsonHeaders)
}

func reason(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	// Validation errors identify a field; state conflicts carry a reason code.
	details, _ := jsonBody(t, recorder)["error"].(map[string]any)["details"].(map[string]any)
	if field, ok := details["field"].(string); ok {
		return field
	}
	value, _ := details["reason"].(string)
	return value
}

func TestRecordingUploadLifecycle(t *testing.T) {
	f := newMediaFixture(t, application.DefaultMediaLimits())
	handler := f.handler("workspace-a", true)
	meetingID := createMeeting(t, handler)

	created := send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/recordings", []byte(`{"mimeType":"Audio/WebM; codecs=opus"}`), jsonHeaders)
	if created.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", created.Code, created.Body.String())
	}
	body := jsonBody(t, created)
	assertKeys(t, body, "recordingSession", "mediaAsset", "upload")
	asset := body["mediaAsset"].(map[string]any)
	assertKeys(t, asset, "id", "workspaceId", "meetingId", "recordingSessionId", "kind", "mimeType", "status", "durationSeconds", "sizeBytes", "createdAt", "updatedAt")
	if asset["status"] != "uploading" || asset["mimeType"] != "audio/webm;codecs=opus" || body["recordingSession"].(map[string]any)["status"] != "recording" {
		t.Fatalf("created = %v", body)
	}
	assetID := asset["id"].(string)
	chunks := [][]byte{webmChunk(true, "header+cluster0|"), webmChunk(false, "cluster1|"), webmChunk(false, "cluster2")}

	// Out of order, with a duplicate retry.
	for _, index := range []int{2, 0, 0} {
		if response := putChunk(t, handler, assetID, index, chunks[index]); response.Code != http.StatusOK {
			t.Fatalf("chunk %d status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
	if response := putChunk(t, handler, assetID, 0, webmChunk(true, "different")); response.Code != http.StatusConflict || reason(t, response) != "chunk_mismatch" {
		t.Fatalf("conflicting retry status=%d body=%s", response.Code, response.Body.String())
	}
	status := jsonBody(t, send(t, handler, http.MethodGet, "/api/v1/media/"+assetID+"/upload", nil, nil))["upload"].(map[string]any)
	if fmt.Sprint(status["receivedChunks"]) != "[0 2]" {
		t.Fatalf("upload status = %v", status)
	}
	missing := finalize(t, handler, assetID, 3)
	if missing.Code != http.StatusConflict || reason(t, missing) != "missing_chunks" ||
		fmt.Sprint(jsonBody(t, missing)["error"].(map[string]any)["details"].(map[string]any)["missing"]) != "[1]" {
		t.Fatalf("finalize with gap status=%d body=%s", missing.Code, missing.Body.String())
	}
	putChunk(t, handler, assetID, 1, chunks[1])
	if response := finalize(t, handler, assetID, 2); response.Code != http.StatusConflict || reason(t, response) != "unexpected_chunks" {
		t.Fatalf("finalize ignoring a chunk status=%d body=%s", response.Code, response.Body.String())
	}

	ready := finalize(t, handler, assetID, 3)
	if ready.Code != http.StatusOK {
		t.Fatalf("finalize status=%d body=%s", ready.Code, ready.Body.String())
	}
	readyAsset := jsonBody(t, ready)["mediaAsset"].(map[string]any)
	whole := bytes.Join(chunks, nil)
	if readyAsset["status"] != "ready" || readyAsset["sizeBytes"] != float64(len(whole)) || readyAsset["durationSeconds"] != 42.5 {
		t.Fatalf("ready asset = %v", readyAsset)
	}
	// Finalize is idempotent; a different count or further chunks are refused.
	if response := finalize(t, handler, assetID, 3); response.Code != http.StatusOK {
		t.Fatalf("repeat finalize status=%d", response.Code)
	}
	if response := finalize(t, handler, assetID, 4); response.Code != http.StatusConflict {
		t.Fatalf("finalize with other count status=%d", response.Code)
	}
	if response := putChunk(t, handler, assetID, 3, []byte("late")); response.Code != http.StatusConflict || reason(t, response) != "upload_closed" {
		t.Fatalf("late chunk status=%d body=%s", response.Code, response.Body.String())
	}

	content := send(t, handler, http.MethodGet, "/api/v1/media/"+assetID+"/content", nil, nil)
	if content.Code != http.StatusOK || !bytes.Equal(content.Body.Bytes(), whole) || content.Header().Get("Content-Type") != "audio/webm;codecs=opus" || content.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("content status=%d headers=%v", content.Code, content.Header())
	}
	list := jsonBody(t, send(t, handler, http.MethodGet, "/api/v1/meetings/"+meetingID+"/media", nil, nil))["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["status"] != "ready" {
		t.Fatalf("meeting media = %v", list)
	}
	// Staged chunks are gone after finalize; no storage locators leak.
	if staged, _ := os.ReadDir(filepath.Join(f.root, "staging")); len(staged) != 0 {
		t.Fatalf("staging left behind: %v", staged)
	}
	for _, response := range []*httptest.ResponseRecorder{created, ready} {
		if strings.Contains(response.Body.String(), "obj_") || strings.Contains(response.Body.String(), f.root) {
			t.Fatalf("storage locator leaked: %s", response.Body.String())
		}
	}
}

func TestRangeRequestsAndSignedPlayback(t *testing.T) {
	f := newMediaFixture(t, application.DefaultMediaLimits())
	handler := f.handler("workspace-a", true)
	assetID := startRecording(t, handler, createMeeting(t, handler))
	data := webmChunk(true, "0123456789abcdef")
	putChunk(t, handler, assetID, 0, data)
	finalize(t, handler, assetID, 1)

	cases := []struct {
		header, want, contentRange string
		status                     int
	}{
		{"bytes=4-7", string(data[4:8]), fmt.Sprintf("bytes 4-7/%d", len(data)), http.StatusPartialContent},
		{"bytes=-3", string(data[len(data)-3:]), fmt.Sprintf("bytes %d-%d/%d", len(data)-3, len(data)-1, len(data)), http.StatusPartialContent},
		{"bytes=10-", string(data[10:]), fmt.Sprintf("bytes 10-%d/%d", len(data)-1, len(data)), http.StatusPartialContent},
	}
	for _, tc := range cases {
		response := send(t, handler, http.MethodGet, "/api/v1/media/"+assetID+"/content", nil, map[string]string{"Range": tc.header})
		if response.Code != tc.status || response.Body.String() != tc.want || response.Header().Get("Content-Range") != tc.contentRange ||
			response.Header().Get("Content-Length") != fmt.Sprint(len(tc.want)) {
			t.Fatalf("range %s: status=%d body=%q headers=%v", tc.header, response.Code, response.Body.String(), response.Header())
		}
	}
	if response := send(t, handler, http.MethodGet, "/api/v1/media/"+assetID+"/content", nil, map[string]string{"Range": "bytes=999-"}); response.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("unsatisfiable range status=%d", response.Code)
	}

	playback := jsonBody(t, send(t, handler, http.MethodGet, "/api/v1/media/"+assetID+"/playback", nil, nil))
	streamURL := playback["url"].(string)
	if !strings.HasPrefix(streamURL, "/api/v1/media/"+assetID+"/stream?") || playback["expiresAt"] == "" {
		t.Fatalf("playback = %v", playback)
	}
	// The signed URL works without any Authorization header and supports ranges.
	stream := send(t, handler, http.MethodGet, streamURL, nil, map[string]string{"Range": "bytes=0-3"})
	if stream.Code != http.StatusPartialContent || !bytes.Equal(stream.Body.Bytes(), data[:4]) {
		t.Fatalf("stream status=%d", stream.Code)
	}
	parsed, _ := url.Parse(streamURL)
	tamper := func(key, value string) string {
		query := parsed.Query()
		query.Set(key, value)
		return parsed.Path + "?" + query.Encode()
	}
	for name, target := range map[string]string{
		"bad signature":   tamper("signature", "AAAA"),
		"other workspace": tamper("workspace", "workspace-b"),
		"extended expiry": tamper("expires", fmt.Sprint(time.Now().Add(24*time.Hour).Unix())),
		"other asset":     strings.Replace(streamURL, assetID, "med_00000000000000000000000000000000", 1),
		"unsigned":        "/api/v1/media/" + assetID + "/stream",
	} {
		if response := send(t, handler, http.MethodGet, target, nil, nil); response.Code != http.StatusForbidden || response.Body.Len() > 300 && bytes.Contains(response.Body.Bytes(), data) {
			t.Fatalf("%s: status=%d", name, response.Code)
		}
	}
}

func TestMediaWorkspaceIsolationAndValidation(t *testing.T) {
	f := newMediaFixture(t, application.MediaLimits{MaxChunkBytes: 64, MaxRecordingBytes: 100, StaleUploadAge: time.Hour})
	a := f.handler("workspace-a", true)
	b := f.handler("workspace-b", true)
	meetingID := createMeeting(t, a)
	assetID := startRecording(t, a, meetingID)
	putChunk(t, a, assetID, 0, webmChunk(true, "x"))
	finalize(t, a, assetID, 1)

	for _, target := range []string{"/api/v1/media/" + assetID, "/api/v1/media/" + assetID + "/upload", "/api/v1/media/" + assetID + "/content", "/api/v1/media/" + assetID + "/playback", "/api/v1/meetings/" + meetingID + "/media"} {
		if response := send(t, b, http.MethodGet, target, nil, nil); response.Code != http.StatusNotFound {
			t.Fatalf("workspace-b GET %s status=%d", target, response.Code)
		}
	}
	if response := send(t, b, http.MethodPost, "/api/v1/meetings/"+meetingID+"/recordings", []byte(`{"mimeType":"audio/webm"}`), jsonHeaders); response.Code != http.StatusNotFound {
		t.Fatalf("workspace-b recording on foreign meeting status=%d", response.Code)
	}
	if response := send(t, b, http.MethodDelete, "/api/v1/media/"+assetID, nil, nil); response.Code != http.StatusNotFound {
		t.Fatalf("workspace-b delete status=%d", response.Code)
	}

	second := startRecording(t, a, meetingID)
	checks := []struct {
		name   string
		resp   *httptest.ResponseRecorder
		status int
		reason string
	}{
		{"missing meeting", send(t, a, http.MethodPost, "/api/v1/meetings/mtg_missing/recordings", []byte(`{"mimeType":"audio/webm"}`), jsonHeaders), 404, ""},
		{"video mime", send(t, a, http.MethodPost, "/api/v1/meetings/"+meetingID+"/recordings", []byte(`{"mimeType":"video/webm"}`), jsonHeaders), 400, "mimeType"},
		{"html mime", send(t, a, http.MethodPost, "/api/v1/meetings/"+meetingID+"/recordings", []byte(`{"mimeType":"text/html"}`), jsonHeaders), 400, "mimeType"},
		{"wrong magic", putChunk(t, a, second, 0, []byte("<html><script>")), 400, "chunk"},
		{"oversized chunk", putChunk(t, a, second, 1, bytes.Repeat([]byte("a"), 65)), 400, "chunk"},
		{"negative index", send(t, a, http.MethodPut, "/api/v1/media/"+second+"/chunks/-1", []byte("a"), octetHeaders), 400, "index"},
		{"non-canonical index", send(t, a, http.MethodPut, "/api/v1/media/"+second+"/chunks/007", []byte("a"), octetHeaders), 400, "index"},
		{"traversal index", send(t, a, http.MethodPut, "/api/v1/media/"+second+"/chunks/..%2F1", []byte("a"), octetHeaders), 400, "index"},
		{"index too large", send(t, a, http.MethodPut, "/api/v1/media/"+second+"/chunks/100000", []byte("a"), octetHeaders), 400, "index"},
		{"wrong content type", send(t, a, http.MethodPut, "/api/v1/media/"+second+"/chunks/1", []byte("a"), jsonHeaders), 400, "contentType"},
		{"traversal asset", send(t, a, http.MethodPut, "/api/v1/media/..%2F..%2Fetc/chunks/1", []byte("a"), octetHeaders), 404, ""},
		{"zero chunk count", send(t, a, http.MethodPost, "/api/v1/media/"+second+"/finalize", []byte(`{"chunkCount":0}`), jsonHeaders), 400, "chunkCount"},
		{"negative duration", send(t, a, http.MethodPost, "/api/v1/media/"+second+"/finalize", []byte(`{"chunkCount":1,"durationSeconds":-1}`), jsonHeaders), 400, "durationSeconds"},
		{"not ready playback", send(t, a, http.MethodGet, "/api/v1/media/"+second+"/playback", nil, nil), 409, "not_ready"},
		{"not ready content", send(t, a, http.MethodGet, "/api/v1/media/"+second+"/content", nil, nil), 409, "not_ready"},
	}
	for _, check := range checks {
		if check.resp.Code != check.status || (check.reason != "" && reason(t, check.resp) != check.reason) {
			t.Fatalf("%s: status=%d body=%s", check.name, check.resp.Code, check.resp.Body.String())
		}
	}
	// Total recording limit: 64 + 64 > 100.
	if response := putChunk(t, a, second, 0, webmChunk(true, strings.Repeat("a", 60))); response.Code != http.StatusOK {
		t.Fatalf("first chunk status=%d", response.Code)
	}
	if response := putChunk(t, a, second, 1, bytes.Repeat([]byte("b"), 60)); response.Code != http.StatusBadRequest || reason(t, response) != "recording" {
		t.Fatalf("over total status=%d body=%s", response.Code, response.Body.String())
	}
	// Nothing escaped the media directory.
	if entries, _ := os.ReadDir(filepath.Dir(f.root)); len(entries) != 1 {
		t.Fatalf("unexpected files beside media root: %v", entries)
	}
}

func TestMediaDisabledIsUnavailable(t *testing.T) {
	f := newMediaFixture(t, application.DefaultMediaLimits())
	handler := f.handler("workspace-a", false)
	meetingID := createMeeting(t, handler)
	if response := send(t, handler, http.MethodPost, "/api/v1/meetings/"+meetingID+"/recordings", []byte(`{"mimeType":"audio/webm"}`), jsonHeaders); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled media status=%d", response.Code)
	}
	capabilities := jsonBody(t, send(t, handler, http.MethodGet, "/api/v1/capabilities", nil, nil))
	if capabilities["mediaStorage"].(map[string]any)["available"] != false {
		t.Fatalf("capabilities = %v", capabilities)
	}
	enabled := jsonBody(t, send(t, f.handler("workspace-a", true), http.MethodGet, "/api/v1/capabilities", nil, nil))
	if enabled["mediaStorage"].(map[string]any)["available"] != true || enabled["transcription"].(map[string]any)["available"] != false {
		t.Fatalf("enabled capabilities = %v", enabled)
	}
}

func TestDeleteRestartAndCleanup(t *testing.T) {
	f := newMediaFixture(t, application.MediaLimits{MaxChunkBytes: 1 << 20, MaxRecordingBytes: 1 << 20, StaleUploadAge: time.Hour})
	handler := f.handler("workspace-a", true)
	meetingID := createMeeting(t, handler)
	ready := startRecording(t, handler, meetingID)
	putChunk(t, handler, ready, 0, webmChunk(true, "keep me"))
	finalize(t, handler, ready, 1)
	incomplete := startRecording(t, handler, meetingID)
	putChunk(t, handler, incomplete, 0, webmChunk(true, "part one"))
	doomed := startRecording(t, handler, meetingID)
	putChunk(t, handler, doomed, 0, webmChunk(true, "x"))
	finalize(t, handler, doomed, 1)
	if response := send(t, handler, http.MethodDelete, "/api/v1/media/"+doomed, nil, nil); response.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d", response.Code)
	}
	if response := send(t, handler, http.MethodGet, "/api/v1/media/"+doomed, nil, nil); response.Code != http.StatusNotFound {
		t.Fatalf("deleted asset status=%d", response.Code)
	}

	// "Restart": a new store and service over the same directory and metadata.
	store, err := mediastore.NewFilesystem(f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.service = application.NewMediaService(f.meetings, f.media, store, f.limits)
	restarted := f.handler("workspace-a", true)
	status := jsonBody(t, send(t, restarted, http.MethodGet, "/api/v1/media/"+incomplete+"/upload", nil, nil))["upload"].(map[string]any)
	if fmt.Sprint(status["receivedChunks"]) != "[0]" || status["status"] != "uploading" {
		t.Fatalf("incomplete upload after restart = %v", status)
	}
	if content := send(t, restarted, http.MethodGet, "/api/v1/media/"+ready+"/content", nil, nil); content.Code != http.StatusOK || !bytes.Contains(content.Body.Bytes(), []byte("keep me")) {
		t.Fatalf("ready asset after restart status=%d", content.Code)
	}

	// An orphaned object (e.g. meeting deleted) and a stale upload are cleaned; the ready asset is not.
	orphan := "obj_" + strings.Repeat("f", 32)
	if _, err := store.PutChunk(context.Background(), orphan, 0, strings.NewReader("orphan"), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Assemble(context.Background(), orphan, 1, orphan); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, dir := range []string{filepath.Join(f.root, "objects", orphan), filepath.Join(f.root, "staging", orphan)} {
		_ = os.Chtimes(dir, old, old)
	}
	if err := f.media.TouchUpload(context.Background(), "workspace-a", domain.MediaAssetID(incomplete), old); err != nil {
		t.Fatal(err)
	}
	report, err := f.service.Cleanup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.FailedUploads != 1 || report.RemovedOrphans != 1 || report.RemovedStaging < 1 {
		t.Fatalf("report = %#v", report)
	}
	if got := jsonBody(t, send(t, restarted, http.MethodGet, "/api/v1/media/"+incomplete, nil, nil))["mediaAsset"].(map[string]any)["status"]; got != "failed" {
		t.Fatalf("stale upload status = %v", got)
	}
	if content := send(t, restarted, http.MethodGet, "/api/v1/media/"+ready+"/content", nil, nil); content.Code != http.StatusOK {
		t.Fatalf("cleanup removed a ready asset: %d", content.Code)
	}
	if _, err := os.Stat(filepath.Join(f.root, "objects", orphan)); !os.IsNotExist(err) {
		t.Fatalf("orphan not removed: %v", err)
	}
}

type rejectAll struct{}

func (rejectAll) Authenticate(context.Context, string) (domain.Identity, error) {
	return domain.Identity{}, &auth.Failure{Category: auth.CategoryMissingBearer}
}

func TestMediaRoutesRequireAuthenticationExceptSignedStream(t *testing.T) {
	f := newMediaFixture(t, application.DefaultMediaLimits())
	credentials := memory.NewCredentialRepository()
	locked := api.NewHandler(application.NewMeetingService(f.meetings),
		application.NewConfigurationService(memory.NewConfigurationRepository(application.DefaultConfiguration()), credentials),
		application.NewCredentialService(credentials), rejectAll{}, application.NewDevelopmentResolver("workspace-a"), "test", nil,
		api.WithMedia(f.service, f.signer))
	for _, route := range []struct{ method, target string }{
		{http.MethodPost, "/api/v1/meetings/mtg_1/recordings"},
		{http.MethodGet, "/api/v1/meetings/mtg_1/media"},
		{http.MethodGet, "/api/v1/media/med_1"},
		{http.MethodDelete, "/api/v1/media/med_1"},
		{http.MethodGet, "/api/v1/media/med_1/upload"},
		{http.MethodPut, "/api/v1/media/med_1/chunks/0"},
		{http.MethodPost, "/api/v1/media/med_1/finalize"},
		{http.MethodGet, "/api/v1/media/med_1/content"},
		{http.MethodGet, "/api/v1/media/med_1/playback"},
	} {
		if response := send(t, locked, route.method, route.target, nil, nil); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without token status=%d", route.method, route.target, response.Code)
		}
	}
	// A valid signed URL streams without a bearer; anything else is forbidden.
	open := f.handler("workspace-a", true)
	assetID := startRecording(t, open, createMeeting(t, open))
	putChunk(t, open, assetID, 0, webmChunk(true, "signed"))
	finalize(t, open, assetID, 1)
	path, _ := f.signer.Sign("workspace-a", domain.MediaAssetID(assetID))
	if response := send(t, locked, http.MethodGet, path, nil, nil); response.Code != http.StatusOK {
		t.Fatalf("signed stream status=%d", response.Code)
	}
	if response := send(t, locked, http.MethodGet, "/api/v1/media/"+assetID+"/stream", nil, nil); response.Code != http.StatusForbidden {
		t.Fatalf("unsigned stream status=%d", response.Code)
	}
}
