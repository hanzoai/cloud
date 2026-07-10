package http

type Error struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
	Param   string `json:"param,omitempty"`
}

func (e Error) Error() string {
	return e.Message
}
