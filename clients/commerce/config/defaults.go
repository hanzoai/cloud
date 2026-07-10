package config

import (
	"os"
	"path/filepath"

	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
)

// envOrDefault returns the value of the environment variable named by the key,
// or fallback if the variable is not set or is empty.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Default settings
func Defaults() *Config {
	config := new(Config)

	config.ProjectId = "crowdstart-us"
	config.Protocol = "//" // Protocol relative
	config.Hosts = make(map[string]string, 10)
	config.Prefixes = make(map[string]string, 10)
	config.RootDir, _ = filepath.Abs(cwd + "/../..")
	config.SiteTitle = "Hanzo"
	config.DashboardUrl = "https://dash3.hanzo.ai"

	config.Fee = 0.05

	config.Secret = os.Getenv("SESSION_SECRET")
	config.SessionName = "session"

	config.DemoMode = demoMode

	config.Email = struct {
		From     email.Email
		ReplyTo  email.Email
		Provider integration.Integration
	}{
		email.Email{
			Name:    "Hanzo",
			Address: "platform@hanzo.ai",
		},
		email.Email{
			Name:    "Hanzo",
			Address: "noreply@hanzo.ai",
		},
		integration.Integration{
			Mandrill: integration.Mandrill{
				APIKey: os.Getenv("MANDRILL_API_KEY"),
			},
		},
	}

	config.Ethereum.TestPassword = os.Getenv("ETHEREUM_TEST_PASSWORD")
	config.Ethereum.DepositPassword = os.Getenv("ETHEREUM_DEPOSIT_PASSWORD")
	config.Ethereum.MainNetNodes = []string{envOrDefault("ETHEREUM_MAINNET_NODE", "http://35.193.184.247:13264")}
	config.Ethereum.TestNetNodes = []string{envOrDefault("ETHEREUM_TESTNET_NODE", "https://api.infura.io/v1/jsonrpc/ropsten")}
	config.Ethereum.WebhookPassword = os.Getenv("ETHEREUM_WEBHOOK_PASSWORD")

	config.Bitcoin.TestPassword = os.Getenv("BITCOIN_TEST_PASSWORD")
	config.Bitcoin.DepositPassword = os.Getenv("BITCOIN_DEPOSIT_PASSWORD")
	config.Bitcoin.MainNetNodes = []string{envOrDefault("BITCOIN_MAINNET_NODE", "http://35.192.49.112:19283")}
	config.Bitcoin.MainNetUsernames = []string{os.Getenv("BITCOIN_MAINNET_USERNAME")}
	config.Bitcoin.MainNetPasswords = []string{os.Getenv("BITCOIN_MAINNET_PASSWORD")}
	config.Bitcoin.TestNetNodes = []string{envOrDefault("BITCOIN_TESTNET_NODE", "http://104.154.51.133:19283")}
	config.Bitcoin.TestNetUsernames = []string{os.Getenv("BITCOIN_TESTNET_USERNAME")}
	config.Bitcoin.TestNetPasswords = []string{os.Getenv("BITCOIN_TESTNET_PASSWORD")}
	config.Bitcoin.WebhookPassword = os.Getenv("BITCOIN_WEBHOOK_PASSWORD")

	config.Mercury.WebhookSecret = os.Getenv("MERCURY_WEBHOOK_SECRET")

	config.Paypal.Email = "dev@hanzo.ai"
	config.Paypal.Api = "https://svcs.sandbox.paypal.com"
	config.Paypal.IpnUrl = "https://api.staging.hanzo.ai/paypal/ipn/"
	config.Paypal.PaypalIpnUrl = "https://www.sandbox.paypal.com/cgi-bin/webscr"

	config.Mandrill.FromName = "Hanzo"
	config.Mandrill.FromEmail = "noreply@hanzo.ai"

	config.Netlify.BaseUrl = "https://api.netlify.com/api/v1"
	config.Netlify.ClientId = os.Getenv("NETLIFY_CLIENT_ID")
	config.Netlify.Secret = os.Getenv("NETLIFY_SECRET")

	config.Cloudflare.Email = "dev@hanzo.ai"
	config.Cloudflare.Key = os.Getenv("CLOUDFLARE_API_KEY")
	config.Cloudflare.Zone = "hanzo.ai"

	config.SMTPRelay.Endpoint = "https://smtprelay.hanzo.ai"
	config.SMTPRelay.Username = "admin@hanzo.ai"
	config.SMTPRelay.Password = os.Getenv("SMTP_RELAY_PASSWORD")

	return config
}
