package platform

import (
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"os"
)

func LoadObjectConfig() (gateway.S3Config, error) {
	return gateway.S3Config{Endpoint: os.Getenv("KB_S3_ENDPOINT"), Region: os.Getenv("KB_S3_REGION"), Bucket: os.Getenv("KB_S3_BUCKET"), AccessKey: os.Getenv("KB_S3_ACCESS_KEY"), SecretKey: os.Getenv("KB_S3_SECRET_KEY")}, nil
}
