package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type MaintenanceHTTPClient struct {
	endpoint, secret, instance string
	client                     *http.Client
}

func NewMaintenanceHTTPClient(endpoint, secret, instance string) (*MaintenanceHTTPClient, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || len(secret) < 32 || instance == "" || len(instance) > 128 {
		return nil, domain.ErrInvalidInput
	}
	return &MaintenanceHTTPClient{endpoint: strings.TrimSuffix(endpoint, "/"), secret: secret, instance: instance, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *MaintenanceHTTPClient) Admit(ctx context.Context, instance, kind string) (domain.OperationPermit, error) {
	if instance != c.instance {
		return domain.OperationPermit{}, domain.ErrForbidden
	}
	if strings.HasPrefix(kind, "host-admin:") || strings.Contains(kind, "recovery") {
		return domain.OperationPermit{}, domain.ErrForbidden
	}
	var p domain.OperationPermit
	err := c.request(ctx, http.MethodPost, "/v1/admit", struct {
		InstanceID string `json:"instance_id"`
		Kind       string `json:"kind"`
	}{instance, kind}, &p)
	if err == nil && (p.ID == "" || p.InstanceID != instance || p.Kind != kind || p.Generation == 0 || p.Unknown) {
		err = domain.ErrUnavailable
	}
	return p, err
}
func (c *MaintenanceHTTPClient) Finish(ctx context.Context, p domain.OperationPermit) error {
	if p.InstanceID != c.instance {
		return domain.ErrForbidden
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := c.request(ctx, http.MethodPost, "/v1/finish", p, &result); err != nil {
		return err
	}
	if !result.OK {
		return domain.ErrUnavailable
	}
	return nil
}
func (c *MaintenanceHTTPClient) State(ctx context.Context) (domain.MaintenanceState, error) {
	var s domain.MaintenanceState
	err := c.request(ctx, http.MethodGet, "/v1/state", nil, &s)
	return s, err
}
func (c *MaintenanceHTTPClient) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return domain.ErrInvalidInput
		}
		body = bytes.NewReader(data)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return domain.ErrUnavailable
	}
	r.Header.Set("Authorization", "Bearer "+c.secret)
	r.Header.Set("Content-Type", "application/json")
	if id, ok := ctx.Value(maintenanceRequestIDKey{}).(string); ok {
		r.Header.Set("X-Request-Id", id)
	}
	response, err := c.client.Do(r)
	if err != nil {
		return domain.ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, (32<<10)+1))
	if err != nil || len(data) > 32<<10 {
		return domain.ErrUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if response.StatusCode != http.StatusOK {
		var result struct {
			Code string `json:"code"`
		}
		if d.Decode(&result) != nil || d.Decode(&struct{}{}) != io.EOF {
			return domain.ErrUnavailable
		}
		switch result.Code {
		case "unauthorized":
			return domain.ErrUnauthorized
		case "forbidden":
			return domain.ErrForbidden
		case "invalid_input":
			return domain.ErrInvalidInput
		case "stale":
			return domain.ErrStale
		case "unknown_outcome":
			return domain.ErrUnknownOutcome
		case "maintenance":
			return domain.ErrMaintenance
		}
		return domain.ErrUnavailable
	}
	if d.Decode(output) != nil || d.Decode(&struct{}{}) != io.EOF {
		return domain.ErrUnavailable
	}
	return nil
}

type maintenanceRequestIDKey struct{}
