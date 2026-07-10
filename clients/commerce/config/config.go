package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
)

var demoMode = true
var cachedConfig *Config

// Env is the current environment (development, test, sandbox, staging, production)
var Env = os.Getenv("ENV")

// The current working dir is config/development due to how we split
// development and production app.yaml files so we need to check two places for
// config.json based on which module is trying to load it.
var cwd, _ = os.Getwd()
var configFileLocations = []string{
	cwd + "/../../../../config.json",
	cwd + "/../../../config.json",
	cwd + "/../../config.json",
	cwd + "/../config.json",
	cwd + "/config.json",
}

type Config struct {
	AutoCompileAssets bool
	AutoLoadFixtures  bool
	CookieDomain      string
	DatastoreWarn     bool
	DemoMode          bool
	IsDevelopment     bool
	IsTest            bool
	IsProduction      bool
	IsSandbox         bool
	IsStaging         bool
	ProjectId         string
	Protocol          string
	RootDir           string
	SentryDSN         string
	SiteTitle         string
	StaticUrl         string
	DashboardUrl      string

	Email struct {
		From     email.Email
		ReplyTo  email.Email
		Provider integration.Integration
	}

	Ethereum struct {
		TestPassword    string
		DepositPassword string
		MainNetNodes    []string
		TestNetNodes    []string

		WebhookPassword string
	}

	Bitcoin struct {
		TestPassword    string
		DepositPassword string

		MainNetNodes     []string
		MainNetUsernames []string
		MainNetPasswords []string

		TestNetNodes     []string
		TestNetUsernames []string
		TestNetPasswords []string

		WebhookPassword string
	}

	Mercury struct {
		WebhookSecret string
	}

	Secret      string
	SessionName string

	Prefixes map[string]string
	Hosts    map[string]string

	Fee float64

	Salesforce struct {
		ConsumerKey    string
		ConsumerSecret string
		CallbackURL    string
	}

	Paypal struct {
		Email        string
		Api          string
		IpnUrl       string
		PaypalIpnUrl string
	}

	Square struct {
		// Application ID from Square Developer Dashboard
		ApplicationId string

		// Access token for API calls
		AccessToken string

		// Location ID for transactions
		LocationId string

		// Webhook signature key
		WebhookSignatureKey string

		// Environment: sandbox or production
		Environment string

		// Sandbox credentials (for development/testing)
		SandboxApplicationId string
		SandboxAccessToken   string
		SandboxLocationId    string

		// Production credentials
		ProductionApplicationId string
		ProductionAccessToken   string
		ProductionLocationId    string
	}

	Mandrill struct {
		APIKey    string
		FromEmail string
		FromName  string
	}

	Facebook struct {
		AppId        string
		AppSecret    string
		GraphVersion string
	}

	Google struct {
		APIKey string
		Bucket struct {
			ImageUploads string
		}
	}

	// Netlify
	Netlify struct {
		BaseUrl     string
		ClientId    string
		Secret      string
		AccessToken string
	}

	// Cloudflare {
	Cloudflare struct {
		Email string
		Key   string
		Zone  string
	}

	// Sendgrid API key
	SendGrid struct {
		APIKey string
	}

	// SMTP Relay
	SMTPRelay struct {
		Endpoint string
		Username string
		Password string
	}

	// Current working dir
	WorkingDir string
}

// Return url to static file, module or path rooted in a module
func (c Config) UrlFor(moduleName string, args ...string) (url string) {
	// Trim whitespace
	moduleName = strings.TrimSpace(moduleName)

	// If we find `moduleName`, we'll use that as root, otherwise assume we
	// were passed a static file as `moduleName`.
	if host, ok := c.Hosts[moduleName]; ok {
		// Use host + prefix to build url root to path in given module
		url = host + c.Prefixes[moduleName]
		args = append([]string{url}, args...)
	} else {
		staticPath := moduleName
		args = append([]string{c.StaticUrl, staticPath}, args...)
	}

	// Join all parts of the path
	url = path.Join(args...)

	// Strip leading slash and replace with protocol relative leading "//".
	url = c.Protocol + strings.TrimLeft(url, "/")

	// Add back ending "/" if trimmed.
	if len(args) > 0 {
		last := args[len(args)-1]
		if string(last[len(last)-1]) == "/" {
			url = url + "/"
		}
	}

	return url
}

// Return absolute url (including protocol to path)
func (c Config) AbsoluteUrlFor(moduleName string, args ...string) (url string) {
	url = c.UrlFor(moduleName, args...)
	if c.IsDevelopment {
		return "http://localhost:8080" + url
	} else {
		return "https:" + url
	}
}

// Load configuration from JSON file
func (c *Config) Load(fileName string) {
	file, err := os.Open(fileName)
	if err != nil {
		panic(fmt.Sprintf("Failed to open configuration file: %v", err))
	}
	decoder := json.NewDecoder(file)
	if err = decoder.Decode(c); err != nil {
		panic(fmt.Sprintf("Failed to decode configuration file: %v", err))
	}
}

// Get current config object
func Get() *Config {
	if cachedConfig != nil {
		return cachedConfig
	}

	// Default to development environment
	cachedConfig = Development()

	switch Env {
	case "test":
		cachedConfig = Test()
	case "sandbox":
		cachedConfig = Sandbox()
	case "staging":
		cachedConfig = Staging()
	case "production":
		cachedConfig = Production()
	}

	for _, configFile := range configFileLocations {
		if _, err := os.Stat(configFile); err == nil {
			cachedConfig.Load(configFile)
		}
	}

	// Set current working dir
	cachedConfig.WorkingDir = cwd

	return cachedConfig
}

var config = Get()

// Expose global config.
var AutoCompileAssets = config.AutoCompileAssets
var AutoLoadFixtures = config.AutoLoadFixtures
var Bitcoin = config.Bitcoin
var Cloudflare = config.Cloudflare
var CookieDomain = config.CookieDomain
var DatastoreWarn = config.DatastoreWarn
var DemoMode = config.DemoMode
var Email = config.Email
var Ethereum = config.Ethereum
var Facebook = config.Facebook
var Fee = config.Fee
var Google = config.Google
var IsDevelopment = config.IsDevelopment
var IsProduction = config.IsProduction
var IsSandbox = config.IsSandbox
var IsStaging = config.IsStaging
var IsTest = config.IsTest
var Mandrill = config.Mandrill
var Mercury = config.Mercury
var Netlify = config.Netlify
var Paypal = config.Paypal
var Prefixes = config.Prefixes
var RootDir = config.RootDir
var SMTPRelay = config.SMTPRelay
var Salesforce = config.Salesforce
var Secret = config.Secret
var SendGrid = config.SendGrid
var SentryDSN = config.SentryDSN
var SessionName = config.SessionName
var SiteTitle = config.SiteTitle
var StaticUrl = config.StaticUrl
var DashboardUrl = config.DashboardUrl
var Square = config.Square
var WorkingDir = config.WorkingDir

func UrlFor(moduleName string, args ...string) string {
	return config.UrlFor(moduleName, args...)
}

func AbsoluteUrlFor(moduleName string, args ...string) string {
	return config.AbsoluteUrlFor(moduleName, args...)
}
