package notion

import (
	"encoding/json"
	"testing"
)

// A realistic custom_api_call output wrapping a Notion /v1/search response with one
// page and one database, as the activepieces piece would return it.
const searchOutput = `{
  "status": 200,
  "headers": {"content-type": "application/json"},
  "body": {
    "object": "list",
    "results": [
      {
        "object": "page",
        "id": "page-abc",
        "url": "https://notion.so/page-abc",
        "last_edited_time": "2026-06-01T12:30:00.000Z",
        "properties": {
          "Name": {"type": "title", "title": [{"plain_text": "Q3 Roadmap"}]},
          "Notes": {"type": "rich_text", "rich_text": [{"plain_text": "ship the connector"}]},
          "Status": {"type": "select", "select": {"name": "In Progress"}}
        }
      },
      {
        "object": "database",
        "id": "db-xyz",
        "url": "https://notion.so/db-xyz",
        "last_edited_time": "2026-05-15T09:00:00.000Z",
        "title": [{"plain_text": "Engineering Wiki"}],
        "properties": {}
      }
    ]
  }
}`

func decodeOutput(t *testing.T) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(searchOutput), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func TestResults_ExtractsItems(t *testing.T) {
	recs := Results(decodeOutput(t))
	if len(recs) != 2 {
		t.Fatalf("Results len = %d, want 2", len(recs))
	}
	if recs[0]["id"] != "page-abc" || recs[1]["id"] != "db-xyz" {
		t.Errorf("unexpected records: %v, %v", recs[0]["id"], recs[1]["id"])
	}
}

func TestResults_EmptyOnMalformed(t *testing.T) {
	if got := Results(nil); len(got) != 0 {
		t.Errorf("Results(nil) = %d items, want 0", len(got))
	}
	if got := Results(map[string]any{"body": "not-an-object"}); len(got) != 0 {
		t.Errorf("Results(bad body) = %d items, want 0", len(got))
	}
}

func TestNormalize_Page(t *testing.T) {
	recs := Results(decodeOutput(t))
	d := Normalize(recs[0])
	if d.Title != "Q3 Roadmap" {
		t.Errorf("Title = %q, want Q3 Roadmap", d.Title)
	}
	if d.ExternalID != "notion:page:page-abc" {
		t.Errorf("ExternalID = %q", d.ExternalID)
	}
	if d.URL != "https://notion.so/page-abc" {
		t.Errorf("URL = %q", d.URL)
	}
	if d.Timestamp != "2026-06-01 12:30:00" {
		t.Errorf("Timestamp = %q, want 2026-06-01 12:30:00", d.Timestamp)
	}
	// Body must carry the rich-text + select property prose so the embedding has content.
	if !contains(d.Body, "ship the connector") || !contains(d.Body, "In Progress") {
		t.Errorf("Body missing property text: %q", d.Body)
	}
}

func TestNormalize_Database(t *testing.T) {
	recs := Results(decodeOutput(t))
	d := Normalize(recs[1])
	if d.Title != "Engineering Wiki" {
		t.Errorf("database Title = %q, want Engineering Wiki", d.Title)
	}
	if d.ExternalID != "notion:database:db-xyz" {
		t.Errorf("ExternalID = %q", d.ExternalID)
	}
}

// TestNormalize_IdempotentExternalID proves the external_id is stable for the same
// record (so a re-sync UPDATES in place rather than duplicating the vector point).
func TestNormalize_IdempotentExternalID(t *testing.T) {
	recs := Results(decodeOutput(t))
	if Normalize(recs[0]).ExternalID != Normalize(recs[0]).ExternalID {
		t.Error("external_id not stable across calls")
	}
}

func TestTime_Empty(t *testing.T) {
	if Time("") != "" {
		t.Error("Time('') should be empty")
	}
	if Time("not-a-date") != "" {
		t.Error("Time(bad) should be empty, not a panic")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
