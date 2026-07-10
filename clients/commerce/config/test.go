package config

// Development settings
func Test() *Config {
	config := Defaults()

	config.ProjectId = "crowdstart-test"
	config.IsTest = true

	config.AutoCompileAssets = false
	config.AutoLoadFixtures = false
	config.DatastoreWarn = false

	config.Protocol = "/"

	config.Prefixes["analytics"] = "/"
	config.Prefixes["api"] = "/v1/"
	config.Prefixes["cdn"] = "/"
	config.Prefixes["dash"] = "/"
	config.Prefixes["default"] = "/"

	config.Hosts["analytics"] = ""
	config.Hosts["api"] = ""
	config.Hosts["cdn"] = ""
	config.Hosts["dash"] = ""
	config.Hosts["default"] = ""

	config.StaticUrl = "/static"

	return config
}
