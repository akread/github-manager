package github

import (
	"fmt"
	"net/url"
)

type rawIssue struct {
	Number  int     `json:"number"`
	Title   string  `json:"title"`
	HTMLURL string  `json:"html_url"`
	User    rawUser `json:"user"`
	Draft   bool    `json:"draft"`
}

type rawSearch struct {
	Items []rawIssue `json:"items"`
}

// ReviewRequest is an open pull request where the authenticated user is a
// requested reviewer.
type ReviewRequest struct {
	Ref    PullRef
	Title  string
	Author string
	Draft  bool
}

// LoadReviewRequests lists the open pull requests in a repository that
// request a review from the authenticated user.
func (c *Client) LoadReviewRequests(ref RepoRef) ([]ReviewRequest, error) {
	username, err := c.Username(ref.Domain)
	if err != nil {
		return nil, err
	}
	q := url.QueryEscape(fmt.Sprintf("is:pr is:open review-requested:%s repo:%s", username, ref.Repo))
	var result rawSearch
	if err := c.api(ref.Domain, "search/issues?per_page=100&q="+q, &result); err != nil {
		return nil, err
	}
	out := make([]ReviewRequest, 0, len(result.Items))
	for _, it := range result.Items {
		out = append(out, ReviewRequest{
			Ref: PullRef{
				URL:    fmt.Sprintf("https://%s/%s/pull/%d", ref.Domain, ref.Repo, it.Number),
				Domain: ref.Domain,
				Repo:   ref.Repo,
				Number: it.Number,
			},
			Title:  it.Title,
			Author: it.User.Login,
			Draft:  it.Draft,
		})
	}
	return out, nil
}
