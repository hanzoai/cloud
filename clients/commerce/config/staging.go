package config

import "os"

// Staging Settings
func Staging() *Config {
	config := Production()

	config.ProjectId = "crowdstart-staging"
	config.IsStaging = true

	config.Prefixes["analytics"] = "/"
	config.Prefixes["api"] = "/v1/"
	config.Prefixes["dash"] = "/"
	config.Prefixes["default"] = "/"

	config.Hosts["analytics"] = "analytics-staging.hanzo.ai"
	config.Hosts["api"] = "api-staging.hanzo.ai"
	config.Hosts["dash"] = "dash-staging.hanzo.ai"
	config.Hosts["default"] = "default-staging.hanzo.ai"

	config.StaticUrl = "//static-staging.hanzo.ai"

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

	config.Paypal.Email = "dev@hanzo.ai"
	config.Paypal.Api = "https://svcs.sandbox.paypal.com"
	config.Paypal.IpnUrl = "https://api-staging.hanzo.ai/paypal/ipn/"
	config.Paypal.PaypalIpnUrl = "https://www.sandbox.paypal.com/cgi-bin/webscr"

	config.Facebook.AppId = os.Getenv("FACEBOOK_APP_ID")
	config.Facebook.AppSecret = os.Getenv("FACEBOOK_APP_SECRET")
	config.Facebook.GraphVersion = "v2.2"

	config.Email.Provider.Mandrill.APIKey = os.Getenv("MANDRILL_API_KEY")

	config.Google.APIKey = os.Getenv("GOOGLE_API_KEY")
	config.Google.Bucket.ImageUploads = "crowdstart-staging-image-uploads"

	config.Salesforce.ConsumerKey = os.Getenv("SALESFORCE_CONSUMER_KEY")
	config.Salesforce.ConsumerSecret = os.Getenv("SALESFORCE_CONSUMER_SECRET")
	config.Salesforce.CallbackURL = "https:" + config.UrlFor("dash", "/salesforce/callback")

	config.Netlify.AccessToken = os.Getenv("NETLIFY_ACCESS_TOKEN")

	return config
}
