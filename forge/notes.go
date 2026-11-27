package forge

// notes.go — an issue's conversation and a repository's branches.

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// Note is one comment on an issue.
type Note struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	User    *User  `json:"user,omitempty"`
	Created string `json:"created_at"`
	Updated string `json:"updated_at"`
}

// Comments is an issue's conversation, oldest first, as the actor may read it.
func (c *Client) Comments(ctx context.Context, org, repo string, number int64) ([]Note, error) {
	if err := validSegment(org); err != nil {
		return nil, err
	}
	if err := validSegment(repo); err != nil {
		return nil, fmt.Errorf("forge: repo: %w", err)
	}
	if number <= 0 {
		return nil, fmt.Errorf("forge: comments: bad number %d", number)
	}
	var out []Note
	err := c.do(ctx, "/repos/"+url.PathEscape(org)+"/"+url.PathEscape(repo)+
		"/issues/"+strconv.FormatInt(number, 10)+"/comments", nil, &out)
	return out, err
}

// Branches names every branch of owner/repo.
func (c *Client) Branches(ctx context.Context, owner, repo string) ([]string, error) {
	if err := validSegment(owner); err != nil {
		return nil, err
	}
	if err := validSegment(repo); err != nil {
		return nil, fmt.Errorf("forge: repo: %w", err)
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/branches"
	var names []string
	for p := 1; p <= maxPages; p++ {
		var batch []struct {
			Name string `json:"name"`
		}
		q := url.Values{"limit": {strconv.Itoa(page)}, "page": {strconv.Itoa(p)}}
		if err := c.do(ctx, path, q, &batch); err != nil {
			return nil, err
		}
		for _, b := range batch {
			if b.Name != "" {
				names = append(names, b.Name)
			}
		}
		if len(batch) < page {
			break
		}
	}
	return names, nil
}
