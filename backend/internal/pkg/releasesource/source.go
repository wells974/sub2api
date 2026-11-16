// Package releasesource keeps application and login-runtime downloads on the
// same owner-controlled release stream.
package releasesource

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

const DefaultRepository = "wells974/sub2api"

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+$`)

func Repository() (string, error) {
	repo := strings.TrimSpace(os.Getenv("UPDATE_GITHUB_REPO"))
	if repo == "" {
		repo = DefaultRepository
	}
	if !repositoryPattern.MatchString(repo) || strings.HasSuffix(repo, "/.") || strings.HasSuffix(repo, "/..") {
		return repo, fmt.Errorf("UPDATE_GITHUB_REPO must be a GitHub owner/repository, without a URL or additional path")
	}
	return repo, nil
}
