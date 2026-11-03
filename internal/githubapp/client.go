package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client authenticates as a GitHub App, then narrows each installation token to
// the one repository being operated on. Configure BaseURL and HTTP before use.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	appID   string
	key     *rsa.PrivateKey
}

type publicationGuardKey struct{}

// WithPublicationGuard installs a final input-freshness check before each
// branch or pull request is created. The guard may fetch current issue state.
func WithPublicationGuard(ctx context.Context, guard func(context.Context) error) context.Context {
	return context.WithValue(ctx, publicationGuardKey{}, guard)
}

func checkPublicationGuard(ctx context.Context) error {
	if guard, ok := ctx.Value(publicationGuardKey{}).(func(context.Context) error); ok && guard != nil {
		return guard(ctx)
	}
	return nil
}

func NewClient(appID, key string) (*Client, error) {
	if strings.TrimSpace(appID) == "" {
		return nil, errors.New("GitHub App ID is required")
	}
	block, _ := pem.Decode([]byte(key))
	if block == nil {
		return nil, errors.New("GitHub App private key must be PEM encoded")
	}
	var private *rsa.PrivateKey
	if parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		private = parsed
	} else {
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("invalid GitHub App private key")
		}
		var ok bool
		private, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("GitHub App private key must be RSA")
		}
	}
	if err := private.Validate(); err != nil {
		return nil, errors.New("invalid GitHub App RSA private key")
	}
	return &Client{BaseURL: "https://api.github.com", HTTP: &http.Client{Timeout: 30 * time.Second}, appID: appID, key: private}, nil
}

func (c *Client) jwt() (string, error) {
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{"iss": c.appID, "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix()})
	encoded := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(encoded))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// APIError intentionally excludes request/response bodies, which can contain secrets.
type APIError struct {
	Method, Path string
	Status       int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub %s %s returned HTTP %d", e.Method, e.Path, e.Status)
}
func isStatus(err error, status int) bool {
	var api *APIError
	return errors.As(err, &api) && api.Status == status
}

func (c *Client) request(ctx context.Context, token, method, route string, in, out any) error {
	var reader io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+route, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "trajectory-github-app")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	transport := c.HTTP
	if transport == nil {
		transport = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := transport.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub %s request failed: %w", method, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &APIError{Method: method, Path: req.URL.Path, Status: res.StatusCode}
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return err
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode GitHub %s response: %w", method, err)
	}
	return nil
}

func splitRepo(repo string) (string, string, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return "", "", errors.New("repository must be owner/name")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", errors.New("invalid repository")
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return "", "", errors.New("invalid repository")
			}
		}
	}
	return parts[0], parts[1], nil
}
func repositoryRoute(repo string) string { return "/repos/" + repo }
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
func validFilePath(p string) bool {
	return p != "" && p != "." && !strings.HasPrefix(p, "/") && path.Clean(p) == p && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00")
}
func validBranch(p string) bool {
	if !validFilePath(p) || strings.Contains(p, "..") || strings.Contains(p, "@{") || strings.HasSuffix(p, ".") {
		return false
	}
	for _, r := range p {
		if r <= 32 || r == 127 || strings.ContainsRune("~^:?*[", r) {
			return false
		}
	}
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func (c *Client) installationToken(ctx context.Context, installation int64, repo string) (string, error) {
	_, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	if installation <= 0 {
		return "", errors.New("installation ID must be positive")
	}
	jwt, err := c.jwt()
	if err != nil {
		return "", err
	}
	var response struct {
		Token string `json:"token"`
	}
	err = c.request(ctx, jwt, http.MethodPost, "/app/installations/"+strconv.FormatInt(installation, 10)+"/access_tokens", map[string]any{
		"repositories": []string{name}, "permissions": map[string]string{"contents": "write", "issues": "write", "pull_requests": "write", "checks": "write"},
	}, &response)
	if err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", errors.New("GitHub returned an empty installation token")
	}
	return response.Token, nil
}

func (c *Client) Repository(ctx context.Context, installation int64, repo string) (baseBranch, baseSHA string, err error) {
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return "", "", err
	}
	var response struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err = c.request(ctx, token, http.MethodGet, repositoryRoute(repo), nil, &response); err != nil {
		return "", "", err
	}
	if !validBranch(response.DefaultBranch) {
		return "", "", errors.New("repository has no valid default branch")
	}
	sha, err := c.ref(ctx, token, repo, response.DefaultBranch)
	return response.DefaultBranch, sha, err
}
func (c *Client) Issue(ctx context.Context, installation int64, repo string, number int) (title, body string, err error) {
	if number <= 0 {
		return "", "", errors.New("issue number must be positive")
	}
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return "", "", err
	}
	var response struct {
		Title, Body string
		PullRequest json.RawMessage `json:"pull_request"`
	}
	err = c.request(ctx, token, http.MethodGet, repositoryRoute(repo)+"/issues/"+strconv.Itoa(number), nil, &response)
	if err == nil && len(response.PullRequest) > 0 && string(response.PullRequest) != "null" {
		err = errors.New("commands must target an issue, not a pull request")
	}
	return response.Title, response.Body, err
}
func (c *Client) CanWrite(ctx context.Context, installation int64, repo, user string) (bool, error) {
	if user == "" {
		return false, errors.New("GitHub user is required")
	}
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return false, err
	}
	var response struct {
		Permission string
		User       struct{ Permissions struct{ Push bool } }
	}
	err = c.request(ctx, token, http.MethodGet, repositoryRoute(repo)+"/collaborators/"+url.PathEscape(user)+"/permission", nil, &response)
	if isStatus(err, http.StatusNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return response.User.Permissions.Push || response.Permission == "write" || response.Permission == "admin" || response.Permission == "maintain", nil
}
func (c *Client) File(ctx context.Context, installation int64, repo, filePath, ref string) ([]byte, error) {
	if !validFilePath(filePath) || ref == "" {
		return nil, errors.New("valid file path and ref are required")
	}
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return nil, err
	}
	var response struct{ Type, Encoding, Content string }
	err = c.request(ctx, token, http.MethodGet, repositoryRoute(repo)+"/contents/"+escapePath(filePath)+"?"+url.Values{"ref": {ref}}.Encode(), nil, &response)
	if err != nil {
		return nil, err
	}
	if response.Type != "file" || response.Encoding != "base64" {
		return nil, errors.New("GitHub content is not a base64 file")
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(response.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("decode GitHub file: %w", err)
	}
	return data, nil
}
func (c *Client) Comment(ctx context.Context, installation int64, repo string, number int, body string) error {
	if number <= 0 {
		return errors.New("issue number must be positive")
	}
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return err
	}
	return c.request(ctx, token, http.MethodPost, repositoryRoute(repo)+"/issues/"+strconv.Itoa(number)+"/comments", map[string]string{"body": body}, nil)
}

type gitObject struct {
	SHA string `json:"sha"`
}
type gitCommit struct {
	SHA     string      `json:"sha"`
	Tree    gitObject   `json:"tree"`
	Parents []gitObject `json:"parents"`
}

func (c *Client) ref(ctx context.Context, token, repo, branch string) (string, error) {
	var response struct {
		Object gitObject `json:"object"`
	}
	err := c.request(ctx, token, http.MethodGet, repositoryRoute(repo)+"/git/ref/heads/"+escapePath(branch), nil, &response)
	if err != nil {
		return "", err
	}
	if response.Object.SHA == "" {
		return "", errors.New("GitHub returned an empty ref")
	}
	return response.Object.SHA, nil
}
func (c *Client) commit(ctx context.Context, token, repo, sha string) (gitCommit, error) {
	var response gitCommit
	err := c.request(ctx, token, http.MethodGet, repositoryRoute(repo)+"/git/commits/"+url.PathEscape(sha), nil, &response)
	if err == nil && response.Tree.SHA == "" {
		err = errors.New("GitHub returned a commit without a tree")
	}
	return response, err
}
func (c *Client) checkBase(ctx context.Context, token, repo, branch, expected string) error {
	current, err := c.ref(ctx, token, repo, branch)
	if err != nil {
		return err
	}
	if current != expected {
		return errors.New("base branch changed; create a fresh plan")
	}
	return nil
}
func (c *Client) reconcileBranch(ctx context.Context, token, repo, branch, baseSHA, treeSHA string) (bool, error) {
	sha, err := c.ref(ctx, token, repo, branch)
	if isStatus(err, http.StatusNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	commit, err := c.commit(ctx, token, repo, sha)
	if err != nil {
		return false, err
	}
	if commit.Tree.SHA != treeSHA || len(commit.Parents) != 1 || commit.Parents[0].SHA != baseSHA {
		return false, errors.New("publication branch conflict: existing branch differs from checked patch")
	}
	return true, nil
}

// Publish creates one base-derived commit and an app branch without ever updating
// an existing ref. Retries reconcile equivalent branches and existing PRs.
func (c *Client) Publish(ctx context.Context, installation int64, repo, baseBranch, baseSHA, branch, title, body string, files map[string][]byte) (string, error) {
	if !validBranch(baseBranch) || !validBranch(branch) || branch == baseBranch || baseSHA == "" || len(files) == 0 {
		return "", errors.New("publication requires distinct valid branches, a base SHA, and files")
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		if !validFilePath(p) || strings.HasPrefix(strings.ToLower(p), ".github/workflows/") {
			return "", errors.New("invalid or unsupported publication file path")
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return "", err
	}
	if err = c.checkBase(ctx, token, repo, baseBranch, baseSHA); err != nil {
		return "", err
	}
	base, err := c.commit(ctx, token, repo, baseSHA)
	if err != nil {
		return "", err
	}
	var original struct {
		Tree      []struct{ Path, Mode, Type string }
		Truncated bool
	}
	if err = c.request(ctx, token, http.MethodGet, repositoryRoute(repo)+"/git/trees/"+url.PathEscape(base.Tree.SHA)+"?recursive=1", nil, &original); err != nil {
		return "", err
	}
	if original.Truncated {
		return "", errors.New("base tree is truncated; cannot safely preserve file types")
	}
	modes := map[string]string{}
	for _, entry := range original.Tree {
		modes[entry.Path] = entry.Mode
	}
	type treeEntry struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	entries := make([]treeEntry, 0, len(paths))
	for _, p := range paths {
		mode := modes[p]
		if mode == "" {
			mode = "100644"
		}
		if mode != "100644" && mode != "100755" {
			return "", errors.New("publication can only replace regular files")
		}
		// A new nested file must not traverse an existing symlink, file, or submodule.
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if existing := modes[parent]; existing != "" && existing != "040000" {
				return "", errors.New("publication path traverses a non-directory")
			}
		}
		var blob gitObject
		if err = c.request(ctx, token, http.MethodPost, repositoryRoute(repo)+"/git/blobs", map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString(files[p])}, &blob); err != nil {
			return "", err
		}
		if blob.SHA == "" {
			return "", errors.New("GitHub returned an empty blob SHA")
		}
		entries = append(entries, treeEntry{Path: p, Mode: mode, Type: "blob", SHA: blob.SHA})
	}
	var tree gitObject
	if err = c.request(ctx, token, http.MethodPost, repositoryRoute(repo)+"/git/trees", map[string]any{"base_tree": base.Tree.SHA, "tree": entries}, &tree); err != nil {
		return "", err
	}
	if tree.SHA == "" || tree.SHA == base.Tree.SHA {
		return "", errors.New("publication produced no changes")
	}
	exists, err := c.reconcileBranch(ctx, token, repo, branch, baseSHA, tree.SHA)
	if err != nil {
		return "", err
	}
	if !exists {
		var created gitObject
		if err = c.request(ctx, token, http.MethodPost, repositoryRoute(repo)+"/git/commits", map[string]any{"message": title, "tree": tree.SHA, "parents": []string{baseSHA}}, &created); err != nil {
			return "", err
		}
		if created.SHA == "" {
			return "", errors.New("GitHub returned an empty commit SHA")
		}
		if err = c.checkBase(ctx, token, repo, baseBranch, baseSHA); err != nil {
			return "", err
		}
		if err = checkPublicationGuard(ctx); err != nil {
			return "", err
		}
		err = c.request(ctx, token, http.MethodPost, repositoryRoute(repo)+"/git/refs", map[string]string{"ref": "refs/heads/" + branch, "sha": created.SHA}, nil)
		if err != nil {
			recovered, reconcileErr := c.reconcileBranch(ctx, token, repo, branch, baseSHA, tree.SHA)
			if reconcileErr != nil {
				return "", reconcileErr
			}
			if !recovered {
				return "", err
			}
		}
	}
	if err = c.checkBase(ctx, token, repo, baseBranch, baseSHA); err != nil {
		return "", err
	}
	prURL, err := c.existingPR(ctx, token, repo, branch, baseBranch)
	if err != nil || prURL != "" {
		return prURL, err
	}
	var pr struct {
		HTMLURL string `json:"html_url"`
	}
	if err = checkPublicationGuard(ctx); err != nil {
		return "", err
	}
	err = c.request(ctx, token, http.MethodPost, repositoryRoute(repo)+"/pulls", map[string]any{"head": branch, "base": baseBranch, "title": title, "body": body, "draft": true}, &pr)
	if err != nil {
		if recovered, recoveryErr := c.existingPR(ctx, token, repo, branch, baseBranch); recoveryErr == nil && recovered != "" {
			return recovered, nil
		}
		return "", err
	}
	if pr.HTMLURL == "" {
		return "", errors.New("GitHub returned a pull request without a URL")
	}
	return pr.HTMLURL, nil
}

// Published recovers an existing publication without requiring its original
// issue or base branch inputs to remain current. It performs no repository writes.
func (c *Client) Published(ctx context.Context, installation int64, repo, baseBranch, branch string) (string, error) {
	if !validBranch(baseBranch) || !validBranch(branch) || branch == baseBranch {
		return "", errors.New("publication recovery requires distinct valid branches")
	}
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return "", err
	}
	return c.existingPR(ctx, token, repo, branch, baseBranch)
}

func (c *Client) existingPR(ctx context.Context, token, repo, branch, baseBranch string) (string, error) {
	owner, _, _ := splitRepo(repo)
	// Include closed PRs: replaying an old publish must not reopen completed work.
	query := url.Values{"head": {owner + ":" + branch}, "base": {baseBranch}, "state": {"all"}, "per_page": {"100"}}
	for page := 1; ; page++ {
		query.Set("page", strconv.Itoa(page))
		var prs []struct {
			HTMLURL string `json:"html_url"`
			Head    struct{ Ref string }
			Base    struct{ Ref string }
		}
		if err := c.request(ctx, token, http.MethodGet, repositoryRoute(repo)+"/pulls?"+query.Encode(), nil, &prs); err != nil {
			return "", err
		}
		for _, pr := range prs {
			if pr.Head.Ref == branch && pr.Base.Ref == baseBranch {
				if pr.HTMLURL == "" {
					return "", errors.New("existing pull request has no URL")
				}
				return pr.HTMLURL, nil
			}
		}
		if len(prs) < 100 {
			return "", nil
		}
	}
}

// ControlComment returns the GitHub ID so later task-list edits can be bound to it.
func (c *Client) ControlComment(ctx context.Context, installation int64, repo string, number int, body string) (int64, error) {
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return 0, err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	err = c.request(ctx, token, http.MethodPost, repositoryRoute(repo)+"/issues/"+strconv.Itoa(number)+"/comments", map[string]string{"body": body}, &out)
	if err != nil {
		return 0, err
	}
	if out.ID <= 0 {
		return 0, errors.New("GitHub returned an invalid comment ID")
	}
	return out.ID, nil
}

func (c *Client) PRHead(ctx context.Context, installation int64, repo, prURL string) (string, error) {
	prefix := "https://github.com/" + repo + "/pull/"
	if !strings.HasPrefix(prURL, prefix) {
		return "", errors.New("invalid pull request URL")
	}
	number, err := strconv.Atoi(strings.TrimPrefix(prURL, prefix))
	if err != nil || number <= 0 {
		return "", errors.New("invalid pull request number")
	}
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return "", err
	}
	var out struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err = c.request(ctx, token, http.MethodGet, repositoryRoute(repo)+"/pulls/"+strconv.Itoa(number), nil, &out); err != nil {
		return "", err
	}
	if out.Head.SHA == "" {
		return "", errors.New("GitHub returned an empty PR head")
	}
	return out.Head.SHA, nil
}

func decisionActions() []map[string]string {
	return []map[string]string{{"label": "Revalidate", "description": "Recheck inputs and JSON assertions", "identifier": "revalidate"}, {"label": "Post decision", "description": "Post the decision receipt to the issue", "identifier": "show_decision"}}
}

func (c *Client) CreateDecisionCheck(ctx context.Context, installation int64, repo, head, patch string) (int64, error) {
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return 0, err
	}
	body := map[string]any{"name": "Trajectory decision", "head_sha": head, "status": "completed", "conclusion": "neutral", "output": map[string]string{"title": "Decision recorded", "summary": "Repository-defined preimage and JSON checks passed. Behavioral sandbox checks are not connected.\n\nPatch SHA-256: `" + patch + "`"}, "actions": decisionActions()}
	var out struct {
		ID int64 `json:"id"`
	}
	err = c.request(ctx, token, http.MethodPost, repositoryRoute(repo)+"/check-runs", body, &out)
	if err != nil {
		return 0, err
	}
	if out.ID <= 0 {
		return 0, errors.New("GitHub returned an invalid check ID")
	}
	return out.ID, nil
}

// CurrentComment reads the canonical body to reject delayed checkbox events.
func (c *Client) CurrentComment(ctx context.Context, installation int64, repo string, id int64) (string, error) {
	if id <= 0 {
		return "", errors.New("invalid comment ID")
	}
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return "", err
	}
	var out struct {
		Body string `json:"body"`
	}
	err = c.request(ctx, token, http.MethodGet, repositoryRoute(repo)+"/issues/comments/"+strconv.FormatInt(id, 10), nil, &out)
	return out.Body, err
}

// VerifyPRPatch proves the PR head is a direct child of the pinned base and
// contains precisely the selected regular-file changes, without other edits.
func (c *Client) VerifyPRPatch(ctx context.Context, installation int64, repo, baseSHA, headSHA string, files map[string][]byte) error {
	if baseSHA == "" || headSHA == "" || len(files) == 0 {
		return errors.New("missing PR patch inputs")
	}
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return err
	}
	base, err := c.commit(ctx, token, repo, baseSHA)
	if err != nil {
		return err
	}
	head, err := c.commit(ctx, token, repo, headSHA)
	if err != nil {
		return err
	}
	if len(head.Parents) != 1 || head.Parents[0].SHA != baseSHA {
		return errors.New("PR head is not based directly on the selected base")
	}
	type entry struct{ Path, Mode, Type, SHA string }
	readTree := func(sha string) (map[string]entry, error) {
		var out struct {
			Tree      []entry `json:"tree"`
			Truncated bool    `json:"truncated"`
		}
		if err := c.request(ctx, token, http.MethodGet, repositoryRoute(repo)+"/git/trees/"+url.PathEscape(sha)+"?recursive=1", nil, &out); err != nil {
			return nil, err
		}
		if out.Truncated {
			return nil, errors.New("PR tree is truncated")
		}
		result := map[string]entry{}
		for _, e := range out.Tree {
			if e.Type != "tree" {
				if _, exists := result[e.Path]; exists {
					return nil, errors.New("duplicate tree path")
				}
				result[e.Path] = e
			}
		}
		return result, nil
	}
	before, err := readTree(base.Tree.SHA)
	if err != nil {
		return err
	}
	after, err := readTree(head.Tree.SHA)
	if err != nil {
		return err
	}
	for path, old := range before {
		if _, changed := files[path]; changed {
			continue
		}
		current, found := after[path]
		if !found || current.Mode != old.Mode || current.Type != old.Type || current.SHA != old.SHA {
			return fmt.Errorf("unexpected change in PR: %s", path)
		}
	}
	for path, current := range after {
		if _, changed := files[path]; changed {
			continue
		}
		if _, found := before[path]; !found {
			return fmt.Errorf("unexpected file in PR: %s", path)
		}
		_ = current
	}
	for path, expected := range files {
		current, found := after[path]
		if !found || current.Type != "blob" || (current.Mode != "100644" && current.Mode != "100755") {
			return fmt.Errorf("missing or non-regular PR file: %s", path)
		}
		if old, found := before[path]; found && (old.Type != "blob" || old.Mode != current.Mode) {
			return fmt.Errorf("PR file mode changed: %s", path)
		}
		actual, err := c.File(ctx, installation, repo, path, headSHA)
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, expected) {
			return fmt.Errorf("PR file differs from selected patch: %s", path)
		}
	}
	return nil
}

// UpdateDecisionCheck keeps the original check run and its requested-action buttons.
func (c *Client) UpdateDecisionCheck(ctx context.Context, installation int64, repo string, id int64, patch string) error {
	if id <= 0 {
		return errors.New("invalid check ID")
	}
	token, err := c.installationToken(ctx, installation, repo)
	if err != nil {
		return err
	}
	body := map[string]any{"status": "completed", "conclusion": "neutral", "actions": decisionActions(), "output": map[string]string{"title": "Decision revalidated", "summary": "Repository-defined preimage and JSON checks passed. Behavioral sandbox checks are not connected.\n\nPatch SHA-256: `" + patch + "`"}}
	return c.request(ctx, token, http.MethodPatch, repositoryRoute(repo)+"/check-runs/"+strconv.FormatInt(id, 10), body, nil)
}
