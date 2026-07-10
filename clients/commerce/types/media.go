package types

import (
	"fmt"
	"net/http"

	"github.com/mholt/binding"
)

type MediaType string

const (
	MediaVideo      MediaType = "video"
	MediaImage                = "image"
	MediaLiveStream           = "livestream"
	MediaWebGL                = "webgl"
	MediaAudio                = "audio"
	MediaEmbed                = "embed"
)

type Media struct {
	Type MediaType `json:"type"`
	Alt  string    `json:"alt"`
	Url  string    `json:"url"`
	X    int       `json:"x"`
	Y    int       `json:"y"`
}

func (i Media) Dimensions() string {
	return fmt.Sprintf("%dx%d", i.X, i.Y)
}

func (i Media) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	if i.Url == "" {
		errs = append(errs, binding.Error{
			FieldNames:     []string{"Url"},
			Classification: "InputError",
			Message:        "Image does not have a URL",
		})
	}
	return errs
}
