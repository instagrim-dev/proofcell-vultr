package main

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestManifestRegistrationPersistsCredentials(t *testing.T) {
	var exchanges int
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		if r.Method != "POST" || r.URL.Path != "/app-manifests/registration-code/conversions" {
			t.Errorf("unexpected exchange request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": 12345, "pem": "private-pem", "webhook_secret": "private-secret", "slug": "trajectory-test"})
	}))
	defer github.Close()
	service := setupService{publicURL: "https://trajectory.example.com", stateDir: t.TempDir(), client: github.Client(), conversionBase: github.URL + "/app-manifests/"}
	page := httptest.NewRecorder()
	service.page(page, httptest.NewRequest("GET", "https://trajectory.example.com/setup", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("setup status %d: %s", page.Code, page.Body.String())
	}
	cookies := page.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].MaxAge != 600 {
		t.Fatalf("insecure setup cookie: %#v", cookies)
	}
	if !strings.Contains(page.Body.String(), "?state="+cookies[0].Value) {
		t.Fatal("manifest form does not carry matching state")
	}
	match := regexp.MustCompile(`name="manifest" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
	if len(match) != 2 {
		t.Fatal("manifest form missing escaped JSON")
	}
	var manifest struct {
		RedirectURL string            `json:"redirect_url"`
		Permissions map[string]string `json:"default_permissions"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(match[1])), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.RedirectURL != "https://trajectory.example.com/setup/callback" || len(manifest.Permissions) != 5 || manifest.Permissions["metadata"] != "read" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	request := httptest.NewRequest("GET", "https://trajectory.example.com/setup/callback?state="+cookies[0].Value+"&code=registration-code", nil)
	request.AddCookie(cookies[0])
	callback := httptest.NewRecorder()
	service.callback(callback, request)
	if callback.Code != http.StatusOK || exchanges != 1 {
		t.Fatalf("callback status %d, exchanges %d: %s", callback.Code, exchanges, callback.Body.String())
	}
	if !strings.Contains(callback.Body.String(), "https://github.com/apps/trajectory-test/installations/new") {
		t.Fatal("installation link missing")
	}
	for _, secret := range []string{"private-pem", "private-secret", "registration-code"} {
		if strings.Contains(callback.Body.String(), secret) {
			t.Fatal("callback exposed a secret")
		}
	}
	path := filepath.Join(service.stateDir, "credentials.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential file permissions: %v, %v", info, err)
	}
	for _, name := range []string{"TRAJECTORY_APP_ID", "TRAJECTORY_PRIVATE_KEY_FILE", "TRAJECTORY_WEBHOOK_SECRET"} {
		t.Setenv(name, "")
	}
	saved, err := configuredCredentials(service.stateDir)
	if err != nil || saved == nil || saved.AppID != "12345" || saved.PrivateKey != "private-pem" || saved.WebhookSecret != "private-secret" {
		t.Fatalf("saved credentials did not load: %v", err)
	}
	// Reusing the valid request cannot consume another code or overwrite credentials.
	repeated := httptest.NewRecorder()
	service.callback(repeated, request)
	if repeated.Code != http.StatusConflict || exchanges != 1 {
		t.Fatalf("repeat status %d, exchanges %d", repeated.Code, exchanges)
	}
	if err := saveCredentials(service.stateDir, appCredentials{AppID: "replacement"}); err == nil {
		t.Fatal("credential persistence allowed overwrite")
	}
}

func TestRegistrationRejectsMissingOrMismatchedState(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid state reached GitHub")
	}))
	defer github.Close()
	service := setupService{stateDir: t.TempDir(), client: github.Client(), conversionBase: github.URL + "/"}
	for _, withCookie := range []bool{false, true} {
		request := httptest.NewRequest("GET", "https://trajectory.example.com/setup/callback?state="+strings.Repeat("a", 64)+"&code=code", nil)
		if withCookie {
			request.AddCookie(&http.Cookie{Name: "trajectory_setup", Value: strings.Repeat("b", 64)})
		}
		response := httptest.NewRecorder()
		service.callback(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid state status %d", response.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(service.stateDir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatal("invalid callback wrote credentials")
	}
}

func TestRegistrationDoesNotExposeGitHubErrors(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sensitive-upstream-body", http.StatusBadRequest)
	}))
	defer github.Close()
	service := setupService{stateDir: t.TempDir(), client: github.Client(), conversionBase: github.URL + "/"}
	state := strings.Repeat("a", 64)
	request := httptest.NewRequest("GET", "https://trajectory.example.com/setup/callback?state="+state+"&code=code", nil)
	request.AddCookie(&http.Cookie{Name: "trajectory_setup", Value: state})
	response := httptest.NewRecorder()
	service.callback(response, request)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "sensitive-upstream-body") {
		t.Fatalf("upstream failure response: %d %s", response.Code, response.Body.String())
	}
}

func TestPublicOriginRequiresHTTPSOrigin(t *testing.T) {
	for _, raw := range []string{"", "http://localhost", "https://user:pass@example.com", "https://example.com/path", "https://example.com?", "https://example.com#fragment"} {
		if _, err := publicOrigin(raw); err == nil {
			t.Errorf("accepted invalid origin %q", raw)
		}
	}
	if origin, err := publicOrigin("https://example.com/"); err != nil || origin != "https://example.com" {
		t.Fatalf("valid origin rejected: %q %v", origin, err)
	}
}

func TestSetupRequiresAdministratorAuthentication(t *testing.T) {
	var calls int
	next := func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusOK) }
	for _, test := range []struct {
		name, token, username, password string
		want                            int
	}{
		{name: "disabled", want: http.StatusServiceUnavailable},
		{name: "missing credentials", token: "admin-secret", want: http.StatusUnauthorized},
		{name: "wrong password", token: "admin-secret", username: "trajectory", password: "wrong", want: http.StatusUnauthorized},
		{name: "wrong username", token: "admin-secret", username: "other", password: "admin-secret", want: http.StatusUnauthorized},
		{name: "administrator", token: "admin-secret", username: "trajectory", password: "admin-secret", want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "https://trajectory.example.com/setup", nil)
			if test.username != "" {
				request.SetBasicAuth(test.username, test.password)
			}
			response := httptest.NewRecorder()
			setupAuth(test.token, next)(response, request)
			if response.Code != test.want {
				t.Fatalf("status %d, want %d", response.Code, test.want)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("protected handler called %d times", calls)
	}
}
