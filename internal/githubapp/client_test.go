package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	c, err := NewClient("1234", string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c.BaseURL, c.HTTP = server.URL, server.Client()
	return c, key
}

// Rejects broken JWT signing, wrong installation/repository scope, and using an app JWT for repository APIs.
func TestClientAuthenticationAndReads(t *testing.T) {
	var key *rsa.PrivateKey
	c, k := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app/installations/7/access_tokens" {
			if r.Method != "POST" {
				t.Errorf("token method %s", r.Method)
			}
			jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			parts := strings.Split(jwt, ".")
			if len(parts) != 3 {
				t.Errorf("invalid JWT")
				http.Error(w, "bad JWT", 401)
				return
			}
			signature, err := base64.RawURLEncoding.DecodeString(parts[2])
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
				t.Error(err)
			}
			claimsBytes, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims struct {
				Iss      string `json:"iss"`
				Iat, Exp int64
			}
			_ = json.Unmarshal(claimsBytes, &claims)
			now := time.Now().Unix()
			if claims.Iss != "1234" || claims.Iat > now || claims.Exp <= now || claims.Exp > now+600 {
				t.Errorf("invalid claims %+v", claims)
			}
			var request struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			if len(request.Repositories) != 1 || request.Repositories[0] != "project" || request.Permissions["contents"] != "write" || request.Permissions["issues"] != "write" || request.Permissions["pull_requests"] != "write" {
				t.Errorf("wrong token scope %+v", request)
			}
			fmt.Fprint(w, `{"token":"installation-token","expires_at":"2099-01-01T00:00:00Z"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer installation-token" {
			t.Error("repository request did not use installation token")
		}
		switch r.URL.Path {
		case "/repos/acme/project":
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case "/repos/acme/project/git/ref/heads/main":
			fmt.Fprint(w, `{"object":{"sha":"base"}}`)
		case "/repos/acme/project/issues/9":
			fmt.Fprint(w, `{"title":"Repair","body":"Details"}`)
		case "/repos/acme/project/collaborators/alice/permission":
			fmt.Fprint(w, `{"permission":"write","user":{"permissions":{"push":true}}}`)
		case "/repos/acme/project/collaborators/bob/permission":
			fmt.Fprint(w, `{"permission":"read","user":{"permissions":{"push":false}}}`)
		case "/repos/acme/project/contents/data.json":
			if r.URL.Query().Get("ref") != "base" {
				t.Error("file ref missing")
			}
			fmt.Fprint(w, `{"type":"file","encoding":"base64","content":"eyJvayI6dHJ1ZX0=\n"}`)
		case "/repos/acme/project/issues/9/comments":
			var v map[string]string
			_ = json.NewDecoder(r.Body).Decode(&v)
			if r.Method != "POST" || v["body"] != "receipt" {
				t.Error("wrong comment")
			}
			fmt.Fprint(w, `{"id":1}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	key = k
	ctx := context.Background()
	branch, sha, err := c.Repository(ctx, 7, "acme/project")
	if err != nil || branch != "main" || sha != "base" {
		t.Fatalf("Repository = %q %q %v", branch, sha, err)
	}
	title, body, err := c.Issue(ctx, 7, "acme/project", 9)
	if err != nil || title != "Repair" || body != "Details" {
		t.Fatalf("Issue = %q %q %v", title, body, err)
	}
	for user, want := range map[string]bool{"alice": true, "bob": false} {
		got, err := c.CanWrite(ctx, 7, "acme/project", user)
		if err != nil || got != want {
			t.Fatalf("CanWrite(%s) = %v %v", user, got, err)
		}
	}
	data, err := c.File(ctx, 7, "acme/project", "data.json", "base")
	if err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("File = %s %v", data, err)
	}
	if err := c.Comment(ctx, 7, "acme/project", 9, "receipt"); err != nil {
		t.Fatal(err)
	}
}

// A lost ref/PR response must recover the original resources, never overwrite a ref or duplicate a PR.
func TestPublishReconcilesRefAndPR(t *testing.T) {
	for _, lost := range []string{"", "ref", "pr"} {
		t.Run("lost_"+lost, func(t *testing.T) {
			ref, pr := false, false
			refWrites, prWrites := 0, 0
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/app/installations/7/access_tokens" {
					fmt.Fprint(w, `{"token":"token"}`)
					return
				}
				if r.Method == "PATCH" {
					t.Fatal("must never overwrite a ref")
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /repos/acme/project/git/ref/heads/main":
					fmt.Fprint(w, `{"object":{"sha":"base"}}`)
				case "GET /repos/acme/project/git/commits/base":
					fmt.Fprint(w, `{"sha":"base","tree":{"sha":"base-tree"},"parents":[]}`)
				case "GET /repos/acme/project/git/trees/base-tree":
					fmt.Fprint(w, `{"sha":"base-tree","tree":[{"path":"script.sh","mode":"100755","type":"blob","sha":"old"}],"truncated":false}`)
				case "POST /repos/acme/project/git/blobs":
					var b struct{ Content, Encoding string }
					_ = json.NewDecoder(r.Body).Decode(&b)
					if b.Encoding != "base64" || b.Content != "IyEvYmluL3NoCg==" {
						t.Errorf("blob changed bytes: %+v", b)
					}
					fmt.Fprint(w, `{"sha":"blob"}`)
				case "POST /repos/acme/project/git/trees":
					var b struct {
						Base string `json:"base_tree"`
						Tree []struct{ Path, Mode, Type, SHA string }
					}
					_ = json.NewDecoder(r.Body).Decode(&b)
					if b.Base != "base-tree" || len(b.Tree) != 1 || b.Tree[0].Mode != "100755" || b.Tree[0].SHA != "blob" {
						t.Errorf("wrong tree %+v", b)
					}
					fmt.Fprint(w, `{"sha":"new-tree"}`)
				case "GET /repos/acme/project/git/ref/heads/trajectory/plan-1":
					if ref {
						fmt.Fprint(w, `{"object":{"sha":"commit"}}`)
					} else {
						http.Error(w, `{"message":"Not Found"}`, 404)
					}
				case "GET /repos/acme/project/git/commits/commit":
					fmt.Fprint(w, `{"sha":"commit","tree":{"sha":"new-tree"},"parents":[{"sha":"base"}]}`)
				case "POST /repos/acme/project/git/commits":
					var b struct {
						Tree    string
						Parents []string
					}
					_ = json.NewDecoder(r.Body).Decode(&b)
					if b.Tree != "new-tree" || len(b.Parents) != 1 || b.Parents[0] != "base" {
						t.Errorf("wrong commit %+v", b)
					}
					fmt.Fprint(w, `{"sha":"commit"}`)
				case "POST /repos/acme/project/git/refs":
					var b struct{ Ref, SHA string }
					_ = json.NewDecoder(r.Body).Decode(&b)
					if b.Ref != "refs/heads/trajectory/plan-1" || b.SHA != "commit" {
						t.Errorf("wrong ref %+v", b)
					}
					ref = true
					refWrites++
					if lost == "ref" {
						http.Error(w, `{"message":"lost acknowledgement"}`, 500)
					} else {
						fmt.Fprint(w, `{"object":{"sha":"commit"}}`)
					}
				case "GET /repos/acme/project/pulls":
					if r.URL.Query().Get("head") != "acme:trajectory/plan-1" || r.URL.Query().Get("base") != "main" || r.URL.Query().Get("state") != "all" {
						t.Errorf("wrong PR lookup %s", r.URL)
					}
					if pr {
						fmt.Fprint(w, `[{"html_url":"https://github.com/acme/project/pull/1","head":{"ref":"trajectory/plan-1","sha":"commit"},"base":{"ref":"main"}}]`)
					} else {
						fmt.Fprint(w, `[]`)
					}
				case "POST /repos/acme/project/pulls":
					var b struct {
						Head, Base, Title, Body string
						Draft                   bool
					}
					_ = json.NewDecoder(r.Body).Decode(&b)
					if !b.Draft || b.Base != "main" || b.Head != "trajectory/plan-1" || b.Title != "Repair" || b.Body != "receipt" {
						t.Errorf("wrong PR %+v", b)
					}
					pr = true
					prWrites++
					if lost == "pr" {
						http.Error(w, `{"message":"lost acknowledgement"}`, 500)
					} else {
						fmt.Fprint(w, `{"html_url":"https://github.com/acme/project/pull/1"}`)
					}
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			})
			for i := 0; i < 2; i++ {
				got, err := c.Publish(context.Background(), 7, "acme/project", "main", "base", "trajectory/plan-1", "Repair", "receipt", map[string][]byte{"script.sh": []byte("#!/bin/sh\n")})
				if err != nil || got != "https://github.com/acme/project/pull/1" {
					t.Fatalf("Publish = %q %v", got, err)
				}
			}
			if refWrites != 1 || prWrites != 1 {
				t.Fatalf("duplicate publication: refs=%d PRs=%d", refWrites, prWrites)
			}
		})
	}
}

func TestPublishRejectsConflictingBranch(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /app/installations/7/access_tokens":
			fmt.Fprint(w, `{"token":"token"}`)
		case "GET /repos/acme/project/git/ref/heads/main":
			fmt.Fprint(w, `{"object":{"sha":"base"}}`)
		case "GET /repos/acme/project/git/commits/base":
			fmt.Fprint(w, `{"tree":{"sha":"base-tree"}}`)
		case "GET /repos/acme/project/git/trees/base-tree":
			fmt.Fprint(w, `{"tree":[],"truncated":false}`)
		case "POST /repos/acme/project/git/blobs":
			fmt.Fprint(w, `{"sha":"blob"}`)
		case "POST /repos/acme/project/git/trees":
			fmt.Fprint(w, `{"sha":"new-tree"}`)
		case "GET /repos/acme/project/git/ref/heads/trajectory/plan-1":
			fmt.Fprint(w, `{"object":{"sha":"other"}}`)
		case "GET /repos/acme/project/git/commits/other":
			fmt.Fprint(w, `{"tree":{"sha":"other-tree"},"parents":[{"sha":"base"}]}`)
		default:
			t.Errorf("unsafe unexpected request %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	})
	_, err := c.Publish(context.Background(), 7, "acme/project", "main", "base", "trajectory/plan-1", "Repair", "receipt", map[string][]byte{"a.txt": []byte("new")})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("wanted branch conflict, got %v", err)
	}
}

func TestNewClientRejectsInvalidKey(t *testing.T) {
	if _, err := NewClient("1234", "not a key"); err == nil {
		t.Fatal("accepted invalid key")
	}
}

// A moved base must stop all writes, and symlinks must not silently become regular files.
func TestPublishRejectsStaleBaseAndNonRegularFiles(t *testing.T) {
	for _, scenario := range []string{"stale", "symlink", "truncated"} {
		t.Run(scenario, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "POST /app/installations/7/access_tokens":
					fmt.Fprint(w, `{"token":"token"}`)
				case "GET /repos/acme/project/git/ref/heads/main":
					if scenario == "stale" {
						fmt.Fprint(w, `{"object":{"sha":"moved"}}`)
					} else {
						fmt.Fprint(w, `{"object":{"sha":"base"}}`)
					}
				case "GET /repos/acme/project/git/commits/base":
					fmt.Fprint(w, `{"tree":{"sha":"base-tree"}}`)
				case "GET /repos/acme/project/git/trees/base-tree":
					if scenario == "truncated" {
						fmt.Fprint(w, `{"tree":[],"truncated":true}`)
					} else {
						fmt.Fprint(w, `{"tree":[{"path":"a.txt","mode":"120000","type":"blob","sha":"link"}]}`)
					}
				default:
					t.Errorf("unsafe unexpected request %s %s", r.Method, r.URL)
					http.Error(w, "unexpected", 500)
				}
			})
			_, err := c.Publish(context.Background(), 7, "acme/project", "main", "base", "trajectory/plan-1", "Repair", "receipt", map[string][]byte{"a.txt": []byte("new")})
			if err == nil {
				t.Fatal("accepted unsafe publication")
			}
		})
	}
}

func TestCanWriteDeniesAbsentCollaborator(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/7/access_tokens" {
			fmt.Fprint(w, `{"token":"token"}`)
			return
		}
		http.Error(w, "private detail should not be returned", 404)
	})
	got, err := c.CanWrite(context.Background(), 7, "acme/project", "unknown")
	if err != nil || got {
		t.Fatalf("CanWrite = %v %v", got, err)
	}
}

func TestClientAcceptsPKCS8Key(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := NewClient("1234", string(raw)); err != nil {
		t.Fatal(err)
	}
}

// Issue freshness must be checked at each publication boundary, including when
// it changes between creating the branch and creating the pull request.
func TestPublicationGuardBlocksBranchAndPR(t *testing.T) {
	for _, stopAt := range []int{1, 2} {
		t.Run(fmt.Sprint(stopAt), func(t *testing.T) {
			refWrites, prWrites, guardCalls := 0, 0, 0
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "POST /app/installations/7/access_tokens":
					fmt.Fprint(w, `{"token":"token"}`)
				case "GET /repos/acme/project/git/ref/heads/main":
					fmt.Fprint(w, `{"object":{"sha":"base"}}`)
				case "GET /repos/acme/project/git/commits/base":
					fmt.Fprint(w, `{"tree":{"sha":"base-tree"}}`)
				case "GET /repos/acme/project/git/trees/base-tree":
					fmt.Fprint(w, `{"tree":[],"truncated":false}`)
				case "POST /repos/acme/project/git/blobs":
					fmt.Fprint(w, `{"sha":"blob"}`)
				case "POST /repos/acme/project/git/trees":
					fmt.Fprint(w, `{"sha":"new-tree"}`)
				case "GET /repos/acme/project/git/ref/heads/trajectory/plan-1":
					http.Error(w, `{"message":"Not Found"}`, 404)
				case "POST /repos/acme/project/git/commits":
					fmt.Fprint(w, `{"sha":"commit"}`)
				case "POST /repos/acme/project/git/refs":
					refWrites++
					fmt.Fprint(w, `{"object":{"sha":"commit"}}`)
				case "GET /repos/acme/project/pulls":
					fmt.Fprint(w, `[]`)
				case "POST /repos/acme/project/pulls":
					prWrites++
					fmt.Fprint(w, `{"html_url":"https://github.com/acme/project/pull/1"}`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.Error(w, "unexpected", 500)
				}
			})
			guardErr := fmt.Errorf("issue changed")
			ctx := WithPublicationGuard(context.Background(), func(ctx context.Context) error {
				guardCalls++
				if guardCalls == stopAt {
					return guardErr
				}
				return nil
			})
			_, err := c.Publish(ctx, 7, "acme/project", "main", "base", "trajectory/plan-1", "Repair", "receipt", map[string][]byte{"a.txt": []byte("new")})
			if err != guardErr {
				t.Fatalf("Publish error = %v, want guard rejection", err)
			}
			if guardCalls != stopAt || refWrites != stopAt-1 || prWrites != 0 {
				t.Fatalf("guard calls=%d refs=%d PRs=%d", guardCalls, refWrites, prWrites)
			}
		})
	}
}

// Recovery must find the prior PR without checking an input/base that may now be stale.
func TestPublishedRecoveryOnlyLooksUpExistingPR(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /app/installations/7/access_tokens":
			fmt.Fprint(w, `{"token":"token"}`)
		case "GET /repos/acme/project/pulls":
			if r.URL.Query().Get("head") != "acme:trajectory/plan-1" || r.URL.Query().Get("base") != "main" {
				t.Errorf("wrong recovery lookup %s", r.URL)
			}
			fmt.Fprint(w, `[{"html_url":"https://github.com/acme/project/pull/1","head":{"ref":"trajectory/plan-1"},"base":{"ref":"main"}}]`)
		default:
			t.Errorf("recovery performed unexpected request %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	})
	got, err := c.Published(context.Background(), 7, "acme/project", "main", "trajectory/plan-1")
	if err != nil || got != "https://github.com/acme/project/pull/1" {
		t.Fatalf("Published = %q %v", got, err)
	}
}

func TestNativeCheckPayloadAndHead(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/installations/7/access_tokens":
			var input struct {
				Permissions map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Permissions["checks"] != "write" {
				t.Error("missing checks permission")
			}
			fmt.Fprint(w, `{"token":"install"}`)
		case "/repos/acme/project/pulls/9":
			fmt.Fprint(w, `{"head":{"sha":"abc123"}}`)
		case "/repos/acme/project/check-runs":
			var input struct {
				Head       string `json:"head_sha"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				Actions    []struct {
					Identifier string `json:"identifier"`
				} `json:"actions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Head != "abc123" || input.Status != "completed" || input.Conclusion != "neutral" || len(input.Actions) != 2 || input.Actions[0].Identifier != "revalidate" || input.Actions[1].Identifier != "show_decision" {
				t.Errorf("invalid check request: %+v", input)
			}
			fmt.Fprint(w, `{"id":88}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	head, err := c.PRHead(ctx, 7, "acme/project", "https://github.com/acme/project/pull/9")
	if err != nil || head != "abc123" {
		t.Fatalf("head %q: %v", head, err)
	}
	if _, err := c.PRHead(ctx, 7, "acme/project", "https://evil.example/pull/9"); err == nil {
		t.Fatal("accepted foreign PR URL")
	}
	id, err := c.CreateDecisionCheck(ctx, 7, "acme/project", head, "patch")
	if err != nil || id != 88 {
		t.Fatalf("check %d: %v", id, err)
	}
}
