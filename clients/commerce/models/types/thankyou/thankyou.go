package thankyou

type Type string

const (
	Html     Type = "html"
	Redirect      = "redirect"
	Disabled      = "disabled"
)

var Types = []Type{Html, Redirect, Disabled}
