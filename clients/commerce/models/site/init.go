package site

import (
	"encoding/gob"
)

func init() {
	gob.Register(Site{})
}
