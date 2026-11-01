package platform

import (
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"os"
)

func LoadObjectConfig() (gateway.S3Config, error) {
	access, err := secretSetting("KB_S3_ACCESS_KEY")
	if err != nil {
		return gateway.S3Config{}, err
	}
	secret, err := secretSetting("KB_S3_SECRET_KEY")
	if err != nil {
		return gateway.S3Config{}, err
	}
	return gateway.S3Config{Endpoint: os.Getenv("KB_S3_ENDPOINT"), Region: os.Getenv("KB_S3_REGION"), Bucket: os.Getenv("KB_S3_BUCKET"), AccessKey: access, SecretKey: secret, AddressingStyle: os.Getenv("KB_S3_ADDRESSING_STYLE")}, nil
}
