package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"golang.org/x/crypto/nacl/secretbox"
)

// S3DeletionJournalConfig is independent of evidence/backup configuration. Keys
// are held outside graph dumps. Retain old key IDs until every retained journal
// envelope can be recovered; the active KeyID only controls new writes.
type S3DeletionJournalConfig struct {
	Endpoint, Region, Bucket, Prefix, Namespace, AccessKey, SecretKey string
	AddressingStyle, KeyID                                            string
	Keys                                                              map[string][32]byte
}

func (S3DeletionJournalConfig) String() string   { return "S3DeletionJournalConfig([redacted])" }
func (S3DeletionJournalConfig) GoString() string { return "S3DeletionJournalConfig([redacted])" }

type S3DeletionJournal struct {
	metadataMu                       sync.Mutex
	links                            map[string]journalLink
	indexed                          bool
	indexedHead                      domain.JournalBarrier
	operations                       map[string]journalOperation
	chain                            []domain.JournalBarrier
	qualificationMu                  sync.Mutex
	qualified                        bool
	client                           *s3.Client
	bucket, prefix, namespace, keyID string
	keys                             map[string][32]byte
}

func (*S3DeletionJournal) String() string   { return "S3DeletionJournal([redacted])" }
func (*S3DeletionJournal) GoString() string { return "S3DeletionJournal([redacted])" }

const journalMaxEnvelope = 4 << 20
const journalMaxWalk = 10000
const journalMaxRetries = 32

var journalID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,255}$`)

func NewS3DeletionJournal(c S3DeletionJournalConfig) (*S3DeletionJournal, error) {
	if err := ValidateS3DeletionJournalConfig(c); err != nil {
		return nil, err
	}
	keys := make(map[string][32]byte, len(c.Keys))
	for id, key := range c.Keys {
		keys[id] = key
	}
	client := s3.New(s3.Options{Region: c.Region, BaseEndpoint: aws.String(c.Endpoint), UsePathStyle: c.AddressingStyle == "path", Credentials: credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""), HTTPClient: &http.Client{Timeout: 30 * time.Second}, RetryMaxAttempts: 1})
	return &S3DeletionJournal{client: client, bucket: c.Bucket, prefix: c.Prefix, namespace: c.Namespace, keyID: c.KeyID, keys: keys}, nil
}
func ValidateS3DeletionJournalConfig(c S3DeletionJournalConfig) error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return domain.ErrInvalidInput
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()))) {
		return domain.ErrInvalidInput
	}
	if !journalID.MatchString(c.Region) || !journalID.MatchString(c.Bucket) || !journalID.MatchString(c.Namespace) || !journalID.MatchString(c.KeyID) || c.AccessKey == "" || c.SecretKey == "" || (c.AddressingStyle != "path" && c.AddressingStyle != "virtual") || len(c.Keys) == 0 || len(c.Keys) > 16 {
		return domain.ErrInvalidInput
	}
	for _, segment := range strings.Split(c.Prefix, "/") {
		if !journalID.MatchString(segment) || segment == "." || segment == ".." {
			return domain.ErrInvalidInput
		}
	}
	if _, ok := c.Keys[c.KeyID]; !ok {
		return domain.ErrInvalidInput
	}
	for id, key := range c.Keys {
		if !journalID.MatchString(id) || key == [32]byte{} {
			return domain.ErrInvalidInput
		}
	}
	return nil
}

// All protocol metadata is inside the authenticated body, including the key ID
// also used outside solely for key lookup. Secretbox authenticates the writer:
// possession of this secret grants both encryption and append authority.
type journalEnvelope struct {
	Version                  int
	KeyID, Nonce, Ciphertext string
}
type journalBody struct {
	Version                int
	Namespace, KeyID, Kind string
	Sequence               uint64
	PreviousManifestDigest string
	RecordDigest           string
	RecordKey              string
	Record                 *domain.JournalRecord `json:",omitempty"`
}
type journalHead struct {
	body    journalBody
	raw     []byte
	etag    string
	barrier domain.JournalBarrier
}

func journalDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func validJournalDigest(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == value
}
func (g *S3DeletionJournal) root() string    { return g.prefix + "/" + g.namespace + "/" }
func (g *S3DeletionJournal) headKey() string { return g.root() + "head" }
func (g *S3DeletionJournal) manifestKey(digest string) string {
	return g.root() + "manifests/" + digest
}
func (g *S3DeletionJournal) recordKey(digest string) string { return g.root() + "records/" + digest }
func (g *S3DeletionJournal) seal(body journalBody) ([]byte, error) {
	body.Version = 1
	body.Namespace = g.namespace
	body.KeyID = g.keyID
	plaintext, err := json.Marshal(body)
	if err != nil || len(plaintext) > journalMaxEnvelope/2 {
		return nil, domain.ErrBudget
	}
	var nonce [24]byte
	if _, err = io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, domain.ErrUnavailable
	}
	key := g.keys[g.keyID]
	ciphertext := secretbox.Seal(nil, plaintext, &nonce, &key)
	out, err := json.Marshal(journalEnvelope{Version: 1, KeyID: g.keyID, Nonce: base64.StdEncoding.EncodeToString(nonce[:]), Ciphertext: base64.StdEncoding.EncodeToString(ciphertext)})
	if err != nil || len(out) > journalMaxEnvelope {
		return nil, domain.ErrBudget
	}
	return out, nil
}
func decodeJournalJSON(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return domain.ErrUnavailable
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return domain.ErrUnavailable
	}
	return nil
}
func (g *S3DeletionJournal) open(raw []byte, kind string) (journalBody, error) {
	var body journalBody
	var env journalEnvelope
	if len(raw) > journalMaxEnvelope || decodeJournalJSON(raw, &env) != nil || env.Version != 1 {
		return body, domain.ErrUnavailable
	}
	key, ok := g.keys[env.KeyID]
	if !ok {
		return body, domain.ErrUnavailable
	}
	nonceBytes, err := base64.StdEncoding.Strict().DecodeString(env.Nonce)
	if err != nil || len(nonceBytes) != 24 {
		return body, domain.ErrUnavailable
	}
	ciphertext, err := base64.StdEncoding.Strict().DecodeString(env.Ciphertext)
	if err != nil || len(ciphertext) < secretbox.Overhead {
		return body, domain.ErrUnavailable
	}
	var nonce [24]byte
	copy(nonce[:], nonceBytes)
	plaintext, ok := secretbox.Open(nil, ciphertext, &nonce, &key)
	if !ok || decodeJournalJSON(plaintext, &body) != nil || body.Version != 1 || body.Namespace != g.namespace || body.KeyID != env.KeyID || body.Kind != kind {
		return journalBody{}, domain.ErrUnavailable
	}
	if body.Sequence > journalMaxWalk {
		return journalBody{}, domain.ErrBudget
	}
	if body.Sequence == 0 {
		if kind != "manifest" || body.PreviousManifestDigest != "" || body.RecordDigest != "" || body.RecordKey != "" || body.Record != nil {
			return journalBody{}, domain.ErrUnavailable
		}
	} else if !validJournalDigest(body.PreviousManifestDigest) {
		return journalBody{}, domain.ErrUnavailable
	}
	if kind == "manifest" && body.Sequence > 0 {
		if !validJournalDigest(body.RecordDigest) || body.RecordKey != g.recordKey(body.RecordDigest) || body.Record != nil {
			return journalBody{}, domain.ErrUnavailable
		}
	}
	if kind == "record" {
		if body.Record == nil || body.RecordKey != "" || body.RecordDigest != "" || validateJournalRecord(*body.Record) != nil {
			return journalBody{}, domain.ErrUnavailable
		}
	}
	return body, nil
}
func journalOpaqueID(id string) bool {
	return domain.ValidateIDs(id) == nil && utf8.ValidString(id)
}
func validateJournalRecord(r domain.JournalRecord) error {
	encoded, marshalErr := json.Marshal(r)
	if marshalErr != nil {
		return domain.ErrInvalidInput
	}
	if len(encoded) > domain.MaxJournalRecordBytes {
		return domain.ErrBudget
	}
	if r.RequestDigest != "" && !validJournalDigest(r.RequestDigest) {
		return domain.ErrInvalidInput
	}
	m := r.Manifest
	if r.Version != 1 || r.Action != "delete" || !journalID.MatchString(r.OperationID) || r.CreatedAt.IsZero() || r.CreatedAt.Location() != time.UTC || !journalOpaqueID(m.ProjectID) {
		return domain.ErrInvalidInput
	}
	n := len(m.IDs) + len(m.SourceDigests) + len(m.Artifacts) + len(m.Sessions) + len(m.Tasks)
	if n == 0 || n > domain.MaxDeletionEntries {
		return domain.ErrBudget
	}
	for _, values := range [][]string{m.IDs, m.Sessions, m.Tasks} {
		for _, id := range values {
			if !journalOpaqueID(id) {
				return domain.ErrInvalidInput
			}
		}
	}
	for _, digest := range m.SourceDigests {
		if !validJournalDigest(digest) {
			return domain.ErrInvalidInput
		}
	}
	for _, artifact := range m.Artifacts {
		if !journalOpaqueID(artifact.ID) || !journalOpaqueID(artifact.Generation) || !validJournalDigest(artifact.Digest) {
			return domain.ErrInvalidInput
		}
	}
	data, err := json.Marshal(m)
	if err != nil || journalDigest(data) != r.ManifestDigest {
		return domain.ErrInvalidInput
	}
	return nil
}
func (g *S3DeletionJournal) get(ctx context.Context, key string) ([]byte, string, error) {
	out, err := g.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, "", domain.ErrUnavailable
	}
	defer func() { _ = out.Body.Close() }()
	if out.ContentLength != nil && *out.ContentLength > journalMaxEnvelope {
		return nil, "", domain.ErrBudget
	}
	raw, err := io.ReadAll(io.LimitReader(out.Body, journalMaxEnvelope+1))
	if err != nil {
		return nil, "", domain.ErrUnavailable
	}
	if len(raw) > journalMaxEnvelope {
		return nil, "", domain.ErrBudget
	}
	if out.ETag == nil || *out.ETag == "" {
		return nil, "", domain.ErrUnavailable
	}
	return raw, *out.ETag, nil
}
func conditionalConflict(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && (api.ErrorCode() == "PreconditionFailed" || api.ErrorCode() == "ConditionalRequestConflict")
}
func (g *S3DeletionJournal) put(ctx context.Context, key string, raw []byte, etag string) error {
	input := &s3.PutObjectInput{Bucket: aws.String(g.bucket), Key: aws.String(key), Body: bytes.NewReader(raw), ContentLength: aws.Int64(int64(len(raw)))}
	if etag == "" {
		input.IfNoneMatch = aws.String("*")
	} else {
		input.IfMatch = aws.String(etag)
	}
	_, err := g.client.PutObject(ctx, input)
	if conditionalConflict(err) {
		return domain.ErrConflict
	}
	if err != nil {
		return domain.ErrUnknownOutcome
	}
	return nil
}
func (g *S3DeletionJournal) immutable(ctx context.Context, key string, raw []byte) error {
	err := g.put(ctx, key, raw, "")
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrUnknownOutcome) {
		existing, _, readErr := g.get(ctx, key)
		if readErr == nil && bytes.Equal(existing, raw) {
			return nil
		}
	}
	return err
}
func (g *S3DeletionJournal) freshHead(ctx context.Context) (journalHead, error) {
	if err := g.qualify(ctx); err != nil {
		return journalHead{}, err
	}
	raw, etag, err := g.get(ctx, g.headKey())
	if err != nil {
		return journalHead{}, err
	}
	body, err := g.open(raw, "manifest")
	if err != nil {
		return journalHead{}, err
	}
	return journalHead{body: body, raw: raw, etag: etag, barrier: domain.JournalBarrier{Namespace: g.namespace, Sequence: body.Sequence, Digest: journalDigest(raw)}}, nil
}

// Initialize is only for an explicit host-authorized fresh installation. A
// missing head during any read or append always fails closed.
func (g *S3DeletionJournal) Initialize(ctx context.Context) (domain.JournalBarrier, error) {
	if err := g.qualify(ctx); err != nil {
		return domain.JournalBarrier{}, err
	}
	raw, err := g.seal(journalBody{Kind: "manifest"})
	if err != nil {
		return domain.JournalBarrier{}, err
	}
	if err = g.immutable(ctx, g.manifestKey(journalDigest(raw)), raw); err != nil {
		return domain.JournalBarrier{}, err
	}
	if err = g.put(ctx, g.headKey(), raw, ""); err != nil {
		return domain.JournalBarrier{}, err
	}
	head, err := g.freshHead(ctx)
	if err != nil || !bytes.Equal(head.raw, raw) {
		return domain.JournalBarrier{}, domain.ErrUnknownOutcome
	}
	return head.barrier, nil
}
func (g *S3DeletionJournal) CurrentHead(ctx context.Context) (domain.JournalBarrier, error) {
	head, err := g.freshHead(ctx)
	if err != nil {
		return domain.JournalBarrier{}, err
	}
	return head.barrier, nil
}
func validBarrier(b domain.JournalBarrier, namespace string) bool {
	return b.Namespace == namespace && (validJournalDigest(b.Digest) || (b.Sequence == 0 && b.Digest == ""))
}
