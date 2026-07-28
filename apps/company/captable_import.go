package company

import (
	"fmt"
	"strings"
)

// captable_import.go parses a cap-table spreadsheet (as rows of string cells read
// from Google Sheets) into stakeholders for the import path. The first row is a
// header; columns are matched by name (case-insensitive), so a founder's export
// from any tool works as long as it has at least name + email columns.
//
// Recognized headers: name (required), email (required), type / stakeholderType,
// relationship / currentRelationship, institution / institutionName.

func parseCapTableRows(rows [][]string) ([]Stakeholder, error) {
	if len(rows) < 2 {
		return nil, fmt.Errorf("cap-table sheet needs a header row and at least one data row")
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[normalizeHeader(h)] = i
	}
	nameCol, hasName := firstCol(col, "name", "fullname", "stakeholder")
	emailCol, hasEmail := firstCol(col, "email", "e-mail")
	if !hasName || !hasEmail {
		return nil, fmt.Errorf("cap-table sheet must have 'name' and 'email' columns")
	}
	typeCol, _ := firstCol(col, "type", "stakeholdertype")
	relCol, _ := firstCol(col, "relationship", "currentrelationship", "role")
	instCol, _ := firstCol(col, "institution", "institutionname", "entity")

	out := make([]Stakeholder, 0, len(rows)-1)
	for _, r := range rows[1:] {
		name := cell(r, nameCol)
		email := cell(r, emailCol)
		if name == "" || email == "" {
			continue // skip blank/partial rows
		}
		h := Stakeholder{
			Name: name, Email: email,
			StakeholderType:     normRelationshipDefault(cell(r, typeCol), "INDIVIDUAL"),
			CurrentRelationship: normRelationshipDefault(cell(r, relCol), "INVESTOR"),
			InstitutionName:     cell(r, instCol),
		}
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no stakeholder rows found in the sheet")
	}
	return out, nil
}

func normalizeHeader(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	return strings.NewReplacer(" ", "", "_", "", "-", "").Replace(h)
}

func firstCol(col map[string]int, names ...string) (int, bool) {
	for _, n := range names {
		if i, ok := col[n]; ok {
			return i, true
		}
	}
	return -1, false
}

func cell(r []string, i int) string {
	if i < 0 || i >= len(r) {
		return ""
	}
	return strings.TrimSpace(r[i])
}

// normRelationshipDefault upper-cases a value (the captable enums are upper-case) or
// returns the default when empty.
func normRelationshipDefault(v, def string) string {
	v = strings.ToUpper(strings.TrimSpace(strings.NewReplacer(" ", "_", "-", "_").Replace(v)))
	if v == "" {
		return def
	}
	return v
}
