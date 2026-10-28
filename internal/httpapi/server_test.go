package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestObserveThenChangeUsesAckOSGitProvider(t *testing.T) {
	root := initGitRepository(t)
	api, err := New(Config{
		Repositories: map[string]string{"test-repo": root},
		BearerToken:  "test-client-token",
		SigningKey:   []byte(strings.Repeat("s", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := api.Handler()

	observed := postJSON(t, handler, "/v1/observations", map[string]string{
		"repository_id": "test-repo",
		"branch":        "main",
		"path":          "README.md",
	}, true)
	if observed.Code != http.StatusOK {
		t.Fatalf("observe status = %d, body = %s", observed.Code, observed.Body.String())
	}
	var observation observationResponse
	if err := json.Unmarshal(observed.Body.Bytes(), &observation); err != nil {
		t.Fatal(err)
	}
	if observation.ObservationToken == "" {
		t.Fatal("observe response did not contain an opaque token")
	}

	want := []byte("# updated by ackOS API\n")
	changed := postJSON(t, handler, "/v1/changes", changeRequest{
		ObservationToken: observation.ObservationToken,
		Mode:             "100644",
		ContentBase64:    base64.StdEncoding.EncodeToString(want),
	}, true)
	if changed.Code != http.StatusOK {
		t.Fatalf("change status = %d, body = %s", changed.Code, changed.Body.String())
	}
	var result changeResponse
	if err := json.Unmarshal(changed.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Verified || !result.Committed || result.ExecutionID == "" {
		t.Fatalf("unexpected change result: %+v", result)
	}

	cmd := exec.Command("git", "-C", root, "show", "refs/heads/main:README.md")
	got, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("committed file = %q, want %q", got, want)
	}

	reused := postJSON(t, handler, "/v1/changes", changeRequest{
		ObservationToken: observation.ObservationToken,
		Mode:             "100644",
		ContentBase64:    base64.StdEncoding.EncodeToString([]byte("# replay\n")),
	}, true)
	if reused.Code != http.StatusConflict {
		t.Fatalf("replayed stale token status = %d, body = %s; want 409", reused.Code, reused.Body.String())
	}
}

func TestMutationRoutesRequireBearerToken(t *testing.T) {
	api, err := New(Config{
		Repositories: map[string]string{"test-repo": "/unused"},
		BearerToken:  "secret",
		SigningKey:   []byte(strings.Repeat("s", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/observations", strings.NewReader(`{"repository_id":"test-repo","branch":"main","path":"README.md"}`))
	response := httptest.NewRecorder()
	api.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", response.Code)
	}
}

func TestObservationTokenRejectsTamperingAndExpiry(t *testing.T) {
	api, err := New(Config{
		Repositories: map[string]string{"test-repo": "/unused"},
		BearerToken:  "secret",
		SigningKey:   []byte(strings.Repeat("s", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	claims := observationClaims{
		Version:      1,
		RepositoryID: "test-repo",
		Branch:       "main",
		Path:         "README.md",
		ResourceID:   "resource",
		Observation:  observationSnapshot{Fingerprint: "fp", ObservedAt: api.now().UTC()},
		ExpiresAt:    api.now().UTC().Add(observationTTL),
	}
	token, err := api.signClaims(claims)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.verifyClaims(token); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if _, err := api.verifyClaims(token + "x"); err == nil {
		t.Fatal("tampered token was accepted")
	}
	claims.ExpiresAt = api.now().UTC().Add(-time.Second)
	expired, err := api.signClaims(claims)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.verifyClaims(expired); err == nil {
		t.Fatal("expired token was accepted")
	}
}

func postJSON(t *testing.T, handler http.Handler, path string, value any, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	if authenticated {
		request.Header.Set("Authorization", "Bearer test-client-token")
	}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func initGitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, output)
		}
	}
	if output, err := exec.Command("git", "init", "-b", "main", root).CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, output)
	}
	runGit("config", "user.name", "ackOS API test")
	runGit("config", "user.email", "ackos-api-test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial")
	return root
}
