// Test-only controlled S3 transport boundary. Never linked into production.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Rule struct {
	ID          string `json:"id"`
	Method      string `json:"method"`
	PathPrefix  string `json:"path_prefix,omitempty"`
	PathSuffix  string `json:"path_suffix,omitempty"`
	HeaderName  string `json:"header_name,omitempty"`
	HeaderValue string `json:"header_value,omitempty"`
	Mode        string `json:"mode"`
}
type State struct {
	ID             string `json:"id"`
	Mode           string `json:"mode"`
	Armed          bool   `json:"armed"`
	Reached        bool   `json:"reached"`
	Released       bool   `json:"released"`
	Forwarded      bool   `json:"forwarded"`
	Completed      bool   `json:"completed"`
	MatchedCount   int    `json:"matched_count"`
	UpstreamStatus int    `json:"upstream_status"`
	ErrorCode      string `json:"error_code"`
}
type activeRule struct {
	rule    Rule
	state   State
	release chan struct{}
}
type Proxy struct {
	mu       sync.Mutex
	upstream *url.URL
	client   *http.Client
	maximum  int64
	timeout  time.Duration
	active   *activeRule
	slots    chan struct{}
}

func NewProxy(upstream string, maximum int64, timeout time.Duration) (*Proxy, error) {
	endpoint, err := url.Parse(upstream)
	if err != nil || endpoint.Scheme != "http" || !net.ParseIP(endpoint.Hostname()).IsLoopback() || endpoint.Port() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") || maximum < 1 || maximum > 8<<20 || timeout <= 0 || timeout > 120*time.Second {
		return nil, errors.New("invalid_fixture_config")
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, MaxConnsPerHost: 4, ResponseHeaderTimeout: 30 * time.Second, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}
	return &Proxy{upstream: endpoint, maximum: maximum, timeout: timeout, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, slots: make(chan struct{}, 4)}, nil
}
func (p *Proxy) DataHandler() http.Handler { return http.HandlerFunc(p.data) }
func (p *Proxy) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			safeJSON(w, 405, map[string]string{"code": "method"})
			return
		}
		safeJSON(w, 200, map[string]bool{"live": true})
	})
	mux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			safeJSON(w, 405, map[string]string{"code": "method"})
			return
		}
		p.mu.Lock()
		var state State
		if p.active != nil {
			state = p.active.state
		}
		p.mu.Unlock()
		safeJSON(w, 200, state)
	})
	mux.HandleFunc("/arm", p.arm)
	mux.HandleFunc("/release", p.release)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { safeJSON(w, 404, map[string]string{"code": "missing"}) })
	return mux
}
func (p *Proxy) arm(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		safeJSON(w, 405, map[string]string{"code": "method"})
		return
	}
	var rule Rule
	if !strictJSON(w, r, &rule) || !validRule(rule) {
		safeJSON(w, 400, map[string]string{"code": "invalid_control"})
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active != nil && !p.active.state.Completed {
		safeJSON(w, 409, map[string]string{"code": "already_armed"})
		return
	}
	p.active = &activeRule{rule: rule, state: State{ID: rule.ID, Mode: rule.Mode, Armed: true}, release: make(chan struct{})}
	safeJSON(w, 200, map[string]bool{"armed": true})
}
func (p *Proxy) release(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		safeJSON(w, 405, map[string]string{"code": "method"})
		return
	}
	var input struct {
		ID string `json:"id"`
	}
	if !strictJSON(w, r, &input) || !validID(input.ID) {
		safeJSON(w, 400, map[string]string{"code": "invalid_control"})
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == nil || p.active.rule.ID != input.ID || p.active.rule.Mode != "hold" {
		safeJSON(w, 409, map[string]string{"code": "wrong_rule"})
		return
	}
	if !p.active.state.Released {
		close(p.active.release)
		p.active.state.Released = true
	}
	safeJSON(w, 200, map[string]bool{"released": true})
}
func validRule(rule Rule) bool {
	if !validID(rule.ID) || (rule.Mode != "hold" && rule.Mode != "drop_ack") || !oneOf(rule.Method, "GET", "HEAD", "PUT", "POST", "DELETE") || len(rule.PathPrefix) > 256 || len(rule.PathSuffix) > 256 || len(rule.HeaderName) > 128 || len(rule.HeaderValue) > 256 {
		return false
	}
	if rule.PathPrefix == "" && rule.PathSuffix == "" && rule.HeaderName == "" {
		return false
	}
	if rule.PathPrefix != "" && !strings.HasPrefix(rule.PathPrefix, "/") {
		return false
	}
	if strings.ContainsAny(rule.PathPrefix+rule.PathSuffix+rule.HeaderValue, "\r\n\x00") {
		return false
	}
	if (rule.HeaderName == "") != (rule.HeaderValue == "") {
		return false
	}
	for _, r := range rule.HeaderName {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!#$%&'*+-.^_`|~", r) {
			return false
		}
	}
	return true
}
func validID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-", r) {
			return false
		}
	}
	return true
}
func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
func strictJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	defer func() { _ = r.Body.Close() }()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		return false
	}
	// Flat command objects reject duplicates in addition to unknown fields.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return false
		}
		seen[key] = true
		var field json.RawMessage
		if decoder.Decode(&field) != nil {
			return false
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		return false
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil
}
func safeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (p *Proxy) selectRule(r *http.Request) *activeRule {
	p.mu.Lock()
	defer p.mu.Unlock()
	active := p.active
	if active == nil || !active.state.Armed || active.state.Reached || active.state.Completed {
		return nil
	}
	rule := active.rule
	if rule.Method != r.Method || !strings.HasPrefix(r.URL.EscapedPath(), rule.PathPrefix) || !strings.HasSuffix(r.URL.EscapedPath(), rule.PathSuffix) || (rule.HeaderName != "" && r.Header.Get(rule.HeaderName) != rule.HeaderValue) {
		return nil
	}
	active.state.Reached = true
	active.state.MatchedCount++
	return active
}
func (p *Proxy) update(active *activeRule, change func(*State)) {
	if active == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == active {
		change(&active.state)
	}
}
func (p *Proxy) data(w http.ResponseWriter, r *http.Request) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		safeJSON(w, 503, map[string]string{"code": "fixture_busy"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), p.timeout)
	defer cancel()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, p.maximum))
	_ = r.Body.Close()
	if err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			safeJSON(w, 413, map[string]string{"code": "body_limit"})
		} else {
			safeJSON(w, 400, map[string]string{"code": "body_unavailable"})
		}
		return
	}
	active := p.selectRule(r)
	if active != nil {
		defer p.update(active, func(state *State) { state.Armed = false; state.Completed = true })
	}
	if active != nil && active.rule.Mode == "hold" {
		select {
		case <-active.release:
		case <-ctx.Done():
			p.update(active, func(state *State) { state.ErrorCode = "hold_timeout" })
			safeJSON(w, 503, map[string]string{"code": "hold_timeout"})
			return
		}
	}
	if ctx.Err() != nil {
		p.update(active, func(state *State) { state.ErrorCode = "request_canceled" })
		safeJSON(w, 503, map[string]string{"code": "request_canceled"})
		return
	}
	outgoing := r.Clone(ctx)
	outgoing.URL.Scheme = p.upstream.Scheme
	outgoing.URL.Host = p.upstream.Host
	outgoing.RequestURI = ""
	outgoing.Host = r.Host
	outgoing.Body = io.NopCloser(bytes.NewReader(body))
	outgoing.ContentLength = int64(len(body))
	outgoing.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	outgoing.Header = r.Header.Clone()
	for _, header := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Upgrade", "Te", "Trailer", "Transfer-Encoding"} {
		outgoing.Header.Del(header)
	}
	p.update(active, func(state *State) { state.Forwarded = true })
	response, err := p.client.Do(outgoing)
	if err != nil {
		p.update(active, func(state *State) { state.ErrorCode = "upstream_unavailable" })
		safeJSON(w, 502, map[string]string{"code": "upstream_unavailable"})
		return
	}
	defer func() { _ = response.Body.Close() }()
	p.update(active, func(state *State) { state.UpstreamStatus = response.StatusCode })
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, p.maximum+1))
	if err != nil || int64(len(responseBody)) > p.maximum {
		p.update(active, func(state *State) { state.ErrorCode = "upstream_body_unavailable" })
		safeJSON(w, 502, map[string]string{"code": "upstream_body_unavailable"})
		return
	}
	if active != nil && active.rule.Mode == "drop_ack" {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			p.update(active, func(state *State) { state.ErrorCode = "hijack_unavailable" })
			safeJSON(w, 502, map[string]string{"code": "hijack_unavailable"})
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			p.update(active, func(state *State) { state.ErrorCode = "hijack_unavailable" })
			return
		}
		_ = connection.Close()
		return
	}
	for key, values := range response.Header {
		if oneOf(http.CanonicalHeaderKey(key), "Connection", "Transfer-Encoding", "Trailer") {
			continue
		}
		w.Header()[key] = append([]string(nil), values...)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}
