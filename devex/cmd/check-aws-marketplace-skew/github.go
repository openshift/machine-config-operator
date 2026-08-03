package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// githubRawURLTemplate points at raw.githubusercontent.com, which serves a file's content at any
// ref (branch, tag, or commit SHA) without needing a local checkout of the repo.
const githubRawURLTemplate = "https://raw.githubusercontent.com/%s/%s/%s/%s"

// githubAPIBaseURL is a var (not a const) so tests can point it at an httptest.Server instead of
// the real GitHub API.
var githubAPIBaseURL = "https://api.github.com"

// githubCommitsAPITemplate lists commits touching a path, most recent first.
const githubCommitsAPITemplate = "%s/repos/%s/%s/commits?sha=%s&path=%s&per_page=%d&page=%d"

const (
	// githubCommitsPerPage is GitHub's own hard cap — the API rejects/clamps anything higher.
	githubCommitsPerPage = 100
	// githubCommitsMaxPages bounds the total history considered to githubCommitsPerPage *
	// githubCommitsMaxPages = 500 commits. Fetching stops as soon as a page comes back short
	// (the true end of history for that path), so real MCO history (63 commits touching
	// pkg/controller/common/constants.go today) still costs a single request — this cap only
	// matters for branches/files with much deeper history than that.
	githubCommitsMaxPages = 5
)

// RefNotFoundError indicates a GitHub API request returned HTTP 404. Path is set only when the
// request was for a specific file (raw.githubusercontent.com), where a 404 can mean either the
// ref or the path doesn't exist and the response body gives no way to tell which; for the commits
// API, a 404 unambiguously means the ref doesn't exist, so Path is left empty there.
type RefNotFoundError struct {
	Owner, Repo, Ref, Path, URL string
}

func (e *RefNotFoundError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s/%s: ref %q or path %q not found (%s returned 404)", e.Owner, e.Repo, e.Ref, e.Path, e.URL)
	}
	return fmt.Sprintf("%s/%s has no branch %q (%s returned 404)", e.Owner, e.Repo, e.Ref, e.URL)
}

// fetchRawGitHubFile fetches path's content at ref from owner/repo over HTTPS, with no local
// checkout of that repo required.
func fetchRawGitHubFile(ctx context.Context, owner, repo, ref, path string) ([]byte, error) {
	url := fmt.Sprintf(githubRawURLTemplate, owner, repo, ref, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &RefNotFoundError{Owner: owner, Repo: repo, Ref: ref, Path: path, URL: url}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetching %s returned HTTP %d: %s", url, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body from %s: %w", url, err)
	}
	return body, nil
}

// ghCommit is the subset of a GitHub API commit resource this tool needs.
type ghCommit struct {
	SHA  string
	Date time.Time
}

// githubCommitsForPath lists commits touching path on branch in owner/repo, most recent first, via
// the GitHub REST API rather than a local git checkout. Paginates up to githubCommitsMaxPages,
// stopping as soon as a page comes back short of githubCommitsPerPage (the true end of history).
func githubCommitsForPath(ctx context.Context, owner, repo, branch, path string) ([]ghCommit, error) {
	var all []ghCommit
	for page := 1; page <= githubCommitsMaxPages; page++ {
		commits, err := fetchGitHubCommitsPage(ctx, owner, repo, branch, path, page)
		if err != nil {
			return nil, err
		}
		all = append(all, commits...)
		if len(commits) < githubCommitsPerPage {
			break
		}
	}
	return all, nil
}

// fetchGitHubCommitsPage fetches a single page of githubCommitsForPath's results.
func fetchGitHubCommitsPage(ctx context.Context, owner, repo, branch, path string, page int) ([]ghCommit, error) {
	url := fmt.Sprintf(githubCommitsAPITemplate, githubAPIBaseURL, owner, repo, branch, path, githubCommitsPerPage, page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// api.github.com (unlike raw.githubusercontent.com) enforces a 60/hour unauthenticated rate
	// limit per IP, shared across everything on that IP — cheap to blow through on a shared NAT.
	// GITHUB_TOKEN is the standard env var convention (gh CLI, GitHub Actions, etc.); honoring it
	// here raises the limit to 5000/hour with zero new flags for the common case of already having
	// one set.
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body from %s: %w", url, err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, &RefNotFoundError{Owner: owner, Repo: repo, Ref: branch, URL: url}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s returned HTTP %d: %s", url, resp.StatusCode, string(body))
	}

	commits, err := parseGitHubCommitsJSON(body)
	if err != nil {
		return nil, fmt.Errorf("branch %s: %w", branch, err)
	}
	return commits, nil
}

// parseGitHubCommitsJSON extracts SHA/committer-date pairs from a GitHub "list commits" API
// response, split out from githubCommitsForPath so it's testable against a fixture without a live
// network call.
func parseGitHubCommitsJSON(body []byte) ([]ghCommit, error) {
	var raw []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse commits list: %w", err)
	}

	commits := make([]ghCommit, 0, len(raw))
	for _, c := range raw {
		commits = append(commits, ghCommit{SHA: c.SHA, Date: c.Commit.Committer.Date})
	}
	return commits, nil
}
