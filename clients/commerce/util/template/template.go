package template

import (
	"os"

	"github.com/aymerick/raymond"
	"github.com/flosch/pongo2"
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/types/country"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/thankyou"
	"github.com/hanzoai/cloud/clients/commerce/util/fs"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

var cwd, _ = os.Getwd()

func createMap(pairs []interface{}) map[string]interface{} {
	// Create map from pairs
	m := make(map[string]interface{})

	for i := 0; i < len(pairs); i = i + 2 {
		m[pairs[i].(string)] = pairs[i+1]
	}

	return m
}

func createContext(pairs []interface{}) pongo2.Context {
	ctx := pongo2.Context{}
	m := createMap(pairs)
	for k, v := range m {
		ctx[k] = v
	}

	return ctx
}

func createTemplateSet() *pongo2.TemplateSet {
	loader := pongo2.MustNewLocalFileSystemLoader("")
	set := pongo2.NewSet("default", loader)

	set.Debug = config.IsDevelopment

	set.Globals["config"] = config.Get()

	set.Globals["isDevelopment"] = config.IsDevelopment
	set.Globals["isProduction"] = config.IsProduction
	set.Globals["isStaging"] = config.IsStaging

	set.Globals["siteTitle"] = config.SiteTitle

	set.Globals["urlFor"] = func(moduleName string, args ...string) string {
		return config.UrlFor(moduleName, args...)
	}

	// DEPRECATED: Remove as soon as all templates are updated to use `urlFor`.
	set.Globals["staticUrl"] = config.StaticUrl
	set.Globals["moduleUrl"] = func(moduleName string, args ...string) string {
		return config.UrlFor(moduleName, args...)
	}

	set.Globals["jsonify"] = json.Encode
	set.Globals["constants"] = struct {
		CountriesByISOCode map[string]country.Country
		Countries          []country.Country
		CurrencyTypes      []currency.Type
		ThankYouTypes      []thankyou.Type
	}{
		CountriesByISOCode: country.ByISO3166_2,
		Countries:          country.Countries,
		CurrencyTypes:      currency.Types,
		ThankYouTypes:      thankyou.Types,
	}
	return set
}

var templateSet = createTemplateSet()

func getTemplate(path string) *pongo2.Template {
	// All templates are expected to be in templates dir
	templatePath := cwd + "/templates/" + path

	// Get template from cache
	template, err := templateSet.FromCache(templatePath)
	if err != nil {
		log.Panic("Unable to fetch template: %v\n\n%v", templatePath, err)
	}

	return template
}

func Render(c *gin.Context, path string, pairs ...interface{}) (err error) {
	// Get template
	template := getTemplate(path)

	// Create pongo context
	ctx := createContext(pairs)

	// Render template
	if err := template.ExecuteWriter(ctx, c.Writer); err != nil {
		log.Panic("Unable to render template: %v\n\n%v", path, err)
	}

	// Set content type
	c.Writer.Header().Set("Content-Type", "text/html; charset=utf-8")

	return
}

func RenderEmail(path string, data map[string]interface{}) string {
	// Get template

	templatePath := cwd + "/templates/email/" + path + ".html"

	template := fs.ReadFile(templatePath)

	// Render template
	out := raymond.MustRender(string(template), data)

	return out
}

func RenderPath(path string, pairs ...interface{}) string {
	// Get template
	template := getTemplate(path)

	// Create pongo context
	ctx := createContext(pairs)

	// Render template
	out, err := template.Execute(ctx)
	if err != nil {
		log.Panic("Unable to render template: %v\n\n%v", path, err)
	}

	return out
}

func RenderString(template string, pairs ...interface{}) string {
	// Create pongo context
	ctx := createContext(pairs)

	// Render template
	str, err := pongo2.RenderTemplateString(template, ctx)
	if err != nil {
		log.Panic("Unable to render template: %v", err)
	}
	return str
}
