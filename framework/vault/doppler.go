package vault

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"
)

// DopplerSecretItem represents an individual secret entry returned by Doppler API v3.
type DopplerSecretItem struct {
	Raw                string `json:"raw"`
	Computed           string `json:"computed"`
	Note               string `json:"note,omitempty"`
	RawVisibility      string `json:"rawVisibility,omitempty"`
	ComputedVisibility string `json:"computedVisibility,omitempty"`
}

// DopplerSecretsListResponse represents the payload of GET /v3/configs/config/secrets.
type DopplerSecretsListResponse struct {
	Secrets map[string]DopplerSecretItem `json:"secrets"`
}

// DopplerSecretSingleResponse represents the payload of GET /v3/configs/config/secret.
type DopplerSecretSingleResponse struct {
	Name  string            `json:"name"`
	Value DopplerSecretItem `json:"value"`
}

// DopplerErrorResponse represents an error payload returned by Doppler API.
type DopplerErrorResponse struct {
	Messages []string `json:"messages"`
	Success  bool     `json:"success"`
}

// DopplerProvider implements the VaultProvider interface for Doppler SecretOps.
type DopplerProvider struct {
	client  *fasthttp.Client
	token   string
	project string
	config  string
	baseURL string
	timeout time.Duration
}

// NewDopplerProvider creates a new DopplerProvider from DopplerConfig.
func NewDopplerProvider(cfg *DopplerConfig) (*DopplerProvider, error) {
	if cfg == nil {
		return nil, ErrMissingConfig
	}

	token := ""
	if cfg.Token != nil {
		token = cfg.Token.GetValue()
	}
	if token == "" {
		return nil, ErrMissingToken
	}

	project := ""
	if cfg.Project != nil {
		project = cfg.Project.GetValue()
	}

	config := ""
	if cfg.Config != nil {
		config = cfg.Config.GetValue()
	}

	baseURL := strings.TrimRight(cfg.GetBaseURL(), "/")
	timeout := time.Duration(cfg.GetTimeoutSec()) * time.Second

	client := &fasthttp.Client{
		Name:                "bifrost-doppler-vault-client",
		MaxConnsPerHost:     512,
		MaxIdleConnDuration: 60 * time.Second,
		ReadTimeout:         timeout,
		WriteTimeout:        timeout,
	}

	return &DopplerProvider{
		client:  client,
		token:   token,
		project: project,
		config:  config,
		baseURL: baseURL,
		timeout: timeout,
	}, nil
}

// GetSecret retrieves a single secret value from Doppler API (GET /v3/configs/config/secret).
func (d *DopplerProvider) GetSecret(ctx context.Context, project, config, name string) (string, error) {
	if name == "" {
		return "", ErrInvalidPath
	}

	proj := project
	if proj == "" {
		proj = d.project
	}
	cfg := config
	if cfg == "" {
		cfg = d.config
	}

	params := url.Values{}
	if proj != "" {
		params.Set("project", proj)
	}
	if cfg != "" {
		params.Set("config", cfg)
	}
	params.Set("name", name)

	reqURL := fmt.Sprintf("%s/v3/configs/config/secret?%s", d.baseURL, params.Encode())

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(reqURL)
	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.Set("Authorization", "Bearer "+d.token)
	req.Header.Set("Accept", "application/json")

	if err := d.doRequest(ctx, req, resp); err != nil {
		return "", err
	}

	statusCode := resp.StatusCode()
	if statusCode == fasthttp.StatusNotFound {
		return "", fmt.Errorf("%w: secret %q (project=%q, config=%q)", ErrSecretNotFound, name, proj, cfg)
	}
	if statusCode == fasthttp.StatusUnauthorized || statusCode == fasthttp.StatusForbidden {
		return "", ErrUnauthorized
	}
	if statusCode == fasthttp.StatusTooManyRequests {
		return "", ErrRateLimited
	}
	if statusCode != fasthttp.StatusOK {
		return "", d.parseAPIError(resp)
	}

	var result DopplerSecretSingleResponse
	if err := sonic.Unmarshal(resp.Body(), &result); err != nil {
		return "", fmt.Errorf("vault: failed to parse doppler secret response: %w", err)
	}

	if result.Value.Computed != "" {
		return result.Value.Computed, nil
	}
	return result.Value.Raw, nil
}

// ListSecrets retrieves all secrets for a project/config from Doppler API (GET /v3/configs/config/secrets).
func (d *DopplerProvider) ListSecrets(ctx context.Context, project, config string) (map[string]string, error) {
	proj := project
	if proj == "" {
		proj = d.project
	}
	cfg := config
	if cfg == "" {
		cfg = d.config
	}

	params := url.Values{}
	if proj != "" {
		params.Set("project", proj)
	}
	if cfg != "" {
		params.Set("config", cfg)
	}
	params.Set("include_dynamic_secrets", "false")
	params.Set("include_managed_secrets", "true")

	reqURL := fmt.Sprintf("%s/v3/configs/config/secrets?%s", d.baseURL, params.Encode())

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(reqURL)
	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.Set("Authorization", "Bearer "+d.token)
	req.Header.Set("Accept", "application/json")

	if err := d.doRequest(ctx, req, resp); err != nil {
		return nil, err
	}

	statusCode := resp.StatusCode()
	if statusCode == fasthttp.StatusNotFound {
		return nil, fmt.Errorf("%w: project %q or config %q not found", ErrSecretNotFound, proj, cfg)
	}
	if statusCode == fasthttp.StatusUnauthorized || statusCode == fasthttp.StatusForbidden {
		return nil, ErrUnauthorized
	}
	if statusCode == fasthttp.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if statusCode != fasthttp.StatusOK {
		return nil, d.parseAPIError(resp)
	}

	var result DopplerSecretsListResponse
	if err := sonic.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("vault: failed to parse doppler secrets list: %w", err)
	}

	secrets := make(map[string]string, len(result.Secrets))
	for k, item := range result.Secrets {
		if item.Computed != "" {
			secrets[k] = item.Computed
		} else {
			secrets[k] = item.Raw
		}
	}

	return secrets, nil
}

// SetSecret creates or updates a secret in Doppler (POST /v3/configs/config/secrets).
func (d *DopplerProvider) SetSecret(ctx context.Context, project, config, name, value string) error {
	if name == "" {
		return ErrInvalidPath
	}

	proj := project
	if proj == "" {
		proj = d.project
	}
	cfg := config
	if cfg == "" {
		cfg = d.config
	}

	params := url.Values{}
	if proj != "" {
		params.Set("project", proj)
	}
	if cfg != "" {
		params.Set("config", cfg)
	}

	reqURL := fmt.Sprintf("%s/v3/configs/config/secrets?%s", d.baseURL, params.Encode())

	payload := map[string]any{
		"secrets": map[string]string{
			name: value,
		},
	}
	bodyBytes, err := sonic.Marshal(payload)
	if err != nil {
		return fmt.Errorf("vault: failed to marshal set secret body: %w", err)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(reqURL)
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.Set("Authorization", "Bearer "+d.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.SetBody(bodyBytes)

	if err := d.doRequest(ctx, req, resp); err != nil {
		return err
	}

	statusCode := resp.StatusCode()
	if statusCode == fasthttp.StatusUnauthorized || statusCode == fasthttp.StatusForbidden {
		return ErrUnauthorized
	}
	if statusCode == fasthttp.StatusTooManyRequests {
		return ErrRateLimited
	}
	if statusCode != fasthttp.StatusOK && statusCode != fasthttp.StatusCreated {
		return d.parseAPIError(resp)
	}

	return nil
}

// DeleteSecret removes a secret from Doppler (DELETE /v3/configs/config/secret).
func (d *DopplerProvider) DeleteSecret(ctx context.Context, project, config, name string) error {
	if name == "" {
		return ErrInvalidPath
	}

	proj := project
	if proj == "" {
		proj = d.project
	}
	cfg := config
	if cfg == "" {
		cfg = d.config
	}

	params := url.Values{}
	if proj != "" {
		params.Set("project", proj)
	}
	if cfg != "" {
		params.Set("config", cfg)
	}
	params.Set("name", name)

	reqURL := fmt.Sprintf("%s/v3/configs/config/secret?%s", d.baseURL, params.Encode())

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(reqURL)
	req.Header.SetMethod(fasthttp.MethodDelete)
	req.Header.Set("Authorization", "Bearer "+d.token)
	req.Header.Set("Accept", "application/json")

	if err := d.doRequest(ctx, req, resp); err != nil {
		return err
	}

	statusCode := resp.StatusCode()
	if statusCode == fasthttp.StatusNotFound {
		// Secret already deleted or not found — treat as success for idempotence
		return nil
	}
	if statusCode == fasthttp.StatusUnauthorized || statusCode == fasthttp.StatusForbidden {
		return ErrUnauthorized
	}
	if statusCode == fasthttp.StatusTooManyRequests {
		return ErrRateLimited
	}
	if statusCode != fasthttp.StatusOK && statusCode != fasthttp.StatusNoContent {
		return d.parseAPIError(resp)
	}

	return nil
}

// Ping verifies connectivity and auth credentials with the Doppler API (GET /v3/me).
func (d *DopplerProvider) Ping(ctx context.Context) error {
	reqURL := fmt.Sprintf("%s/v3/me", d.baseURL)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(reqURL)
	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.Set("Authorization", "Bearer "+d.token)
	req.Header.Set("Accept", "application/json")

	if err := d.doRequest(ctx, req, resp); err != nil {
		return err
	}

	statusCode := resp.StatusCode()
	if statusCode == fasthttp.StatusUnauthorized || statusCode == fasthttp.StatusForbidden {
		return ErrUnauthorized
	}
	if statusCode != fasthttp.StatusOK {
		return d.parseAPIError(resp)
	}

	return nil
}

// Close closes idle HTTP connections.
func (d *DopplerProvider) Close() error {
	if d.client != nil {
		d.client.CloseIdleConnections()
	}
	return nil
}

func (d *DopplerProvider) doRequest(ctx context.Context, req *fasthttp.Request, resp *fasthttp.Response) error {
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		return d.client.DoDeadline(req, resp, deadline)
	}
	return d.client.DoTimeout(req, resp, d.timeout)
}

func (d *DopplerProvider) parseAPIError(resp *fasthttp.Response) error {
	var errResp DopplerErrorResponse
	if err := sonic.Unmarshal(resp.Body(), &errResp); err == nil && len(errResp.Messages) > 0 {
		return fmt.Errorf("vault: doppler API error (status %d): %s", resp.StatusCode(), strings.Join(errResp.Messages, ", "))
	}
	return fmt.Errorf("vault: doppler API request failed with status %d: %s", resp.StatusCode(), string(resp.Body()))
}
