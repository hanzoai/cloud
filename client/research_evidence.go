package client

import "encoding/json"

// ResearchEvidenceRecord persists experiment outcomes across plugin processes.
const ResearchEvidenceRecord = "research_evidence_record"

// ResearchEvidenceIn is evidence from a sibling capability. The authenticated
// plane owns the org; Project is only a sub-dimension inside that org.
type ResearchEvidenceIn struct {
	// Project is the experiment's project within the caller's org.
	Project string `json:"project"`
	// Experiments is the array of research Experiment records, without endpoints.
	Experiments json.RawMessage `json:"experiments"`
}

// ResearchEvidenceRecorded confirms a fenced durable write.
type ResearchEvidenceRecorded struct {
	// Recorded is the number of newly appended evidence versions.
	Recorded int `json:"recorded"`
}
