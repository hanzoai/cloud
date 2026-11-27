package patent

// write.go is the shared append path: validate, digest what the caller sent,
// stamp what the server knows, append. Every rule that decides whether a record
// is evidence lives here rather than in a handler, so no route can be added later
// that forgets one of them.
//
// The split matters. A HANDLER reads the principal and the project off the
// request and calls one of these. THESE turn wire values into versions. Nothing
// else writes, and nothing in here takes a timestamp, an author or a sequence
// number from a caller — those three arguments appear in no signature below.

import (
	"context"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"
)

// ideaVersions appends conception entries and returns how many NEW versions
// landed. project and author are the SERVER's values, passed in by the handler
// that resolved them from the request.
func (s *store) ideaVersions(ctx context.Context, project, author string, in []PatentIdea) (int, error) {
	recs := make([]record, 0, len(in))
	for i := range in {
		a := in[i]
		a.Project = project
		if err := validIdea(&a); err != nil {
			return 0, err
		}
		sum := contentIdea{Kind: a.Kind, Title: a.Title, Statement: a.Statement, Problem: a.Problem,
			Alternatives: a.Alternatives, PriorArt: a.PriorArt, PatentSignature: a.PatentSignature, Notes: a.Notes}
		// The declared revision is part of what is hashed: a retraction that
		// repeats the entry's own content verbatim must still be a NEW version,
		// or withdrawing an idea would be a no-op that reports success.
		h, err := digestOf(struct {
			Content  any    `json:"content"`
			Revision string `json:"revision"`
		}{sum, revision(a.Revision)})
		if err != nil {
			return 0, zip.Errorf(http.StatusInternalServerError, "patent digest failed")
		}
		sig := a.PatentSignature
		if sig != nil {
			// The stored signature says whether THIS deployment checked it. A
			// client cannot set that half, and an unchecked one is never reported
			// as verified.
			copied := *sig
			copied.Verified = false
			sig = &copied
		}
		recs = append(recs, &ideaRecord{
			stamp:           stamp{RecordedAt: stampAt(), AuthoredBy: author},
			Project:         a.Project,
			ID:              a.ID,
			ContentHash:     h,
			Revision:        revision(a.Revision),
			Kind:            a.Kind,
			Title:           a.Title,
			Statement:       a.Statement,
			Problem:         a.Problem,
			Alternatives:    a.Alternatives,
			PriorArt:        a.PriorArt,
			PatentSignature: sig,
			Visibility:      "private",
			Notes:           a.Notes,
		})
	}
	return put(ctx, s, kindIdea, recs)
}

// implVersions appends implementation records. The citation is validated before
// anything is written, because a record that would be filed must not be filed
// when its whole purpose is to be refused.
func (s *store) implVersions(ctx context.Context, project, author string, in []PatentImplementation) (int, error) {
	recs := make([]record, 0, len(in))
	for i := range in {
		a := in[i]
		a.Project = project
		if err := validImpl(&a); err != nil {
			return 0, err
		}
		sum := contentImpl{IdeaIDs: a.IdeaIDs, Repo: a.Repo, GitSHA: a.GitSHA, GitBranch: a.GitBranch,
			GitDirty: a.GitDirty, Model: a.Model, PromptSHA256: a.PromptSHA256, Generated: a.Generated,
			HumanReview: a.HumanReview, ArtifactSHA256: a.ArtifactSHA256, Notes: a.Notes}
		// The declared revision is part of what is hashed: a retraction that
		// repeats the entry's own content verbatim must still be a NEW version,
		// or withdrawing an idea would be a no-op that reports success.
		h, err := digestOf(struct {
			Content  any    `json:"content"`
			Revision string `json:"revision"`
		}{sum, revision(a.Revision)})
		if err != nil {
			return 0, zip.Errorf(http.StatusInternalServerError, "patent digest failed")
		}
		recs = append(recs, &implRecord{
			stamp:          stamp{RecordedAt: stampAt(), AuthoredBy: author},
			Project:        a.Project,
			ID:             a.ID,
			ContentHash:    h,
			Revision:       revision(a.Revision),
			IdeaIDs:        a.IdeaIDs,
			Repo:           a.Repo,
			GitSHA:         a.GitSHA,
			GitBranch:      a.GitBranch,
			GitDirty:       a.GitDirty,
			Model:          a.Model,
			PromptSHA256:   a.PromptSHA256,
			Generated:      a.Generated,
			HumanReview:    review(a.HumanReview),
			ArtifactSHA256: a.ArtifactSHA256,
			Visibility:     "private",
			Notes:          a.Notes,
		})
	}
	return put(ctx, s, kindImpl, recs)
}

// discVersions appends disclosures. The body is digested separately from the
// record so a reader can tell "this document, these bytes" from "this document,
// these citations" — an attorney is handed the first and an auditor the second.
func (s *store) discVersions(ctx context.Context, project, author string, in []PatentDisclosure) (int, error) {
	recs := make([]record, 0, len(in))
	for i := range in {
		a := in[i]
		a.Project = project
		if err := validDisclosure(&a); err != nil {
			return 0, err
		}
		sum := contentDisc{Title: a.Title, Summary: a.Summary, Claims: a.Claims,
			IdeaIDs: a.IdeaIDs, ImplementationIDs: a.ImplementationIDs, Body: a.Body,
			Status: a.Status, FilingRef: a.FilingRef}
		// The declared revision is part of what is hashed: a retraction that
		// repeats the entry's own content verbatim must still be a NEW version,
		// or withdrawing an idea would be a no-op that reports success.
		h, err := digestOf(struct {
			Content  any    `json:"content"`
			Revision string `json:"revision"`
		}{sum, revision(a.Revision)})
		if err != nil {
			return 0, zip.Errorf(http.StatusInternalServerError, "patent digest failed")
		}
		recs = append(recs, &discRecord{
			stamp:             stamp{RecordedAt: stampAt(), AuthoredBy: author},
			Project:           a.Project,
			ID:                a.ID,
			ContentHash:       h,
			Revision:          revision(a.Revision),
			Title:             a.Title,
			Summary:           a.Summary,
			Claims:            a.Claims,
			IdeaIDs:           a.IdeaIDs,
			ImplementationIDs: a.ImplementationIDs,
			Body:              a.Body,
			BodySHA256:        hashJoin(a.Body),
			Status:            status(a.Status),
			FilingRef:         a.FilingRef,
			Visibility:        "private",
		})
	}
	return put(ctx, s, kindDisc, recs)
}

// approvalVersions appends role decisions. The author is the caller's validated
// subject and the id is derived from it, so one person's re-decision supersedes
// their own prior word and nobody else's.
func (s *store) approvalVersions(ctx context.Context, project, author string, in []PatentApproval) (int, error) {
	recs := make([]record, 0, len(in))
	for i := range in {
		a := in[i]
		a.Project, a.AuthoredBy = project, author
		if err := validApproval(&a); err != nil {
			return 0, err
		}
		sum := contentApproval{Role: strings.TrimSpace(a.Role), Decision: a.Decision, Comment: a.Comment}
		// The declared revision is part of what is hashed: a retraction that
		// repeats the entry's own content verbatim must still be a NEW version,
		// or withdrawing an idea would be a no-op that reports success.
		h, err := digestOf(struct {
			Content  any    `json:"content"`
			Revision string `json:"revision"`
		}{sum, revision(a.Revision)})
		if err != nil {
			return 0, zip.Errorf(http.StatusInternalServerError, "patent digest failed")
		}
		recs = append(recs, &approvalRecord{
			stamp:        stamp{RecordedAt: stampAt(), AuthoredBy: author},
			Project:      a.Project,
			DisclosureID: a.DisclosureID,
			ID:           "appr_" + h[:16],
			ContentHash:  h,
			Revision:     revision(a.Revision),
			Role:         strings.TrimSpace(a.Role),
			Decision:     a.Decision,
			Comment:      a.Comment,
		})
	}
	return put(ctx, s, kindApproval, recs)
}
