package subscriber

import (
	"encoding/gob"
)

func init() {
	gob.Register(Subscriber{})
}
