package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleCommitsJSON mirrors the shape of a GitHub "list commits" API response, trimmed to the
// fields parseGitHubCommitsJSON reads.
const sampleCommitsJSON = `[
  {
    "sha": "abc123",
    "commit": {
      "committer": {
        "date": "2026-03-01T12:00:00Z"
      }
    }
  },
  {
    "sha": "def456",
    "commit": {
      "committer": {
        "date": "2025-12-15T08:30:00Z"
      }
    }
  }
]`

func TestParseGitHubCommitsJSON(t *testing.T) {
	commits, err := parseGitHubCommitsJSON([]byte(sampleCommitsJSON))
	require.NoError(t, err)
	require.Len(t, commits, 2)

	assert.Equal(t, "abc123", commits[0].SHA)
	assert.True(t, commits[0].Date.Equal(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)))

	assert.Equal(t, "def456", commits[1].SHA)
	assert.True(t, commits[1].Date.Equal(time.Date(2025, 12, 15, 8, 30, 0, 0, time.UTC)))
}

func TestParseGitHubCommitsJSON_empty(t *testing.T) {
	commits, err := parseGitHubCommitsJSON([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, commits)
}

func TestParseGitHubCommitsJSON_malformed(t *testing.T) {
	_, err := parseGitHubCommitsJSON([]byte("not json"))
	require.Error(t, err)
}

// withFakeGitHubAPI points githubAPIBaseURL at a local httptest.Server for the duration of the
// test — fully hermetic (localhost-only), unlike a live call to the real GitHub API.
func withFakeGitHubAPI(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	original := githubAPIBaseURL
	githubAPIBaseURL = server.URL
	t.Cleanup(func() { githubAPIBaseURL = original })
}

// fakeCommitsJSON renders shas as a GitHub "list commits" API response body.
func fakeCommitsJSON(t *testing.T, shas []string) []byte {
	t.Helper()
	type commit struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	commits := make([]commit, len(shas))
	for i, sha := range shas {
		commits[i].SHA = sha
		commits[i].Commit.Committer.Date = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Duration(i) * 24 * time.Hour)
	}
	body, err := json.Marshal(commits)
	require.NoError(t, err)
	return body
}

func TestGithubCommitsForPath_paginatesUntilShortPage(t *testing.T) {
	const total = 250 // spans 3 pages: 100, 100, 50 — proves it stops as soon as a page is short.
	requestCount := 0
	withFakeGitHubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		require.Equal(t, githubCommitsPerPage, perPage)

		start := (page - 1) * perPage
		end := start + perPage
		if start > total {
			start = total
		}
		if end > total {
			end = total
		}
		shas := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			shas = append(shas, fmt.Sprintf("sha-%d", i))
		}
		_, err := w.Write(fakeCommitsJSON(t, shas))
		assert.NoError(t, err)
	})

	commits, err := githubCommitsForPath(context.Background(), "owner", "repo", "main", "path")
	require.NoError(t, err)
	assert.Len(t, commits, total)
	assert.Equal(t, 3, requestCount)
	assert.Equal(t, "sha-0", commits[0].SHA)
	assert.Equal(t, fmt.Sprintf("sha-%d", total-1), commits[total-1].SHA)
}

func TestGithubCommitsForPath_capsAtMaxPages(t *testing.T) {
	requestCount := 0
	withFakeGitHubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		// The server has "infinite" history — always returns a full page — proving the client
		// caps itself at githubCommitsMaxPages rather than looping forever.
		shas := make([]string, perPage)
		for i := range shas {
			shas[i] = fmt.Sprintf("page%d-%d", page, i)
		}
		_, err := w.Write(fakeCommitsJSON(t, shas))
		assert.NoError(t, err)
	})

	commits, err := githubCommitsForPath(context.Background(), "owner", "repo", "main", "path")
	require.NoError(t, err)
	assert.Len(t, commits, githubCommitsPerPage*githubCommitsMaxPages)
	assert.Equal(t, githubCommitsMaxPages, requestCount)
}

func TestGithubCommitsForPath_branchNotFound(t *testing.T) {
	withFakeGitHubAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, err := w.Write([]byte(`{"message": "Not Found"}`))
		assert.NoError(t, err)
	})

	_, err := githubCommitsForPath(context.Background(), "owner", "repo", "nonexistent-branch", "path")
	require.Error(t, err)
	var notFound *RefNotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, "nonexistent-branch", notFound.Ref)
}
