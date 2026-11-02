package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway/memoryapi"
)

// The fetch API caps artifact offsets at 8 MiB. Test this against the OpenAPI
// contract and service constant so gateway and server cannot silently drift.

// Validate before unmarshalling into generated structs: encoding/json otherwise
// accepts missing required fields, null scalars, case aliases and duplicate keys.
// The generated response models use non-pointer fields for required properties,
// pointer fields for optional properties, and Valid methods for closed enums.
// Keep this gateway-local so untrusted DTOs never reach services or handlers.
func validMemoryResponse(body []byte, dest any) bool {
	if !utf8.Valid(body) || !validUnicodeEscapes(body) || !uniqueJSON(body) {
		return false
	}
	t := reflect.TypeOf(dest)
	return t != nil && t.Kind() == reflect.Pointer && validJSONValue(json.RawMessage(body), t.Elem(), "")
}

// encoding/json silently replaces unpaired UTF-16 surrogate escapes with U+FFFD.
// Reject them rather than changing quoted source text, IDs, or exact byte spans.
func validUnicodeEscapes(body []byte) bool {
	inside := false
	for i := 0; i < len(body); i++ {
		if body[i] == '"' {
			inside = !inside
			continue
		}
		if !inside || body[i] != '\\' {
			continue
		}
		i++
		if i >= len(body) {
			return false
		}
		if body[i] != 'u' {
			continue
		}
		if i+4 >= len(body) {
			return false
		}
		code, err := strconv.ParseUint(string(body[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if code >= 0xDC00 && code <= 0xDFFF {
			return false
		}
		if code >= 0xD800 && code <= 0xDBFF {
			if i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(body[i+3:i+7]), 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return !inside
}

// Duplicate JSON keys are ambiguous even if the final value is valid. Limit
// nesting before recursive traversal, and decode keys before comparing them so
// escaped spellings cannot evade the check.
func uniqueJSON(body []byte) bool {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if !uniqueJSONValue(d, 0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func uniqueJSONValue(d *json.Decoder, depth int) bool {
	if depth > 32 {
		return false
	}
	tok, err := d.Token()
	if err != nil {
		return false
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return false
			}
			name, ok := key.(string)
			if !ok {
				return false
			}
			if _, exists := seen[name]; exists {
				return false
			}
			seen[name] = struct{}{}
			if !uniqueJSONValue(d, depth+1) {
				return false
			}
		}
	case '[':
		for d.More() {
			if !uniqueJSONValue(d, depth+1) {
				return false
			}
		}
	default:
		return false
	}
	end, err := d.Token()
	return err == nil && (delim == '{' && end == json.Delim('}') || delim == '[' && end == json.Delim(']'))
}

var memoryTimeType = reflect.TypeOf(time.Time{})

func validJSONValue(raw json.RawMessage, typ reflect.Type, field string) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	if typ.Kind() == reflect.Pointer {
		return validJSONValue(raw, typ.Elem(), field)
	}
	if typ == memoryTimeType {
		var value time.Time
		return json.Unmarshal(raw, &value) == nil
	}
	if typ.Kind() == reflect.Struct {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return false
		}
		fields := make(map[string]reflect.StructField, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				return false
			}
			fields[name] = f
			if f.Type.Kind() != reflect.Pointer {
				if _, ok := object[name]; !ok {
					return false
				}
			}
		}
		for name, value := range object {
			f, ok := fields[name]
			if !ok || !validJSONValue(value, f.Type, name) {
				return false
			}
		}
		return true
	}
	if typ.Kind() == reflect.Slice {
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return false
		}
		if field == "entity_ids" && len(values) > domain.MaxGraphVisited {
			return false
		}
		for _, value := range values {
			if !validJSONValue(value, typ.Elem(), field) {
				return false
			}
		}
		return true
	}
	if typ.Kind() == reflect.Map {
		var values map[string]json.RawMessage
		if typ.Key().Kind() != reflect.String || json.Unmarshal(raw, &values) != nil || values == nil {
			return false
		}
		for _, value := range values {
			if !validJSONValue(value, typ.Elem(), "") {
				return false
			}
		}
		return true
	}
	value := reflect.New(typ)
	if json.Unmarshal(raw, value.Interface()) != nil {
		return false
	}
	if enum, ok := value.Elem().Interface().(interface{ Valid() bool }); ok && !enum.Valid() {
		return false
	}
	if typ.Kind() == reflect.String {
		s := value.Elem().String()
		if isOpaqueIDField(field) && strings.TrimSpace(s) == "" || field == "digest" && s == "" {
			return false
		}
	}
	if typ.Kind() == reflect.Int || typ.Kind() == reflect.Int64 {
		n := value.Elem().Int()
		if field == "visited" && (n < 0 || n > domain.MaxGraphVisited) || (field == "start_byte" || field == "end_byte" || field == "source_bytes") && n < 0 {
			return false
		}
	}
	return true
}

func isOpaqueIDField(field string) bool {
	switch field {
	case "project_id", "task_id", "session_id", "id", "revision", "record_id", "claim_id", "artifact_id", "source_revision", "client_id", "attestation_id", "depends_on", "entity_ids", "evidence_revision_ids", "authorized_project_ids":
		return true
	default:
		return false
	}
}

// These cross-field invariants are not expressible in the wire schema. An
// invalid passage must never be presented as an exact byte span of source text.
func validRetrievalResult(r memoryapi.RetrievalResult) bool {
	if r.NoResults && (len(r.Chunks) != 0 || r.TaskCheckpoint != nil || value(r.TaskStateOmitted)) ||
		!r.Truncated && (value(r.Continuation) != "" || r.TruncationReason != nil || value(r.TaskStateOmitted)) ||
		r.TaskCheckpoint != nil && value(r.TaskStateOmitted) {
		return false
	}
	for _, c := range r.Chunks {
		if c.Passage != nil {
			p := c.Passage
			if p.StartByte > p.EndByte || p.EndByte > p.SourceBytes || p.SourceBytes > domain.MaxArtifactBytes || p.EndByte-p.StartByte != int64(len(c.Text)) {
				return false
			}
		}
	}
	return true
}

func validRetrievalForRequest(r memoryapi.RetrievalResult, request domain.RetrievalRequest) bool {
	if request.MaxVisited > 0 && r.Visited > request.MaxVisited {
		return false
	}
	if r.TaskCheckpoint != nil {
		scope := scopeOf(r.TaskCheckpoint.Checkpoint.Scope)
		if request.Mode == domain.RetrievalEvidence || request.Scope.TaskID == "" || scope.ProjectID != request.Scope.ProjectID || scope.TaskID != request.Scope.TaskID || len(r.TaskCheckpoint.Checkpoint.EvidenceRevisionIds) != 0 {
			return false
		}
	}
	for _, c := range r.Chunks {
		if c.Passage != nil {
			return false
		}
		switch c.ClaimStatus {
		case memoryapi.ClaimStatusCandidate:
			if request.Mode != domain.RetrievalWorking || request.Scope.TaskID == "" || c.VerificationOutcome != nil || !slices.Contains(c.Warnings, "unverified_task_candidate") {
				return false
			}
		case memoryapi.ClaimStatusVerified, memoryapi.ClaimStatusDisputed:
		default:
			return false
		}
	}
	return true
}

// Passage cursors are publicly readable but untrusted. This only checks that
// their framing and byte offsets agree with the passage; only the remote
// service can authenticate the MAC and authorize the next request.
type passageOffsets struct {
	start, offset int64
}

func readPassageOffsets(cursor string) (passageOffsets, bool) {
	var out passageOffsets
	if len(cursor) > 2048 {
		return out, false
	}
	payloadText, tagText, ok := strings.Cut(cursor, ".")
	if !ok || payloadText == "" || tagText == "" || strings.Contains(tagText, ".") {
		return out, false
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(payloadText)
	if err != nil || !utf8.Valid(payload) || !validUnicodeEscapes(payload) || !uniqueJSON(payload) {
		return out, false
	}
	tag, err := base64.RawURLEncoding.Strict().DecodeString(tagText)
	if err != nil || len(tag) != sha256.Size {
		return out, false
	}
	// Pointers distinguish missing or null values from a legitimate zero.
	var fields struct {
		Kind   *string `json:"k"`
		Offset *int64  `json:"o"`
		Start  *int64  `json:"b"`
	}
	if json.Unmarshal(payload, &fields) != nil || fields.Kind == nil || *fields.Kind != "passage" || fields.Start == nil || fields.Offset == nil || *fields.Start < 0 || *fields.Offset <= *fields.Start || *fields.Offset > domain.MaxArtifactBytes {
		return out, false
	}
	return passageOffsets{start: *fields.Start, offset: *fields.Offset}, true
}

func validFetchResult(r memoryapi.RetrievalResult, request domain.FetchRequest) bool {
	if len(r.Chunks) != 1 || r.NoResults || r.Visited != 0 || r.TaskCheckpoint != nil || value(r.TaskStateOmitted) {
		return false
	}
	c := r.Chunks[0]
	if c.Passage == nil || c.RecordId != request.ArtifactID || c.ClaimStatus != memoryapi.ClaimStatusCandidate || c.OriginTrust != memoryapi.OriginTrustSubmitted || c.VerificationOutcome != nil || len(c.Evidence) != 1 || !slices.Contains(c.Warnings, "source_evidence_is_not_a_verified_claim") {
		return false
	}
	ref := c.Evidence[0]
	if ref.ArtifactId != request.ArtifactID || ref.Locator != request.Locator || ref.Digest != c.Revision || ref.Origin.Trust != memoryapi.OriginTrustSubmitted {
		return false
	}
	p := c.Passage
	if request.Mode == domain.FetchWorking && !slices.Contains(c.Warnings, "unverified_task_evidence") ||
		(p.StartByte != 0 || p.EndByte != p.SourceBytes) && !slices.Contains(c.Warnings, "partial_source_passage") {
		return false
	}
	// The first start may move forward by at most three UTF-8 continuation
	// bytes, including when an artifact itself starts with a split rune. A
	// signed continuation was cut at a rune boundary and must match exactly.
	originalStart := request.StartByte
	if request.Continuation != "" {
		previous, ok := readPassageOffsets(request.Continuation)
		if !ok || p.StartByte != previous.offset {
			return false
		}
		originalStart = previous.start
	} else if p.StartByte < request.StartByte || p.StartByte-request.StartByte > utf8.UTFMax-1 {
		return false
	}
	if request.EndByte != nil && p.EndByte > *request.EndByte {
		return false
	}
	if r.Continuation != nil {
		next, ok := readPassageOffsets(*r.Continuation)
		if !ok || next.start != originalStart || next.offset != p.EndByte || next.offset <= p.StartByte {
			return false
		}
	}
	return true
}
