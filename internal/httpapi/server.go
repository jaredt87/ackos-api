package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jaredt87/ackOS/control"
	gitprovider "github.com/jaredt87/ackOS/integrations/git"
	"github.com/jaredt87/ackOS/kernel"
)

const (
	observationTTL = 5 * time.Minute
	maxBodyBytes   = 1 << 20
	providerName   = "git"
)

type Config struct {
	// Repositories maps caller-visible repository IDs to trusted local checkout roots.
	Repositories map[string]string
	BearerToken  string
	SigningKey   []byte
	AuthorityTTL time.Duration
}

type Server struct {
	repositories map[string]string
	bearerToken  string
	signingKey   []byte
	authorityTTL time.Duration

	mu       sync.Mutex
	sessions map[string]*resourceSession
	now      func() time.Time
}

type resourceSession struct {
	repositoryID string
	mu           sync.Mutex
	repo         *gitprovider.Repository
	provider     *gitprovider.FileProvider
	host         *control.Host
	runtime      *kernel.Runtime
}

type observationRequest struct {
	RepositoryID string `json:"repository_id"`
	Branch       string `json:"branch"`
	Path         string `json:"path"`
}

type changeRequest struct {
	ObservationToken string  `json:"observation_token"`
	Mode             string  `json:"mode"`
	ContentBase64    *string `json:"content_base64"`
}

type observationClaims struct {
	Version      int                 `json:"v"`
	RepositoryID string              `json:"repository_id"`
	Branch       string              `json:"branch"`
	Path         string              `json:"path"`
	ResourceID   string              `json:"resource_id"`
	Observation  observationSnapshot `json:"observation"`
	ExpiresAt    time.Time           `json:"expires_at"`
}

type observationSnapshot struct {
	Fingerprint      string    `json:"fingerprint"`
	Version          uint64    `json:"version"`
	ConcurrencyToken string    `json:"concurrency_token,omitempty"`
	ObservedAt       time.Time `json:"observed_at"`
}

type observationResponse struct {
	RepositoryID     string    `json:"repository_id"`
	Branch           string    `json:"branch"`
	Path             string    `json:"path"`
	ObservationToken string    `json:"observation_token"`
	ObservedAt       time.Time `json:"observed_at"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type changeResponse struct {
	ExecutionID string    `json:"execution_id"`
	Verified    bool      `json:"verified"`
	Committed   bool      `json:"committed"`
	CompletedAt time.Time `json:"completed_at"`
	State       string    `json:"state"`
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func New(cfg Config) (*Server, error) {
	if len(cfg.Repositories) == 0 {
		return nil, fmt.Errorf("at least one repository mapping is required")
	}
	if strings.TrimSpace(cfg.BearerToken) == "" {
		return nil, fmt.Errorf("bearer token is required")
	}
	if len(cfg.SigningKey) < 32 {
		return nil, fmt.Errorf("observation signing key must be at least 32 bytes")
	}
	for id, root := range cfg.Repositories {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(root) == "" {
			return nil, fmt.Errorf("repository IDs and roots must be non-empty")
		}
	}
	if cfg.AuthorityTTL < 0 {
		return nil, fmt.Errorf("authority TTL cannot be negative")
	}
	if cfg.AuthorityTTL == 0 {
		cfg.AuthorityTTL = 30 * time.Second
	}
	roots := make(map[string]string, len(cfg.Repositories))
	canonicalRoots := make(map[string]string, len(cfg.Repositories))
	for id, root := range cfg.Repositories {
		roots[id] = root
		canonical, err := canonicalRepositoryRoot(root)
		if err != nil {
			return nil, fmt.Errorf("canonicalize repository root for %q: %w", id, err)
		}
		if previousID, exists := canonicalRoots[canonical]; exists {
			return nil, fmt.Errorf("repository IDs %q and %q refer to the same checkout root", previousID, id)
		}
		canonicalRoots[canonical] = id
	}
	return &Server{
		repositories: roots,
		bearerToken:  cfg.BearerToken,
		signingKey:   append([]byte(nil), cfg.SigningKey...),
		authorityTTL: cfg.AuthorityTTL,
		sessions:     make(map[string]*resourceSession),
		now:          time.Now,
	}, nil
}

func canonicalRepositoryRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved), nil
	}
	return absolute, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("POST /v1/observations", s.authenticate(http.HandlerFunc(s.observe)))
	mux.Handle("POST /v1/changes", s.authenticate(http.HandlerFunc(s.change)))
	return mux
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if provided == "" || provided == r.Header.Get("Authorization") ||
			!hmac.Equal([]byte(provided), []byte(s.bearerToken)) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer authentication is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) observe(w http.ResponseWriter, r *http.Request) {
	var in observationRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if in.RepositoryID == "" || in.Branch == "" || in.Path == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "repository_id, branch, and path are required")
		return
	}
	root, ok := s.repositories[in.RepositoryID]
	if !ok {
		writeError(w, http.StatusNotFound, "repository_not_found", "repository ID is not configured")
		return
	}
	repo, err := gitprovider.Open(root)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "repository_unavailable", "configured repository could not be opened")
		return
	}
	ref := gitprovider.RefName("refs/heads/" + in.Branch)
	resourceID, err := repo.FileResourceID(ref, in.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_resource", "branch or file path is invalid")
		return
	}
	provider, err := gitprovider.NewFileProvider(repo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "provider_unavailable", "Git provider could not be initialized")
		return
	}

	_, observation, err := s.getOrCreateSession(r.Context(), in.RepositoryID, resourceID, provider, repo)
	if err != nil {
		writeError(w, http.StatusConflict, "observation_failed", "resource could not be observed")
		return
	}
	claims := observationClaims{
		Version:      1,
		RepositoryID: in.RepositoryID,
		Branch:       in.Branch,
		Path:         in.Path,
		ResourceID:   resourceID,
		Observation: observationSnapshot{
			Fingerprint:      observation.Resource.Fingerprint,
			Version:          observation.Version,
			ConcurrencyToken: observation.ConcurrencyToken,
			ObservedAt:       observation.ObservedAt,
		},
		ExpiresAt: s.now().UTC().Add(observationTTL),
	}
	token, err := s.signClaims(claims)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_creation_failed", "observation token could not be issued")
		return
	}
	writeJSON(w, http.StatusOK, observationResponse{
		RepositoryID:     in.RepositoryID,
		Branch:           in.Branch,
		Path:             in.Path,
		ObservationToken: token,
		ObservedAt:       observation.ObservedAt,
		ExpiresAt:        claims.ExpiresAt,
	})
}

func (s *Server) change(w http.ResponseWriter, r *http.Request) {
	var in changeRequest
	if err := decodeJSON(w, r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if in.ObservationToken == "" || in.ContentBase64 == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "observation_token and content_base64 are required")
		return
	}
	claims, err := s.verifyClaims(in.ObservationToken)
	if err != nil {
		writeError(w, http.StatusConflict, "invalid_observation", "observation token is invalid or expired; inspect the resource again")
		return
	}
	root, ok := s.repositories[claims.RepositoryID]
	if !ok {
		writeError(w, http.StatusNotFound, "repository_not_found", "repository ID is not configured")
		return
	}
	content, err := base64.StdEncoding.Strict().DecodeString(*in.ContentBase64)
	if err != nil || base64.StdEncoding.EncodeToString(content) != *in.ContentBase64 {
		writeError(w, http.StatusBadRequest, "invalid_content", "content_base64 must be canonical padded base64")
		return
	}
	if len(content) > maxBodyBytes/2 {
		writeError(w, http.StatusRequestEntityTooLarge, "content_too_large", "file content exceeds the API limit")
		return
	}
	fingerprint, err := gitprovider.FileStateFingerprint(in.Mode, content)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_mode", "mode must be 100644 or 100755")
		return
	}
	payload, err := gitprovider.EncodeFilePayload(in.Mode, content)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_content", "file payload could not be encoded")
		return
	}

	s.mu.Lock()
	key := sessionKey(claims.RepositoryID, claims.ResourceID)
	session := s.sessions[key]
	s.mu.Unlock()
	if session == nil {
		writeError(w, http.StatusConflict, "session_unavailable", "the observation session is no longer available; inspect the resource again")
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	s.mu.Lock()
	currentSession := s.sessions[key]
	s.mu.Unlock()
	if currentSession != session || session.repositoryID != claims.RepositoryID {
		writeError(w, http.StatusConflict, "session_unavailable", "observation session changed; inspect the resource again")
		return
	}
	if !s.now().UTC().Before(claims.ExpiresAt) {
		writeError(w, http.StatusConflict, "invalid_observation", "observation token is invalid or expired; inspect the resource again")
		return
	}
	if err := r.Context().Err(); err != nil {
		writeError(w, http.StatusRequestTimeout, "request_cancelled", "request was cancelled before execution")
		return
	}
	if claims.ResourceID == "" || claims.Observation.Fingerprint == "" {
		writeError(w, http.StatusConflict, "invalid_observation", "observation token does not contain a valid resource observation")
		return
	}
	if root == "" {
		writeError(w, http.StatusNotFound, "repository_not_found", "repository ID is not configured")
		return
	}
	ref := gitprovider.RefName("refs/heads/" + claims.Branch)
	resourceID, err := session.repo.FileResourceID(ref, claims.Path)
	if err != nil || resourceID != claims.ResourceID {
		writeError(w, http.StatusConflict, "invalid_observation", "observation token does not match the configured resource")
		return
	}
	expected := control.Observation{
		Resource:         control.ResourceRef{ID: claims.ResourceID, Fingerprint: claims.Observation.Fingerprint},
		Version:          claims.Observation.Version,
		ConcurrencyToken: claims.Observation.ConcurrencyToken,
		ObservedAt:       claims.Observation.ObservedAt,
	}
	result, err := session.host.Control(r.Context(), providerName, control.ControlRequest{
		Target:              control.ResourceRef{ID: claims.ResourceID, Fingerprint: claims.Observation.Fingerprint},
		Desired:             control.ResourceRef{ID: claims.ResourceID, Fingerprint: fingerprint},
		Payload:             payload,
		AuthorityTTL:        s.authorityTTL,
		ExpectedObservation: &expected,
	})
	if err != nil {
		if errors.Is(err, control.ErrStaleObservation) {
			writeError(w, http.StatusConflict, "stale_observation", "resource changed since inspection; inspect it again before proposing a new change")
			return
		}
		if session.runtime.Phase() == kernel.PhaseRecovery {
			writeError(w, http.StatusConflict, "recovery_required", "ackOS requires a fresh observation and recovery attempt before another change")
			return
		}
		writeError(w, http.StatusConflict, "control_rejected", "ackOS did not commit the requested change")
		return
	}
	writeJSON(w, http.StatusOK, changeResponse{
		ExecutionID: result.Execution.ExecutionID,
		Verified:    true,
		Committed:   true,
		CompletedAt: result.Verification.VerifiedAt,
		State:       "committed",
	})
}

func (s *Server) getOrCreateSession(ctx context.Context, repositoryID, resourceID string, provider *gitprovider.FileProvider, repo *gitprovider.Repository) (*resourceSession, control.Observation, error) {
	key := sessionKey(repositoryID, resourceID)
	for {
		s.mu.Lock()
		session := s.sessions[key]
		s.mu.Unlock()
		if session == nil {
			observation, err := provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: resourceID}})
			if err != nil {
				return nil, control.Observation{}, err
			}
			candidate, err := newResourceSession(repositoryID, repo, provider, observation.Resource.Fingerprint)
			if err != nil {
				return nil, control.Observation{}, err
			}
			s.mu.Lock()
			if s.sessions[key] == nil {
				s.sessions[key] = candidate
				s.mu.Unlock()
				return candidate, observation, nil
			}
			s.mu.Unlock()
			continue
		}

		session.mu.Lock()
		s.mu.Lock()
		current := s.sessions[key]
		s.mu.Unlock()
		if current != session {
			session.mu.Unlock()
			continue
		}
		observation, err := session.provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: resourceID}})
		if err != nil {
			session.mu.Unlock()
			return nil, control.Observation{}, err
		}
		phase := session.runtime.Phase()
		if (phase == kernel.PhaseIdle || phase == kernel.PhaseCommitted) && session.runtime.Root() != observation.Resource.Fingerprint {
			replacement, err := newResourceSession(repositoryID, session.repo, session.provider, observation.Resource.Fingerprint)
			if err != nil {
				session.mu.Unlock()
				return nil, control.Observation{}, err
			}
			s.mu.Lock()
			if s.sessions[key] != session {
				s.mu.Unlock()
				session.mu.Unlock()
				continue
			}
			s.sessions[key] = replacement
			s.mu.Unlock()
			session.mu.Unlock()
			return replacement, observation, nil
		}
		session.mu.Unlock()
		return session, observation, nil
	}
}

func sessionKey(repositoryID, resourceID string) string {
	return fmt.Sprintf("%q:%q", repositoryID, resourceID)
}

func newResourceSession(repositoryID string, repo *gitprovider.Repository, provider *gitprovider.FileProvider, initialFingerprint string) (*resourceSession, error) {
	runtime := kernel.NewRuntime(initialFingerprint, kernel.AllowPolicy{})
	host, err := control.NewHost(runtime, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if err := host.Register(providerName, provider); err != nil {
		return nil, err
	}
	return &resourceSession{repositoryID: repositoryID, repo: repo, provider: provider, host: host, runtime: runtime}, nil
}

func (s *Server) signClaims(claims observationClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.signingKey)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) verifyClaims(token string) (observationClaims, error) {
	var claims observationClaims
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return claims, fmt.Errorf("invalid token format")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, fmt.Errorf("invalid token signature")
	}
	mac := hmac.New(sha256.New, s.signingKey)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return claims, fmt.Errorf("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims, fmt.Errorf("invalid token payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		return claims, fmt.Errorf("invalid token payload")
	}
	if claims.Version != 1 || claims.ResourceID == "" || claims.Observation.Fingerprint == "" ||
		claims.ExpiresAt.IsZero() || !s.now().UTC().Before(claims.ExpiresAt) {
		return claims, fmt.Errorf("expired or incomplete observation token")
	}
	return claims, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON request")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request must contain exactly one JSON value")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: apiError{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
