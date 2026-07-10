package middleware

import (
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
)

func Static(urlRoot string) gin.HandlerFunc {
	// Shave off leading /, otherwise filepath.Join will fail.
	directory := strings.TrimLeft(urlRoot, "/")
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(config.RootDir, directory)
	}
	dir := http.Dir(directory)

	return func(c *gin.Context) {
		if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
			return
		}

		url := c.Request.URL
		file := strings.Replace(url.Path, urlRoot, "", 1)

		f, err := dir.Open(file)
		if err != nil {
			c.AbortWithStatus(404)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			c.AbortWithStatus(500)
			return
		}

		if fi.IsDir() {
			file = path.Join(file, "index.html")
			f, err = dir.Open(file)
			if err != nil {
				c.AbortWithStatus(500)
				return
			}
			defer f.Close()
			fi, err = f.Stat()
			if err != nil || fi.IsDir() {
				c.AbortWithStatus(500)
				return
			}
		}

		// res.Header().Set("Expires", expires)
		http.ServeContent(c.Writer, c.Request, file, fi.ModTime(), f)

		c.Next()
	}
}
