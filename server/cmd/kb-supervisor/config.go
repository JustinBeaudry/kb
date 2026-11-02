package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform"
)

type supervisorConfig struct {
	Root, ControlAddress, InstanceID, ControlSecret, EvidenceNamespace string
	Platform                                                           platform.Config
	Archive                                                            gateway.BackupArchiveConfig
	BackupStore                                                        gateway.S3Config
}

func backupSecret(name string) (string, error) {
	value, path := os.Getenv(name), os.Getenv(name+"_FILE")
	if path != "" {
		if value != "" {
			return "", domain.ErrInvalidInput
		}
		return platform.ReadProtectedSecret(path)
	}
	return value, nil
}
func loadSupervisorConfig() (supervisorConfig, error) {
	c := supervisorConfig{Root: os.Getenv("KB_SUPERVISOR_ROOT"), ControlAddress: os.Getenv("KB_SUPERVISOR_CONTROL_LISTEN"), InstanceID: os.Getenv("KB_SUPERVISOR_INSTANCE_ID"), EvidenceNamespace: os.Getenv("KB_EVIDENCE_NAMESPACE")}
	if c.Root == "" {
		c.Root = "/data/kb"
	}
	if c.ControlAddress == "" {
		c.ControlAddress = "0.0.0.0:8091"
	}
	if c.InstanceID == "" {
		c.InstanceID = "graph-supervisor"
	}
	if !filepath.IsAbs(c.Root) || domain.ValidateIDs(c.InstanceID) != nil {
		return c, domain.ErrInvalidInput
	}
	if mode := os.Getenv("KB_SUPERVISOR_RESTORE_MODE"); mode != "" && mode != "0" && mode != "1" {
		return c, domain.ErrInvalidInput
	}
	if _, _, e := net.SplitHostPort(c.ControlAddress); e != nil {
		return c, domain.ErrInvalidInput
	}
	var err error
	c.Platform, err = platform.LoadConfig()
	if err != nil {
		return c, err
	}
	c.ControlSecret, err = platform.ReadProtectedSecret(os.Getenv("KB_MAINTENANCE_SECRET_FILE"))
	if err != nil || len(c.ControlSecret) < 32 {
		return c, domain.ErrInvalidInput
	}
	access, err := backupSecret("KB_BACKUP_ACCESS_KEY")
	if err != nil {
		return c, err
	}
	secret, err := backupSecret("KB_BACKUP_SECRET_KEY")
	if err != nil {
		return c, err
	}
	c.BackupStore = gateway.S3Config{Endpoint: os.Getenv("KB_BACKUP_ENDPOINT"), Region: os.Getenv("KB_BACKUP_REGION"), Bucket: os.Getenv("KB_BACKUP_BUCKET"), AccessKey: access, SecretKey: secret, AddressingStyle: os.Getenv("KB_BACKUP_ADDRESSING_STYLE")}
	c.Archive, err = loadArchiveConfig(c.Root)
	return c, err
}
func loadArchiveConfig(root string) (gateway.BackupArchiveConfig, error) {
	c := gateway.BackupArchiveConfig{Namespace: os.Getenv("KB_BACKUP_NAMESPACE"), ScratchRoot: filepath.Join(root, "scratch"), MaxArchiveBytes: 4 << 30}
	if raw := os.Getenv("KB_BACKUP_MAX_BYTES"); raw != "" {
		n, e := strconv.ParseInt(raw, 10, 64)
		if e != nil {
			return c, domain.ErrInvalidInput
		}
		c.MaxArchiveBytes = n
	}
	text, e := platform.ReadProtectedSecret(os.Getenv("KB_BACKUP_KEYRING_FILE"))
	if e != nil {
		return c, e
	}
	var ring struct {
		AuthorityKeyID string            `json:"authority_key_id"`
		AuthorityKeys  map[string]string `json:"authority_keys"`
		Recipients     map[string]string `json:"recipients"`
		Identities     map[string]string `json:"identities"`
	}
	dec := json.NewDecoder(bytes.NewBufferString(text))
	dec.DisallowUnknownFields()
	if dec.Decode(&ring) != nil || dec.Decode(new(any)) != io.EOF {
		return c, domain.ErrInvalidInput
	}
	c.AuthorityKeyID = ring.AuthorityKeyID
	c.Recipients = ring.Recipients
	c.Identities = ring.Identities
	c.AuthorityKeys = map[string][32]byte{}
	for id, raw := range ring.AuthorityKeys {
		v, e := base64.StdEncoding.Strict().DecodeString(raw)
		if e != nil || len(v) != 32 {
			return c, domain.ErrInvalidInput
		}
		var key [32]byte
		copy(key[:], v)
		c.AuthorityKeys[id] = key
	}
	return c, nil
}
