package platform

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"golang.org/x/sys/unix"
)

// LoadJournalConfig never falls back to evidence or backup credentials. Key
// bytes are loaded from a bounded regular owner-only file, separately recovered
// from graph backups; no filename, secret or vendor failure enters its errors.
func LoadJournalConfig() (gateway.S3DeletionJournalConfig, error) {
	access, err := secretSetting("KB_JOURNAL_ACCESS_KEY")
	if err != nil {
		return gateway.S3DeletionJournalConfig{}, err
	}
	secret, err := secretSetting("KB_JOURNAL_SECRET_KEY")
	if err != nil {
		return gateway.S3DeletionJournalConfig{}, err
	}
	cfg := gateway.S3DeletionJournalConfig{Endpoint: os.Getenv("KB_JOURNAL_ENDPOINT"), Region: os.Getenv("KB_JOURNAL_REGION"), Bucket: os.Getenv("KB_JOURNAL_BUCKET"), Prefix: os.Getenv("KB_JOURNAL_PREFIX"), Namespace: os.Getenv("KB_JOURNAL_NAMESPACE"), AccessKey: access, SecretKey: secret, AddressingStyle: os.Getenv("KB_JOURNAL_ADDRESSING_STYLE"), KeyID: os.Getenv("KB_JOURNAL_KEY_ID")}
	keys, err := loadJournalKeyring(os.Getenv("KB_JOURNAL_KEYRING_FILE"))
	if err != nil {
		return gateway.S3DeletionJournalConfig{}, err
	}
	cfg.Keys = keys
	if err = gateway.ValidateS3DeletionJournalConfig(cfg); err != nil {
		return gateway.S3DeletionJournalConfig{}, err
	}
	return cfg, nil
}
func loadJournalKeyring(path string) (map[string][32]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, domain.ErrInvalidInput
	}
	// O_NOFOLLOW keeps the file identity and permission check on the opened inode.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, domain.ErrInvalidInput
	}
	file := os.NewFile(uintptr(fd), "journal-keyring")
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return nil, domain.ErrInvalidInput
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, domain.ErrInvalidInput
	}
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(data) > 16384 {
		return nil, domain.ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, domain.ErrInvalidInput
	}
	keys := map[string][32]byte{}
	for decoder.More() {
		token, tokenErr := decoder.Token()
		id, valid := token.(string)
		if tokenErr != nil || !valid {
			return nil, domain.ErrInvalidInput
		}
		if _, exists := keys[id]; exists || len(keys) >= 16 {
			return nil, domain.ErrInvalidInput
		}
		var encoded string
		if decoder.Decode(&encoded) != nil {
			return nil, domain.ErrInvalidInput
		}
		decoded, decodeErr := base64.StdEncoding.Strict().DecodeString(encoded)
		if decodeErr != nil || len(decoded) != 32 {
			return nil, domain.ErrInvalidInput
		}
		var key [32]byte
		copy(key[:], decoded)
		keys[id] = key
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, domain.ErrInvalidInput
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return nil, domain.ErrInvalidInput
	}
	return keys, nil
}
