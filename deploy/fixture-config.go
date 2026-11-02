//go:build ignore

// Synthetic compose fixture configuration. Never used in production or builds.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/btcsuite/btcd/btcec/v2"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fixture_configuration_failed")
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) == 3 && os.Args[1] == "buckets" {
		return createBuckets(os.Args[2])
	}
	if len(os.Args) != 2 || !filepath.IsAbs(os.Args[1]) {
		return fmt.Errorf("directory")
	}
	root := os.Args[1]
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		return fmt.Errorf("nonempty")
	}
	key, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}
	owner, err := btcec.NewPrivateKey()
	if err != nil {
		return err
	}
	token := func() string {
		var b [32]byte
		if _, e := rand.Read(b[:]); e != nil {
			panic(e)
		}
		return hex.EncodeToString(b[:])
	}
	objectAccess, objectSecret := token(), token()
	restoredAccess, restoredSecret := token(), token()
	journalAccess, journalSecret := token(), token()
	backupAccess, backupSecret := token(), token()
	var journalKey, manifestKey [32]byte
	if _, err = rand.Read(journalKey[:]); err != nil {
		return err
	}
	if _, err = rand.Read(manifestKey[:]); err != nil {
		return err
	}
	journalJSON, _ := json.Marshal(map[string]string{"journal-1": base64.StdEncoding.EncodeToString(journalKey[:])})
	backupJSON, _ := json.Marshal(map[string]any{"authority_key_id": "manifest-1", "authority_keys": map[string]string{"manifest-1": base64.StdEncoding.EncodeToString(manifestKey[:])}, "recipients": map[string]string{"age-1": key.Recipient().String()}, "identities": map[string]string{"age-1": key.String()}})
	project := "kbq-" + token()[:12]
	apiPort, objectPort, proxyPort := "0", "0", "0"
	config := map[string]string{
		"KB_RETRIEVAL_HMAC_KEY_CONTENTS": token(), "KB_GRAPH_USER": "neo4j", "KB_GRAPH_DATABASE": "neo4j", "KB_GRAPH_SECRET_CONTENTS": token(), "KB_MAINTENANCE_SECRET_CONTENTS": token(),
		"KB_S3_ENDPOINT": "http://127.0.0.1:8334", "KB_S3_REGION": "us-east-1", "KB_S3_BUCKET": "evidence", "KB_S3_ADDRESSING_STYLE": "path", "KB_S3_ACCESS_KEY_CONTENTS": objectAccess, "KB_S3_SECRET_KEY_CONTENTS": objectSecret,
		"KB_JOURNAL_ENDPOINT": "http://127.0.0.1:8334", "KB_JOURNAL_REGION": "us-east-1", "KB_JOURNAL_BUCKET": "journal", "KB_JOURNAL_PREFIX": "deletions", "KB_JOURNAL_NAMESPACE": project + "-journal", "KB_JOURNAL_ADDRESSING_STYLE": "path", "KB_JOURNAL_ACCESS_KEY_CONTENTS": journalAccess, "KB_JOURNAL_SECRET_KEY_CONTENTS": journalSecret, "KB_JOURNAL_KEY_ID": "journal-1", "KB_JOURNAL_KEYRING_CONTENTS": string(journalJSON),
		"KB_BACKUP_ENDPOINT": "http://127.0.0.1:8333", "KB_BACKUP_REGION": "us-east-1", "KB_BACKUP_BUCKET": "backups", "KB_BACKUP_ADDRESSING_STYLE": "path", "KB_BACKUP_NAMESPACE": project + "-backups", "KB_BACKUP_ACCESS_KEY_CONTENTS": backupAccess, "KB_BACKUP_SECRET_KEY_CONTENTS": backupSecret, "KB_BACKUP_KEYRING_CONTENTS": string(backupJSON), "KB_EVIDENCE_NAMESPACE": project + "-evidence", "KB_BACKUP_MAX_BYTES": "1073741824",
	}
	if err = writeEnv(filepath.Join(root, "runtime.env"), config); err != nil {
		return err
	}
	appConfig := map[string]string{}
	for k, v := range config {
		if !strings.HasPrefix(k, "KB_BACKUP_") && !strings.HasPrefix(k, "KB_SUPERVISOR_") && k != "KB_EVIDENCE_NAMESPACE" {
			appConfig[k] = v
		}
	}
	workerConfig := map[string]string{}
	for k, v := range appConfig {
		if k != "KB_RETRIEVAL_HMAC_KEY_CONTENTS" {
			workerConfig[k] = v
		}
	}
	workerConfig["KB_WORKER_ID"] = project + "-worker"
	if err = writeEnv(filepath.Join(root, "worker.env"), workerConfig); err != nil {
		return err
	}
	if err = writeEnv(filepath.Join(root, "api.env"), appConfig); err != nil {
		return err
	}
	restoredConfig := map[string]string{}
	for k, v := range config {
		restoredConfig[k] = v
	}
	restoredConfig["KB_RETRIEVAL_HMAC_KEY_CONTENTS"] = token()
	restoredConfig["KB_MAINTENANCE_SECRET_CONTENTS"] = token()
	restoredConfig["KB_SUPERVISOR_RESTORE_MODE"] = "1"
	restoredConfig["KB_S3_BUCKET"] = "restored-evidence"
	restoredConfig["KB_S3_ACCESS_KEY_CONTENTS"] = restoredAccess
	restoredConfig["KB_S3_SECRET_KEY_CONTENTS"] = restoredSecret
	restoredConfig["KB_EVIDENCE_NAMESPACE"] = project + "-restored-evidence"
	if err = writeEnv(filepath.Join(root, "runtime-restored.env"), restoredConfig); err != nil {
		return err
	}
	restoredAppConfig := map[string]string{}
	for k, v := range restoredConfig {
		if !strings.HasPrefix(k, "KB_BACKUP_") && !strings.HasPrefix(k, "KB_SUPERVISOR_") && k != "KB_EVIDENCE_NAMESPACE" {
			restoredAppConfig[k] = v
		}
	}
	restoredWorkerConfig := map[string]string{}
	for k, v := range restoredAppConfig {
		if k != "KB_RETRIEVAL_HMAC_KEY_CONTENTS" {
			restoredWorkerConfig[k] = v
		}
	}
	restoredWorkerConfig["KB_WORKER_ID"] = project + "-worker"
	if err = writeEnv(filepath.Join(root, "worker-restored.env"), restoredWorkerConfig); err != nil {
		return err
	}
	if err = writeEnv(filepath.Join(root, "api-restored.env"), restoredAppConfig); err != nil {
		return err
	}
	identities := []any{}
	for _, item := range [][3]string{{"evidence", objectAccess, objectSecret}, {"journal", journalAccess, journalSecret}, {"backups", backupAccess, backupSecret}, {"restored-evidence", restoredAccess, restoredSecret}} {
		identities = append(identities, map[string]any{"name": item[0], "credentials": []any{map[string]string{"accessKey": item[1], "secretKey": item[2]}}, "actions": []string{"Read:" + item[0], "Write:" + item[0], "List:" + item[0], "Tagging:" + item[0]}})
	}
	s3JSON, _ := json.Marshal(map[string]any{"identities": identities})
	if err = os.WriteFile(filepath.Join(root, "s3.json"), s3JSON, 0600); err != nil {
		return err
	}
	policy := []byte(`{"ProjectID":"fixture-project","ExpectedRevision":"","Policy":{"Revision":"fixture-policy-1","RawHistoryRetention":7776000000000000,"InactiveTaskRetention":2592000000000000,"BackupRetention":2592000000000000,"SpoolLimitBytes":10485760}}`)
	if err = os.WriteFile(filepath.Join(root, "policy.json"), policy, 0600); err != nil {
		return err
	}
	compose := map[string]string{"KB_FIXTURE_DIRECTORY": root, "KB_FIXTURE_ENV_FILE": filepath.Join(root, "runtime.env"), "KB_FIXTURE_API_PORT": apiPort, "KB_FIXTURE_API_ENV_FILE": filepath.Join(root, "api.env"), "KB_FIXTURE_WORKER_ENV_FILE": filepath.Join(root, "worker.env"), "KB_FIXTURE_OBJECT_PORT": objectPort, "KB_FIXTURE_PROXY_PORT": proxyPort}
	if err = writeEnv(filepath.Join(root, "compose.env"), compose); err != nil {
		return err
	}
	runtimeJSON, _ := json.Marshal(map[string]string{"api_url": "http://127.0.0.1:" + apiPort, "object_url": "http://127.0.0.1:" + objectPort, "proxy_url": "http://127.0.0.1:" + proxyPort, "credential_file": filepath.Join(root, "credential"), "compose_project": project, "config_file": filepath.Join(root, "compose.env"), "owner_public_key": hex.EncodeToString(owner.PubKey().SerializeCompressed()[1:]), "project_id": "fixture-project", "restore_graph_config_file": filepath.Join(root, "runtime-restored.env"), "restore_api_config_file": filepath.Join(root, "api-restored.env"), "restore_worker_config_file": filepath.Join(root, "worker-restored.env"), "restore_evidence_bucket": "restored-evidence"})
	return os.WriteFile(filepath.Join(root, "runtime.json"), runtimeJSON, 0600)
}
func writeEnv(path string, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		value := values[key]
		if strings.ContainsAny(value, "'\r\n") {
			return fmt.Errorf("env value")
		}
		fmt.Fprintf(&b, "%s='%s'\n", key, value)
	}
	return os.WriteFile(path, []byte(b.String()), 0600)
}

func createBuckets(root string) error {
	raw, err := os.ReadFile(filepath.Join(root, "runtime.json"))
	if err != nil {
		return err
	}
	var runtime map[string]string
	if err = json.Unmarshal(raw, &runtime); err != nil {
		return err
	}
	env, err := os.ReadFile(filepath.Join(root, "runtime.env"))
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(env), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			values[k] = strings.Trim(v, "'")
		}
	}
	for _, item := range [][3]string{{"evidence", "KB_S3_ACCESS_KEY_CONTENTS", "KB_S3_SECRET_KEY_CONTENTS"}, {"journal", "KB_JOURNAL_ACCESS_KEY_CONTENTS", "KB_JOURNAL_SECRET_KEY_CONTENTS"}, {"backups", "KB_BACKUP_ACCESS_KEY_CONTENTS", "KB_BACKUP_SECRET_KEY_CONTENTS"}} {
		client := s3.NewFromConfig(aws.Config{Region: "us-east-1", HTTPClient: &http.Client{Timeout: 5 * time.Second}, Credentials: credentials.NewStaticCredentialsProvider(values[item[1]], values[item[2]], "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(runtime["object_url"]); o.UsePathStyle = true })
		if _, err = client.HeadBucket(context.Background(), &s3.HeadBucketInput{Bucket: aws.String(item[0])}); err != nil {
			var response *smithyhttp.ResponseError
			if errors.As(err, &response) {
				fmt.Fprintf(os.Stderr, "fixture_bucket_status=%d\n", response.HTTPStatusCode())
			}
			return fmt.Errorf("bucket unavailable")
		}
	}
	return nil
}
