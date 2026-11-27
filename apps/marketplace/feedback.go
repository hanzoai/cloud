package marketplace

// feedback.go — reputation, earned: only the parties of a settled job may say
// anything about each other, once each, and a listing's and a seller's standing
// is nothing but what they said and what their jobs did (store.go reputations).

import (
	"context"
	"database/sql"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/zap-proto/zip"
)

// Feedback is what one party of a settled job said about the other.
type Feedback struct {
	// Job is the job it is about.
	Job string `json:"job"`
	// From is the org that gave it.
	From string `json:"from"`
	// About is the other party of the job, the org it is about.
	About string `json:"about"`
	// Listing is the listing the job was hired through; empty for a direct offer.
	Listing string `json:"listing,omitempty"`
	// Rating is 1 to 5 stars.
	Rating int `json:"rating"`
	// Comment is the party's words, at most 4096 characters.
	Comment string `json:"comment,omitempty"`
	// At is when, unix seconds.
	At int64 `json:"at"`
}

// feedbackIn rates the other party of a job.
type feedbackIn struct {
	// ID is the job, from the path.
	ID string `json:"id"`
	// Rating is 1 to 5 stars. Required.
	Rating int `json:"rating"`
	// Comment is the party's words, at most 4096 characters.
	Comment string `json:"comment"`
}

// Rates the other party of a settled job — the seller when the caller's org
// bought, the buyer when it sold — once per party per job, and never edited. A
// job counts as settled once it was released, or refunded after the seller took
// it on; one cancelled or declined before any work earns nothing. What buyers say
// of a seller is its listing's and its own reputation in the shop. It answers 201
// with the feedback recorded.
//
// Example: {"id": "job_9a1f", "rating": 5, "comment": "Thorough and on time"}
func (o marketOps) feedback(ctx context.Context, in *feedbackIn) (*Feedback, error) {
	j, org, err := o.party(ctx, in.ID, eitherSide)
	if err != nil {
		return nil, err
	}
	if !slices.Contains([]string{StatusReleased, StatusRefunded}, j.Status) {
		return nil, zip.Errorf(http.StatusConflict, "job %s is %s: feedback is for a settled job", j.ID, j.Status)
	}
	if in.Rating < 1 || in.Rating > 5 {
		return nil, zip.ErrBadRequest("rating is 1 to 5 stars")
	}
	comment := strings.TrimSpace(in.Comment)
	if len(comment) > maxText {
		return nil, zip.ErrBadRequest("comment too long")
	}
	if err := dialect.Text(comment); err != nil {
		return nil, zip.ErrBadRequest("comment: " + err.Error())
	}
	f := Feedback{Job: j.ID, From: org, About: j.SellerOrg, Listing: j.Listing, Rating: in.Rating, Comment: comment,
		At: time.Now().Unix()}
	if org == j.SellerOrg {
		f.About = j.BuyerOrg
	}
	var fresh bool
	st := o.s.State.store
	said, err := st.seal("feedback", "comment", f.Comment, f.Job, f.From)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "feedback: %v", err)
	}
	err = st.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO feedback (job, from_org, about, listing, rating, comment, at)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, f.Job, f.From, f.About, f.Listing, f.Rating, said, f.At)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		fresh = true
		return o.trail(ctx, org, org, "marketplace.feedback.given", audit.Resource{Type: "marketplace.job", ID: j.ID},
			map[string]any{"about": f.About, "rating": f.Rating})
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "feedback: %v", err)
	}
	if !fresh {
		return nil, zip.Errorf(http.StatusConflict, "your org already rated job %s", j.ID)
	}
	if org == j.BuyerOrg {
		o.stated(ctx, eventReview, j.Job, f.Rating)
	}
	return &f, nil
}
