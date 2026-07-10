package mpc

import (
	"os"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

func init() {
	kmsEndpoint := strings.TrimSpace(os.Getenv("MPC_KMS_ENDPOINT"))
	if kmsEndpoint == "" {
		kmsEndpoint = "https://kms.hanzo.ai"
	}
	mpcEndpoint := strings.TrimSpace(os.Getenv("MPC_ENDPOINT"))
	apiKey := strings.TrimSpace(os.Getenv("MPC_API_KEY"))

	p := NewProcessor(Config{
		KMSEndpoint: kmsEndpoint,
		MPCEndpoint: mpcEndpoint,
		APIKey:      apiKey,
	})
	processor.Register(p)
}
