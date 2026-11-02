package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func postControl(t *testing.T, server *httptest.Server, path, body string, status int) {
	t.Helper()
	response, err := http.Post(server.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("control status %d expected %d (%s)", response.StatusCode, status, data)
	}
	var result map[string]any
	if json.Unmarshal(data, &result) != nil {
		t.Fatal("control did not return JSON object")
	}
}
func fixtureState(t *testing.T, server *httptest.Server) State {
	t.Helper()
	response, err := http.Get(server.URL + "/state")
	if err != nil {
		t.Fatal(err)
	}
	var state State
	decodeErr := json.NewDecoder(response.Body).Decode(&state)
	closeErr := response.Body.Close()
	if decodeErr != nil || closeErr != nil {
		t.Fatal("invalid state")
	}
	return state
}
func awaitState(t *testing.T, server *httptest.Server, check func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := fixtureState(t, server)
		if check(state) {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("state did not reach expected boundary")
	return State{}
}
func newFixture(t *testing.T, upstream http.Handler, maximum int64, hold time.Duration) (*httptest.Server, *httptest.Server) {
	t.Helper()
	target := httptest.NewServer(upstream)
	t.Cleanup(target.Close)
	proxy, err := NewProxy(target.URL, maximum, hold)
	if err != nil {
		t.Fatal(err)
	}
	control := httptest.NewServer(proxy.ControlHandler())
	t.Cleanup(control.Close)
	data := httptest.NewServer(proxy.DataHandler())
	t.Cleanup(data.Close)
	return data, control
}

func TestHoldReceivesWholeBodyThenBlocksBeforeForwardingAndMatchesOnlyOnce(t *testing.T) {
	var calls atomic.Int32
	data, control := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if string(body) != "fixture-source" {
			t.Errorf("body changed: %q", body)
		}
		if r.URL.RawQuery != "" && r.URL.RawQuery != "signature=kept%2Fvalue" {
			t.Error("signed query changed")
		}
		if r.Host != "signed.original:8334" {
			t.Errorf("signed Host changed: %q", r.Host)
		}
		if r.Header.Get("Authorization") != "fixture-signature" {
			t.Error("signature header changed")
		}
		w.WriteHeader(201)
	}), 1024, time.Second)
	postControl(t, control, "/arm", `{"id":"held","method":"PUT","path_prefix":"/evidence/","mode":"hold"}`, 200)
	reader, writer := io.Pipe()
	request, err := http.NewRequest("PUT", data.URL+"/evidence/source?signature=kept%2Fvalue", reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "signed.original:8334"
	request.Header.Set("Authorization", "fixture-signature")
	result := make(chan error, 1)
	go func() {
		response, e := http.DefaultClient.Do(request)
		if e == nil {
			if response.StatusCode != 201 {
				e = io.ErrUnexpectedEOF
			}
			_ = response.Body.Close()
		}
		result <- e
	}()
	if _, err = io.WriteString(writer, "fixture-"); err != nil {
		t.Fatal(err)
	}
	if fixtureState(t, control).Reached {
		t.Fatal("matched before full request body")
	}
	if _, err = io.WriteString(writer, "source"); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	awaitState(t, control, func(s State) bool { return s.Reached })
	if calls.Load() != 0 {
		t.Fatal("held request already reached upstream")
	}
	postControl(t, control, "/arm", `{"id":"other","method":"PUT","path_prefix":"/evidence/","mode":"hold"}`, 409)
	postControl(t, control, "/release", `{"id":"wrong"}`, 409)
	postControl(t, control, "/release", `{"id":"held"}`, 200)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	state := awaitState(t, control, func(s State) bool { return s.Completed })
	if !state.Forwarded || state.MatchedCount != 1 || state.UpstreamStatus != 201 {
		t.Fatalf("wrong state: %+v", state)
	}
	request, err = http.NewRequest("PUT", data.URL+"/evidence/source", strings.NewReader("fixture-source"))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "signed.original:8334"
	request.Header.Set("Authorization", "fixture-signature")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if calls.Load() != 2 || fixtureState(t, control).MatchedCount != 1 {
		t.Fatal("one-shot rule matched another request")
	}
}

func TestDropAcknowledgementCommitsUpstreamButReturnsNoResponse(t *testing.T) {
	var calls atomic.Int32
	data, control := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "fixture-upstream-ack")
	}), 1024, time.Second)
	postControl(t, control, "/arm", `{"id":"lost_ack","method":"PUT","path_suffix":"/head","header_name":"X-Amz-Meta-Kb-Deleted","header_value":"1","mode":"drop_ack"}`, 200)
	request, err := http.NewRequest("PUT", data.URL+"/journal/namespace/head", strings.NewReader("fixture-secret-body"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("x-amz-meta-kb-deleted", "1")
	response, err := http.DefaultClient.Do(request)
	if err == nil {
		_ = response.Body.Close()
		t.Fatal("lost acknowledgement returned an HTTP response")
	}
	state := awaitState(t, control, func(s State) bool { return s.Completed })
	if calls.Load() != 1 || !state.Reached || !state.Forwarded || state.UpstreamStatus != 200 {
		t.Fatalf("wrong drop state: %+v calls%d", state, calls.Load())
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-secret-body", "fixture-upstream-ack", "namespace", "X-Amz-Meta"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("state disclosed request data")
		}
	}
}

func TestProxyRejectsAmbiguousControlAndBoundsRequestBodies(t *testing.T) {
	var calls atomic.Int32
	data, control := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) }), 8, time.Second)
	for _, body := range []string{`{}`, `{"id":"x","method":"PUT","mode":"hold"}`, `{"id":"x","id":"y","method":"PUT","path_prefix":"/","mode":"hold"}`, `{"id":"x","method":"PUT","path_prefix":"/","mode":"hold","unknown":1}`, `{"id":"x","method":"PUT","path_prefix":"/","mode":"hold"} {}`, `{"id":"x","method":"PUT","path_prefix":"/","mode":"invalid"}`} {
		postControl(t, control, "/arm", body, 400)
	}
	response, err := http.Post(data.URL+"/evidence/key", "application/octet-stream", strings.NewReader("larger-than-eight"))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 413 || calls.Load() != 0 {
		t.Fatal("oversized body reached upstream")
	}
	postControl(t, control, "/arm", strings.Repeat("x", 4097), 400)
	postControl(t, control, "/release", `{"id":"missing"}`, 409)
}

func TestHoldTimeoutNeverForwardsMutation(t *testing.T) {
	var calls atomic.Int32
	data, control := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) }), 1024, 20*time.Millisecond)
	postControl(t, control, "/arm", `{"id":"expires","method":"PUT","path_prefix":"/evidence/","mode":"hold"}`, 200)
	request, err := http.NewRequest("PUT", data.URL+"/evidence/key", strings.NewReader("source"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 503 || calls.Load() != 0 {
		t.Fatal("timed out hold forwarded")
	}
	state := fixtureState(t, control)
	if !state.Completed || state.ErrorCode != "hold_timeout" || state.Forwarded {
		t.Fatalf("wrong timeout state: %+v", state)
	}
}
