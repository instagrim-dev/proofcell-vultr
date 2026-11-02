package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/instagrim-dev/proofcell-vultr/internal/githubapp"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	publicURL, err := publicOrigin(os.Getenv("TRAJECTORY_PUBLIC_URL"))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mux := http.NewServeMux()
	stateDir := envOr("TRAJECTORY_STATE_DIR", "./trajectory-state")
	setup := setupService{publicURL: publicURL, stateDir: stateDir, client: &http.Client{Timeout: 15 * time.Second}, conversionBase: "https://api.github.com/app-manifests/"}
	setupToken := os.Getenv("TRAJECTORY_SETUP_TOKEN")
	mux.HandleFunc("/setup", setupAuth(setupToken, setup.page))
	mux.HandleFunc("/setup/callback", setupAuth(setupToken, setup.callback))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})

	credentials, err := configuredCredentials(stateDir)
	if err != nil {
		return err
	}
	if credentials == nil {
		mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "GitHub App credentials are not configured", http.StatusServiceUnavailable)
		})
		log.Print("Trajectory setup mode: register at /setup, then restart to load saved credentials")
	} else {
		if id, err := strconv.ParseInt(credentials.AppID, 10, 64); err != nil || id <= 0 {
			return errors.New("TRAJECTORY_APP_ID must be a positive numeric GitHub App ID")
		}
		client, err := githubapp.NewClient(credentials.AppID, credentials.PrivateKey)
		if err != nil {
			return fmt.Errorf("configure GitHub App client: %w", err)
		}
		engine, err := githubapp.Open(stateDir, client)
		if err != nil {
			return fmt.Errorf("open Trajectory state: %w", err)
		}
		mux.Handle("/webhook", engine.Handler(credentials.WebhookSecret))
		go engine.Start(ctx)
	}

	server := &http.Server{
		Addr:              envOr("TRAJECTORY_ADDR", "127.0.0.1:8081"),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP shutdown: %v", err)
		}
	}()
	log.Printf("Trajectory listening on %s", server.Addr)
	err = server.ListenAndServe()
	stop()
	<-shutdownDone
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve Trajectory: %w", err)
	}
	return nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func publicOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("TRAJECTORY_PUBLIC_URL must be an HTTPS origin, such as https://trajectory.example.com, without credentials, path, query, or fragment")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

type appCredentials struct {
	AppID         string `json:"app_id"`
	PrivateKey    string `json:"private_key"`
	WebhookSecret string `json:"webhook_secret"`
	Slug          string `json:"slug"`
}

func configuredCredentials(stateDir string) (*appCredentials, error) {
	appID, keyFile, secret := os.Getenv("TRAJECTORY_APP_ID"), os.Getenv("TRAJECTORY_PRIVATE_KEY_FILE"), os.Getenv("TRAJECTORY_WEBHOOK_SECRET")
	if appID != "" || keyFile != "" || secret != "" {
		if appID == "" || keyFile == "" || secret == "" {
			return nil, errors.New("set all of TRAJECTORY_APP_ID, TRAJECTORY_PRIVATE_KEY_FILE, and TRAJECTORY_WEBHOOK_SECRET, or omit all three to load saved credentials")
		}
		key, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("read TRAJECTORY_PRIVATE_KEY_FILE: %w", err)
		}
		return &appCredentials{AppID: appID, PrivateKey: string(key), WebhookSecret: secret}, nil
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "credentials.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("cannot read saved GitHub App credentials")
	}
	var credentials appCredentials
	if json.Unmarshal(data, &credentials) != nil || credentials.AppID == "" || credentials.PrivateKey == "" || credentials.WebhookSecret == "" {
		return nil, errors.New("saved GitHub App credentials are incomplete or invalid")
	}
	return &credentials, nil
}

type setupService struct {
	publicURL, stateDir, conversionBase string
	client                              *http.Client
}

func setupAuth(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if token == "" {
			http.Error(w, "set TRAJECTORY_SETUP_TOKEN to enable app registration", http.StatusServiceUnavailable)
			return
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "trajectory" || subtle.ConstantTimeCompare([]byte(password), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Trajectory setup", charset="UTF-8"`)
			http.Error(w, "setup authentication required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

var setupPage = template.Must(template.New("setup").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><title>Register Trajectory</title>
<body><h1>Register your Trajectory GitHub App</h1>
<p>This creates a GitHub App for {{.PublicURL}}. You choose which repositories to install it on.</p>
<p>Permissions: repository metadata (read), issues (write), contents (write), and pull requests (write). Events: issues and issue comments.</p>
<form action="https://github.com/settings/apps/new?state={{.State}}" method="post">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<button type="submit">Register on GitHub</button></form>
<p>GitHub will return here to save the App ID, private key, and webhook secret in this service's protected state directory. Then restart the service and install the app on your repositories.</p>
</body></html>`))

func (s setupService) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/setup" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, err := os.Stat(filepath.Join(s.stateDir, "credentials.json")); err == nil {
		http.Error(w, "App credentials already exist. Restart the service to use them; registration will not overwrite them.", http.StatusConflict)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "cannot inspect credential storage", http.StatusInternalServerError)
		return
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		http.Error(w, "cannot initialize registration", http.StatusInternalServerError)
		return
	}
	state := hex.EncodeToString(random[:])
	http.SetCookie(w, &http.Cookie{Name: "trajectory_setup", Value: state, Path: "/setup/callback", MaxAge: 600, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	manifest, _ := json.Marshal(map[string]any{
		"name":            "Trajectory",
		"url":             s.publicURL,
		"redirect_url":    s.publicURL + "/setup/callback",
		"public":          false,
		"hook_attributes": map[string]any{"url": s.publicURL + "/webhook", "active": true},
		"default_permissions": map[string]string{
			"metadata": "read", "issues": "write", "contents": "write", "pull_requests": "write",
		},
		"default_events": []string{"issues", "issue_comment"},
	})
	privatePage(w)
	if err := setupPage.Execute(w, struct{ PublicURL, Manifest, State string }{s.publicURL, string(manifest), state}); err != nil {
		log.Print("render setup page failed")
	}
}

var registrationPage = template.Must(template.New("registered").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><title>Install Trajectory</title><body><h1>App registration saved</h1><p>The App ID, private key, and webhook secret were saved with owner-only file permissions. Restart this service with the same TRAJECTORY_STATE_DIR and omit the three credential environment variables to load them.</p><p><a href="{{.}}">Install your GitHub App</a> on the repositories you want Trajectory to handle.</p><p>Credentials are never displayed here.</p></body></html>`))

func privatePage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func (s setupService) callback(w http.ResponseWriter, r *http.Request) {
	privatePage(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cookie, err := r.Cookie("trajectory_setup")
	state := r.URL.Query().Get("state")
	if err != nil || len(state) != 64 || len(cookie.Value) != 64 || subtle.ConstantTimeCompare([]byte(state), []byte(cookie.Value)) != 1 {
		http.Error(w, "registration state mismatch; start again at /setup", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 256 || strings.ContainsAny(code, "/\\?#") {
		http.Error(w, "invalid registration code; start again at /setup", http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(filepath.Join(s.stateDir, "credentials.json")); err == nil {
		http.Error(w, "App credentials already exist; refusing to overwrite", http.StatusConflict)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "cannot inspect credential storage", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "trajectory_setup", Value: "", Path: "/setup/callback", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.conversionBase+url.PathEscape(code)+"/conversions", nil)
	if err != nil {
		http.Error(w, "cannot prepare registration exchange", http.StatusInternalServerError)
		return
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "trajectory-github-app")
	response, err := s.client.Do(request)
	if err != nil {
		http.Error(w, "GitHub registration exchange failed; start again at /setup", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		http.Error(w, "GitHub rejected registration exchange; start again at /setup", http.StatusBadGateway)
		return
	}
	var result struct {
		ID            int64  `json:"id"`
		PEM           string `json:"pem"`
		WebhookSecret string `json:"webhook_secret"`
		Slug          string `json:"slug"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result) != nil || result.ID <= 0 || result.PEM == "" || result.WebhookSecret == "" || !validSlug(result.Slug) {
		http.Error(w, "GitHub returned incomplete registration credentials", http.StatusBadGateway)
		return
	}
	credentials := appCredentials{AppID: strconv.FormatInt(result.ID, 10), PrivateKey: result.PEM, WebhookSecret: result.WebhookSecret, Slug: result.Slug}
	if err := saveCredentials(s.stateDir, credentials); err != nil {
		http.Error(w, "could not save registration credentials; refusing to overwrite existing credentials", http.StatusInternalServerError)
		return
	}
	if err := registrationPage.Execute(w, "https://github.com/apps/"+result.Slug+"/installations/new"); err != nil {
		log.Print("render installation page failed")
	}
}

func validSlug(slug string) bool {
	if slug == "" || len(slug) > 100 {
		return false
	}
	for _, ch := range slug {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
			return false
		}
	}
	return true
}

func saveCredentials(stateDir string, credentials appCredentials) error {
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(stateDir, ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(credentials); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// Linking publishes the complete mode-0600 file atomically and cannot overwrite.
	return os.Link(file.Name(), filepath.Join(stateDir, "credentials.json"))
}
