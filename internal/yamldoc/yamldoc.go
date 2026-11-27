package yamldoc

import (
	"regexp"

	"sigs.k8s.io/yaml"
)

// Encode renders a JSON document as YAML, quoting every plain hex, octal or
// binary literal. The encoder quotes a string only when its own resolver would
// read it back as something else, and that resolver gives up on a literal too
// wide for int64 and calls it a string — so an address went out bare, and a
// YAML 1.1 reader (the SDK generators among them) read a 160-bit integer where
// the document said a string. JSON has no such literals, so every match was a
// string in the document.
func Encode(j []byte) ([]byte, error) {
	y, err := yaml.JSONToYAML(j)
	if err != nil {
		return nil, err
	}
	return wide.ReplaceAll(y, []byte(`$1"$2"`)), nil
}

var wide = regexp.MustCompile(`(?m)^(\s*(?:(?:- )+(?:[^\s#'"-][^:\n]*: )?|[^\s#'"-][^:\n]*: ))([-+]?0(?:[xX][0-9a-fA-F_]+|[oO]?[0-7_]+|[bB][01_]+))$`)
