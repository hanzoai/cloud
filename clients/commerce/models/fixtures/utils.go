package fixtures

// Inline all styles before use
import (
	// "github.com/vanng822/go-premailer/premailer"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/util/fs"
)

func readEmailTemplate(path string) string {
	template := string(fs.ReadFile(config.WorkingDir + path))
	return template

	// prem := premailer.NewPremailerFromString(template, premailer.NewOptions())
	// html, err := prem.Transform()
	// if err != nil {
	// 	panic(err)
	// }
	// return html
}
