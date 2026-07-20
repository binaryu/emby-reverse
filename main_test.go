package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func newTestApp(t *testing.T) *App {
	t.Helper()

	db, err := openDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return &App{
		db: db,
		pm: NewProxyManager(db),
	}
}

func freePort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port listen: %v", err)
	}
	defer ln.Close()

	return ln.Addr().(*net.TCPAddr).Port
}

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	return body
}

func TestGenerateTokenPreservesSpecialCharacters(t *testing.T) {
	jwtSecret = []byte("test-secret")

	token, err := generateToken(7, `bad"name\user`)
	if err != nil {
		t.Fatalf("generateToken error: %v", err)
	}

	userID, username, err := validateToken(token)
	if err != nil {
		t.Fatalf("validateToken error: %v", err)
	}

	if userID != 7 {
		t.Fatalf("userID = %d, want 7", userID)
	}
	if username != `bad"name\user` {
		t.Fatalf("username = %q", username)
	}
}

func TestResolveJWTSecretGeneratesRandomFallback(t *testing.T) {
	secretA, ephemeralA, err := resolveJWTSecret("")
	if err != nil {
		t.Fatalf("resolveJWTSecret A: %v", err)
	}
	secretB, ephemeralB, err := resolveJWTSecret("")
	if err != nil {
		t.Fatalf("resolveJWTSecret B: %v", err)
	}

	if !ephemeralA || !ephemeralB {
		t.Fatalf("expected ephemeral fallback secrets")
	}
	if len(secretA) == 0 || len(secretB) == 0 {
		t.Fatalf("expected non-empty secrets")
	}
	if bytes.Equal(secretA, secretB) {
		t.Fatalf("expected random fallback secrets to differ")
	}
}

func TestTLSIssuerNameFallsBackSafely(t *testing.T) {
	name := tlsIssuerName(nil)
	if name != "" {
		t.Fatalf("nil issuer name = %q, want empty", name)
	}
}

func TestHandleAuthCheckExposesSingleAdminModeBeforeSetup(t *testing.T) {
	app := newTestApp(t)
	jwtSecretEphemeral = true
	t.Cleanup(func() { jwtSecretEphemeral = false })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/check", nil)

	app.handleAuthCheck(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	body := decodeBody(t, rr)
	if got := mustBoolValue(t, body, "needs_setup"); !got {
		t.Fatalf("needs_setup = %v, want true", got)
	}
	if got := mustStringValue(t, body, "mode"); got != "single_admin" {
		t.Fatalf("mode = %q, want single_admin", got)
	}
	if got := mustBoolValue(t, body, "jwt_secret_ephemeral"); !got {
		t.Fatalf("jwt_secret_ephemeral = %v, want true", got)
	}
}

func TestHandleAuthCheckExposesConfiguredSingleAdminMode(t *testing.T) {
	app := newTestApp(t)
	jwtSecretEphemeral = false

	if _, err := app.db.CreateUser("admin", "admin123"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/check", nil)

	app.handleAuthCheck(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	body := decodeBody(t, rr)
	if got := mustBoolValue(t, body, "needs_setup"); got {
		t.Fatalf("needs_setup = %v, want false", got)
	}
	if got := mustStringValue(t, body, "mode"); got != "single_admin" {
		t.Fatalf("mode = %q, want single_admin", got)
	}
	if got := mustBoolValue(t, body, "jwt_secret_ephemeral"); got {
		t.Fatalf("jwt_secret_ephemeral = %v, want false", got)
	}
}

func TestDiagnoseSiteUsesRootSystemInfoProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Info/Public" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Version":"4.8.0.80"}`))
	}))
	defer server.Close()

	app := newTestApp(t)
	site, err := app.db.CreateSite("diag", freePort(t), server.URL, "", "direct", "[]", "infuse", 0, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	result := diagnoseSite(site, app.pm)
	if result.Health.Status != "online" {
		t.Fatalf("health.status = %q, want online (error=%q)", result.Health.Status, result.Health.Error)
	}
	if result.Health.EmbyVer != "4.8.0.80" {
		t.Fatalf("emby_version = %q, want 4.8.0.80", result.Health.EmbyVer)
	}
	if result.Health.Probe.Kind != "metadata_api" {
		t.Fatalf("probe.kind = %q, want metadata_api", result.Health.Probe.Kind)
	}
	if result.Health.Probe.Method != http.MethodGet {
		t.Fatalf("probe.method = %q, want GET", result.Health.Probe.Method)
	}
	if !strings.HasSuffix(result.Health.Probe.URL, "/System/Info/Public") {
		t.Fatalf("probe.url = %q, want suffix /System/Info/Public", result.Health.Probe.URL)
	}
	if result.Health.Probe.HTTPStatus != http.StatusOK {
		t.Fatalf("probe.http_status = %d, want 200", result.Health.Probe.HTTPStatus)
	}
}

func TestDiagnoseSiteTreatsReachable4xxAsOnline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusForbidden)
	}))
	defer server.Close()

	app := newTestApp(t)
	site, err := app.db.CreateSite("diag", freePort(t), server.URL, "", "direct", "[]", "infuse", 0, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	result := diagnoseSite(site, app.pm)
	if result.Health.Status != "online" {
		t.Fatalf("health.status = %q, want online (error=%q)", result.Health.Status, result.Health.Error)
	}
	if result.Health.Error != "" {
		t.Fatalf("health.error = %q, want empty for reachable upstream", result.Health.Error)
	}
	if result.Health.Probe.HTTPStatus != http.StatusForbidden {
		t.Fatalf("probe.http_status = %d, want 403", result.Health.Probe.HTTPStatus)
	}
}

func TestDiagnoseSiteMarksRootReachabilityFallbackProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	app := newTestApp(t)
	site, err := app.db.CreateSite("diag", freePort(t), server.URL, "", "direct", "[]", "infuse", 0, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	result := diagnoseSite(site, app.pm)
	if result.Health.Status != "online" {
		t.Fatalf("health.status = %q, want online (error=%q)", result.Health.Status, result.Health.Error)
	}
	if result.Health.Probe.Kind != "reachability_fallback" {
		t.Fatalf("probe.kind = %q, want reachability_fallback", result.Health.Probe.Kind)
	}
	if result.Health.Probe.Method != http.MethodGet {
		t.Fatalf("probe.method = %q, want GET", result.Health.Probe.Method)
	}
	if result.Health.Probe.URL != server.URL+"/" {
		t.Fatalf("probe.url = %q, want %q", result.Health.Probe.URL, server.URL+"/")
	}
	if result.Health.Probe.HTTPStatus != http.StatusOK {
		t.Fatalf("probe.http_status = %d, want 200", result.Health.Probe.HTTPStatus)
	}
}

func TestHandleSiteDiagReturnsPlaybackFallbackMetadata(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Info/Public" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"Version":"4.8.1.0"}`))
	}))
	defer apiServer.Close()

	app := newTestApp(t)
	site, err := app.db.CreateSite("diag", freePort(t), apiServer.URL, "", "direct", "[]", "infuse", 0, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sites/"+jsonNumber64(site.ID)+"/diag", nil)

	app.handleSiteByID(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	body := decodeBody(t, rr)
	upstreams := mustMapValue(t, body, "upstreams")
	primary := mustMapValue(t, upstreams, "primary")
	playback := mustMapValue(t, upstreams, "playback")

	if got := mustStringValue(t, primary, "effective_url"); got != apiServer.URL {
		t.Fatalf("primary effective_url = %q, want %q", got, apiServer.URL)
	}
	if got := mustBoolValue(t, primary, "show_health"); !got {
		t.Fatalf("primary show_health = %v, want true", got)
	}
	primaryHealth := mustMapValue(t, primary, "health")
	primaryProbe := mustMapValue(t, primaryHealth, "probe")
	if got := mustStringValue(t, primaryProbe, "kind"); got != "metadata_api" {
		t.Fatalf("primary probe.kind = %q, want metadata_api", got)
	}
	if got := mustStringValue(t, primaryProbe, "method"); got != http.MethodGet {
		t.Fatalf("primary probe.method = %q, want GET", got)
	}
	if got := mustStringValue(t, playback, "effective_url"); got != apiServer.URL {
		t.Fatalf("playback effective_url = %q, want %q", got, apiServer.URL)
	}
	if got := mustBoolValue(t, playback, "configured"); got {
		t.Fatalf("playback configured = %v, want false", got)
	}
	if got := mustBoolValue(t, playback, "using_fallback"); !got {
		t.Fatalf("playback using_fallback = %v, want true", got)
	}
	if got := mustBoolValue(t, playback, "same_as_primary"); !got {
		t.Fatalf("playback same_as_primary = %v, want true", got)
	}
	if got := mustBoolValue(t, playback, "show_health"); got {
		t.Fatalf("playback show_health = %v, want false", got)
	}
	if got := mustBoolValue(t, playback, "show_tls"); got {
		t.Fatalf("playback show_tls = %v, want false", got)
	}
	playbackProbe := mustMapValue(t, mustMapValue(t, playback, "health"), "probe")
	if got := mustStringValue(t, playbackProbe, "kind"); got != "metadata_api" {
		t.Fatalf("fallback playback probe.kind = %q, want metadata_api", got)
	}
}

func TestHandleSiteDiagMarksSharedPlaybackTarget(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Info/Public" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"Version":"4.8.1.0"}`))
	}))
	defer apiServer.Close()

	app := newTestApp(t)
	site, err := app.db.CreateSite("diag", freePort(t), apiServer.URL, apiServer.URL, "direct", "[]", "infuse", 0, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sites/"+jsonNumber64(site.ID)+"/diag", nil)

	app.handleSiteByID(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	body := decodeBody(t, rr)
	playback := mustMapValue(t, mustMapValue(t, body, "upstreams"), "playback")

	if got := mustBoolValue(t, playback, "configured"); !got {
		t.Fatalf("playback configured = %v, want true", got)
	}
	if got := mustBoolValue(t, playback, "using_fallback"); got {
		t.Fatalf("playback using_fallback = %v, want false", got)
	}
	if got := mustBoolValue(t, playback, "same_as_primary"); !got {
		t.Fatalf("playback same_as_primary = %v, want true", got)
	}
	if got := mustBoolValue(t, playback, "show_health"); got {
		t.Fatalf("playback show_health = %v, want false", got)
	}
	playbackProbe := mustMapValue(t, mustMapValue(t, playback, "health"), "probe")
	if got := mustStringValue(t, playbackProbe, "kind"); got != "metadata_api" {
		t.Fatalf("shared playback probe.kind = %q, want metadata_api", got)
	}
}

func TestHandleSiteDiagExposesSeparatePlaybackTLS(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Info/Public" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"Version":"4.8.1.0"}`))
	}))
	defer apiServer.Close()

	var playbackMethod string
	var playbackPath string
	playbackServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		playbackMethod = r.Method
		playbackPath = r.URL.Path
		if r.Method == http.MethodGet && r.URL.Path == "/System/Info/Public" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"Version":"4.8.2.0"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer playbackServer.Close()

	app := newTestApp(t)
	site, err := app.db.CreateSite("diag", freePort(t), apiServer.URL, playbackServer.URL, "direct", "[]", "infuse", 0, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sites/"+jsonNumber64(site.ID)+"/diag", nil)

	app.handleSiteByID(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	body := decodeBody(t, rr)
	upstreams := mustMapValue(t, body, "upstreams")
	primary := mustMapValue(t, upstreams, "primary")
	playback := mustMapValue(t, upstreams, "playback")
	playbackHealth := mustMapValue(t, playback, "health")
	playbackProbe := mustMapValue(t, playbackHealth, "probe")
	playbackTLS := mustMapValue(t, playback, "tls")

	if got := mustBoolValue(t, primary, "show_tls"); got {
		t.Fatalf("primary show_tls = %v, want false", got)
	}
	if got := mustBoolValue(t, playback, "configured"); !got {
		t.Fatalf("playback configured = %v, want true", got)
	}
	if got := mustBoolValue(t, playback, "same_as_primary"); got {
		t.Fatalf("playback same_as_primary = %v, want false", got)
	}
	if got := mustBoolValue(t, playback, "show_health"); !got {
		t.Fatalf("playback show_health = %v, want true", got)
	}
	if got := mustBoolValue(t, playback, "show_tls"); !got {
		t.Fatalf("playback show_tls = %v, want true", got)
	}
	if got := mustStringValue(t, playbackProbe, "kind"); got != "metadata_api" {
		t.Fatalf("playback probe.kind = %q, want metadata_api", got)
	}
	if got := mustStringValue(t, playbackProbe, "method"); got != http.MethodGet {
		t.Fatalf("playback probe.method = %q, want GET", got)
	}
	if got := mustNumberValue(t, playbackProbe, "http_status"); got != http.StatusOK {
		t.Fatalf("playback probe.http_status = %d, want 200", got)
	}
	if got := mustStringValue(t, playbackHealth, "status"); got != "online" {
		t.Fatalf("playback health.status = %q, want online", got)
	}
	if got := mustStringValue(t, playbackHealth, "emby_version"); got != "4.8.2.0" {
		t.Fatalf("playback health.emby_version = %q, want 4.8.2.0", got)
	}
	if got := mustBoolValue(t, playbackTLS, "enabled"); !got {
		t.Fatalf("playback tls.enabled = %v, want true", got)
	}
	if got := mustStringValue(t, playback, "effective_url"); got != playbackServer.URL {
		t.Fatalf("playback effective_url = %q, want %q", got, playbackServer.URL)
	}
	if playbackMethod != http.MethodGet {
		t.Fatalf("playback request method = %q, want GET", playbackMethod)
	}
	if playbackPath != "/System/Info/Public" {
		t.Fatalf("playback request path = %q, want /System/Info/Public", playbackPath)
	}
}

func TestApplyUAProfileHeadersRewritesClientAndVersionIdentity(t *testing.T) {
	header := http.Header{}
	header.Set("User-Agent", "OldUA/1.0")
	header.Set("X-Emby-Authorization", `MediaBrowser Client="Old Client", Device="TV", Version="9.9.9"`)
	header.Set("Authorization", `MediaBrowser Client="Old Client", Device="TV", Version="9.9.9"`)

	applyUAProfileHeaders(header, uaProfiles["client"])

	if got := header.Get("User-Agent"); got != uaProfiles["client"].UserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, uaProfiles["client"].UserAgent)
	}
	if got := header.Get("X-Emby-Authorization"); !strings.Contains(got, `Client="Emby Theater"`) {
		t.Fatalf("X-Emby-Authorization = %q", got)
	}
	if got := header.Get("X-Emby-Authorization"); !strings.Contains(got, `Version="4.7.0"`) {
		t.Fatalf("X-Emby-Authorization version = %q", got)
	}
	if got := header.Get("Authorization"); !strings.Contains(got, `Client="Emby Theater"`) {
		t.Fatalf("Authorization = %q", got)
	}
	if got := header.Get("Authorization"); !strings.Contains(got, `Version="4.7.0"`) {
		t.Fatalf("Authorization version = %q", got)
	}
}

func TestHandleSiteDiagReturnsSpoofedVersionField(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Info/Public" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"Version":"4.8.1.0"}`))
	}))
	defer apiServer.Close()

	app := newTestApp(t)
	site, err := app.db.CreateSite("diag", freePort(t), apiServer.URL, "", "direct", "[]", "client", 0, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sites/"+jsonNumber64(site.ID)+"/diag", nil)

	app.handleSiteByID(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	headers := mustMapValue(t, decodeBody(t, rr), "headers")
	if got := mustBoolValue(t, headers, "ua_applied"); !got {
		t.Fatalf("ua_applied = %v, want true", got)
	}
	if got := mustStringValue(t, headers, "current_ua"); got != uaProfiles["client"].UserAgent {
		t.Fatalf("current_ua = %q, want %q", got, uaProfiles["client"].UserAgent)
	}
	if got := mustStringValue(t, headers, "client_field"); got != uaProfiles["client"].Client {
		t.Fatalf("client_field = %q, want %q", got, uaProfiles["client"].Client)
	}
	if got := mustStringValue(t, headers, "version_field"); got != uaProfiles["client"].Version {
		t.Fatalf("version_field = %q, want %q", got, uaProfiles["client"].Version)
	}
}

func TestHandleSitesCreateRollsBackOnStartFailure(t *testing.T) {
	app := newTestApp(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupied listen: %v", err)
	}
	port := occupied.Addr().(*net.TCPAddr).Port
	occupied.Close()
	occupied, err = net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatalf("occupied wildcard listen: %v", err)
	}
	defer occupied.Close()

	body := strings.NewReader(`{"name":"conflict","listen_port":` + jsonNumber(port) + `,"target_url":"http://127.0.0.1:8096","ua_mode":"infuse"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sites", body)
	rr := httptest.NewRecorder()

	app.handleSites(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if count := lenMust(app.db.ListSites()); count != 0 {
		t.Fatalf("site count = %d, want 0", count)
	}
}

func TestHandleSiteToggleRevertsWhenStartFails(t *testing.T) {
	app := newTestApp(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupied listen: %v", err)
	}
	port := occupied.Addr().(*net.TCPAddr).Port
	occupied.Close()
	occupied, err = net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatalf("occupied wildcard listen: %v", err)
	}
	defer occupied.Close()

	site, err := app.db.CreateSite("disabled", port, "http://127.0.0.1:8096", "", "direct", "[]", "infuse", 0, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}
	if _, err := app.db.db.Exec("UPDATE sites SET enabled=0 WHERE id=?", site.ID); err != nil {
		t.Fatalf("disable site: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sites/"+jsonNumber64(site.ID)+"/toggle", nil)
	rr := httptest.NewRecorder()

	app.handleSiteByID(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	reloaded, err := app.db.GetSite(site.ID)
	if err != nil {
		t.Fatalf("GetSite: %v", err)
	}
	if reloaded.Enabled {
		t.Fatalf("site enabled = true, want false")
	}
}

func TestHandleSiteUpdateRollsBackOnStartFailure(t *testing.T) {
	app := newTestApp(t)
	initialPort := freePort(t)
	site, err := app.db.CreateSite("stable", initialPort, "http://127.0.0.1:8096", "", "direct", "[]", "infuse", 0, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}
	if err := app.pm.StartSite(*site); err != nil {
		t.Fatalf("StartSite: %v", err)
	}
	t.Cleanup(func() { app.pm.StopSite(site.ID) })

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupied listen: %v", err)
	}
	conflictPort := occupied.Addr().(*net.TCPAddr).Port
	occupied.Close()
	occupied, err = net.Listen("tcp", fmt.Sprintf(":%d", conflictPort))
	if err != nil {
		t.Fatalf("occupied wildcard listen: %v", err)
	}
	defer occupied.Close()

	body := strings.NewReader(`{"name":"stable","listen_port":` + jsonNumber(conflictPort) + `,"target_url":"http://127.0.0.1:8096","ua_mode":"infuse"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/sites/"+jsonNumber64(site.ID), body)
	rr := httptest.NewRecorder()

	app.handleSiteByID(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	reloaded, err := app.db.GetSite(site.ID)
	if err != nil {
		t.Fatalf("GetSite: %v", err)
	}
	if reloaded.ListenPort != initialPort {
		t.Fatalf("listen_port = %d, want %d", reloaded.ListenPort, initialPort)
	}
	if !app.pm.IsRunning(site.ID) {
		t.Fatalf("expected original site to keep running")
	}
}

func TestFlushTrafficUpdatesBaselineAndStopPersistsPendingUsage(t *testing.T) {
	app := newTestApp(t)
	site, err := app.db.CreateSite("traffic", freePort(t), "http://127.0.0.1:8096", "", "direct", "[]", "infuse", 1024, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	inst := &ProxyInstance{Site: *site, server: &http.Server{}}
	inst.bytesIn.Store(120)
	inst.bytesOut.Store(80)
	app.pm.proxies[site.ID] = inst

	app.pm.FlushTraffic()

	if got := inst.persistedTraffic.Load(); got != 200 {
		t.Fatalf("persistedTraffic after flush = %d, want 200", got)
	}
	inst.bytesIn.Store(10)
	inst.bytesOut.Store(5)
	app.pm.StopSite(site.ID)

	reloaded, err := app.db.GetSite(site.ID)
	if err != nil {
		t.Fatalf("GetSite: %v", err)
	}
	if reloaded.TrafficUsed != 215 {
		t.Fatalf("traffic_used = %d, want 215", reloaded.TrafficUsed)
	}
}

func TestAddTrafficAggregatesSameHour(t *testing.T) {
	app := newTestApp(t)
	site, err := app.db.CreateSite("aggregate", freePort(t), "http://127.0.0.1:8096", "", "direct", "[]", "infuse", 0, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	app.db.AddTraffic(site.ID, 10, 20)
	app.db.AddTraffic(site.ID, 5, 7)

	logs, err := app.db.GetTrafficLogs(site.ID, 1)
	if err != nil {
		t.Fatalf("GetTrafficLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("len(logs) = %d, want 1", len(logs))
	}
	if logs[0].BytesIn != 15 || logs[0].BytesOut != 27 {
		t.Fatalf("aggregated log = in:%d out:%d", logs[0].BytesIn, logs[0].BytesOut)
	}
}

func TestHandleSitesCreatePersistsPlaybackTargetURL(t *testing.T) {
	app := newTestApp(t)

	body := strings.NewReader(`{"name":"split","listen_port":` + jsonNumber(freePort(t)) + `,"target_url":"http://127.0.0.1:8096","playback_target_url":"https://media.example.com","ua_mode":"infuse"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/sites", body)
	rr := httptest.NewRecorder()

	app.handleSites(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var site Site
	if err := json.Unmarshal(rr.Body.Bytes(), &site); err != nil {
		t.Fatalf("decode site: %v body=%s", err, rr.Body.String())
	}
	if site.PlaybackTargetURL != "https://media.example.com" {
		t.Fatalf("playback_target_url = %q, want %q", site.PlaybackTargetURL, "https://media.example.com")
	}

	reloaded, err := app.db.GetSite(site.ID)
	if err != nil {
		t.Fatalf("GetSite: %v", err)
	}
	if reloaded.PlaybackTargetURL != "https://media.example.com" {
		t.Fatalf("persisted playback_target_url = %q, want %q", reloaded.PlaybackTargetURL, "https://media.example.com")
	}
}

func TestProxyRoutesPlaybackRequestsToPlaybackTarget(t *testing.T) {
	app := newTestApp(t)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("api:" + r.URL.Path))
	}))
	defer apiServer.Close()

	playbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("playback:" + r.URL.Path))
	}))
	defer playbackServer.Close()

	site, err := app.db.CreateSite("split", freePort(t), apiServer.URL, playbackServer.URL, "direct", "[]", "infuse", 0, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}
	if err := app.pm.StartSite(*site); err != nil {
		t.Fatalf("StartSite: %v", err)
	}
	t.Cleanup(func() { app.pm.StopSite(site.ID) })

	mainResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/System/Info", site.ListenPort))
	if err != nil {
		t.Fatalf("GET main route: %v", err)
	}
	defer mainResp.Body.Close()

	playbackResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/emby/Videos/123/stream", site.ListenPort))
	if err != nil {
		t.Fatalf("GET playback route: %v", err)
	}
	defer playbackResp.Body.Close()

	if body := mustReadBody(t, mainResp); !strings.Contains(body, "api:/System/Info") {
		t.Fatalf("main route body = %q", body)
	}
	if body := mustReadBody(t, playbackResp); !strings.Contains(body, "playback:/emby/Videos/123/stream") {
		t.Fatalf("playback route body = %q", body)
	}
}

func TestProxyPlaybackRequestsFallBackToMainTarget(t *testing.T) {
	app := newTestApp(t)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("api:" + r.URL.Path))
	}))
	defer apiServer.Close()

	site, err := app.db.CreateSite("single", freePort(t), apiServer.URL, "", "direct", "[]", "infuse", 0, 0, "", "", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}
	if err := app.pm.StartSite(*site); err != nil {
		t.Fatalf("StartSite: %v", err)
	}
	t.Cleanup(func() { app.pm.StopSite(site.ID) })

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/Videos/42/stream", site.ListenPort))
	if err != nil {
		t.Fatalf("GET fallback playback route: %v", err)
	}
	defer resp.Body.Close()

	if body := mustReadBody(t, resp); !strings.Contains(body, "api:/Videos/42/stream") {
		t.Fatalf("fallback playback body = %q", body)
	}
}

func TestProxyInjectsEMOSHeadersAndRange206(t *testing.T) {
	app := newTestApp(t)

	var gotID, gotName, gotXFF, gotRange string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("EMOS-PROXY-ID")
		gotName = r.Header.Get("EMOS-PROXY-NAME")
		gotXFF = r.Header.Get("X-Forwarded-For")
		gotRange = r.Header.Get("Range")
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-3/8")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte("0123"))
			return
		}
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	site, err := app.db.CreateSite("emos", freePort(t), upstream.URL, "", "direct", "[]", "infuse", 0, 0, "eABCDEFGHs", "@emos", false, false)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}
	if err := app.pm.StartSite(*site); err != nil {
		t.Fatalf("StartSite: %v", err)
	}
	t.Cleanup(func() { app.pm.StopSite(site.ID) })

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/emby/Videos/1/stream", site.ListenPort), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Range", "bytes=0-3")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if gotID != "eABCDEFGHs" {
		t.Fatalf("EMOS-PROXY-ID = %q", gotID)
	}
	if gotName != "@emos" {
		t.Fatalf("EMOS-PROXY-NAME = %q", gotName)
	}
	if gotXFF == "" {
		t.Fatalf("X-Forwarded-For empty")
	}
	if gotRange != "bytes=0-3" {
		t.Fatalf("Range = %q", gotRange)
	}
	if body := mustReadBody(t, resp); body != "0123" {
		t.Fatalf("body = %q", body)
	}
}

func TestProxyCachesPingAndThrottlesProgress(t *testing.T) {
	app := newTestApp(t)

	var pingHits, progressHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/System/Ping"):
			pingHits++
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
		case strings.Contains(r.URL.Path, "/Sessions/Playing/Progress"):
			progressHits++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	site, err := app.db.CreateSite("cache", freePort(t), upstream.URL, "", "direct", "[]", "infuse", 0, 0, "e1", "@t", true, true)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}
	if err := app.pm.StartSite(*site); err != nil {
		t.Fatalf("StartSite: %v", err)
	}
	t.Cleanup(func() { app.pm.StopSite(site.ID) })

	// Clear any prior cache entries from other tests by using unique path prefix via site id in key already.
	urlPing := fmt.Sprintf("http://127.0.0.1:%d/emby/System/Ping", site.ListenPort)
	for i := 0; i < 3; i++ {
		resp, err := http.Get(urlPing)
		if err != nil {
			t.Fatalf("ping GET: %v", err)
		}
		body := mustReadBody(t, resp)
		resp.Body.Close()
		if body != `{"ok":true}` {
			t.Fatalf("ping body = %q", body)
		}
	}
	if pingHits != 1 {
		t.Fatalf("pingHits = %d, want 1 (cached)", pingHits)
	}

	urlProgress := fmt.Sprintf("http://127.0.0.1:%d/emby/Sessions/Playing/Progress", site.ListenPort)
	for i := 0; i < 3; i++ {
		resp, err := http.Post(urlProgress, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("progress POST: %v", err)
		}
		resp.Body.Close()
		if i == 0 && resp.StatusCode != http.StatusNoContent {
			// first request reaches upstream which returns 204, or throttle also 204
		}
	}
	if progressHits != 1 {
		t.Fatalf("progressHits = %d, want 1 (throttled)", progressHits)
	}
}

func TestSitePersistsEMOSProxyFields(t *testing.T) {
	app := newTestApp(t)
	site, err := app.db.CreateSite("p", freePort(t), "http://127.0.0.1:8096", "", "direct", "[]", "infuse", 0, 0, "eID", "@name", true, true)
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}
	got, err := app.db.GetSite(site.ID)
	if err != nil {
		t.Fatalf("GetSite: %v", err)
	}
	if got.ProxyID != "eID" || got.ProxyName != "@name" || !got.CacheStatic || !got.ThrottleProgress {
		t.Fatalf("persisted fields = %+v", got)
	}
	if err := app.db.UpdateSite(site.ID, got.Name, got.ListenPort, got.TargetURL, got.PlaybackTargetURL, got.PlaybackMode, got.StreamHosts, got.UAMode, got.TrafficQuota, got.SpeedLimit, "e2", "@n2", false, true); err != nil {
		t.Fatalf("UpdateSite: %v", err)
	}
	got, err = app.db.GetSite(site.ID)
	if err != nil {
		t.Fatalf("GetSite2: %v", err)
	}
	if got.ProxyID != "e2" || got.ProxyName != "@n2" || got.CacheStatic || !got.ThrottleProgress {
		t.Fatalf("updated fields = %+v", got)
	}
}

func lenMust(sites []Site, err error) int {
	if err != nil {
		panic(err)
	}
	return len(sites)
}

func jsonNumber(v int) string {
	return strconv.Itoa(v)
}

func jsonNumber64(v int64) string {
	return strconv.FormatInt(v, 10)
}

func mustReadBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

func mustMapValue(t *testing.T, body map[string]interface{}, key string) map[string]interface{} {
	t.Helper()

	value, ok := body[key]
	if !ok {
		t.Fatalf("missing key %q in %#v", key, body)
	}
	result, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("key %q = %#v, want object", key, value)
	}
	return result
}

func mustStringValue(t *testing.T, body map[string]interface{}, key string) string {
	t.Helper()

	value, ok := body[key]
	if !ok {
		t.Fatalf("missing key %q in %#v", key, body)
	}
	result, ok := value.(string)
	if !ok {
		t.Fatalf("key %q = %#v, want string", key, value)
	}
	return result
}

func mustBoolValue(t *testing.T, body map[string]interface{}, key string) bool {
	t.Helper()

	value, ok := body[key]
	if !ok {
		t.Fatalf("missing key %q in %#v", key, body)
	}
	result, ok := value.(bool)
	if !ok {
		t.Fatalf("key %q = %#v, want bool", key, value)
	}
	return result
}

func mustNumberValue(t *testing.T, body map[string]interface{}, key string) int {
	t.Helper()

	value, ok := body[key]
	if !ok {
		t.Fatalf("missing key %q in %#v", key, body)
	}
	result, ok := value.(float64)
	if !ok {
		t.Fatalf("key %q = %#v, want number", key, value)
	}
	return int(result)
}
