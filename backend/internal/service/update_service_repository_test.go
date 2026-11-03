//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpdateServiceCustomRepositoryUsedForUpdateAndRollback(t *testing.T) {
	t.Setenv("UPDATE_GITHUB_REPO", "  example/my-fork  ")
	client := &updateServiceGitHubClientStub{
		release:        &GitHubRelease{TagName: "v2.1.0"},
		recentReleases: []*GitHubRelease{{TagName: "v1.9.0"}},
	}
	svc := NewUpdateService(&updateServiceCacheStub{}, client, "2.0.0", "release")
	info, err := svc.CheckUpdate(context.Background(), true)
	require.NoError(t, err)
	require.True(t, info.HasUpdate)
	require.Equal(t, "example/my-fork", client.latestRepo)
	versions, err := svc.ListRollbackVersions(context.Background())
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, "example/my-fork", client.recentRepo)
}

func TestUpdateServiceInvalidRepositoryNeverFallsBack(t *testing.T) {
	for _, repo := range []string{"https://github.com/example/repo", "example", "example/repo/extra", "example/repo?x=y", "example/..", "example/.", "example/re po"} {
		t.Run(repo, func(t *testing.T) {
			t.Setenv("UPDATE_GITHUB_REPO", repo)
			client := &updateServiceGitHubClientStub{}
			svc := NewUpdateService(&updateServiceCacheStub{}, client, "2.0.0", "release")
			info, err := svc.CheckUpdate(context.Background(), false)
			require.NoError(t, err)
			require.False(t, info.HasUpdate)
			require.Contains(t, info.Warning, "UPDATE_GITHUB_REPO")
			_, err = svc.ListRollbackVersions(context.Background())
			require.ErrorContains(t, err, "UPDATE_GITHUB_REPO")
			require.Empty(t, client.latestRepo)
			require.Empty(t, client.recentRepo)
		})
	}
}

func TestUpdateServiceRepositoryChangeRejectsCachedRelease(t *testing.T) {
	for _, cachedRepo := range []string{"", "ranxi2001/sub2api"} {
		t.Run("cached-"+cachedRepo, func(t *testing.T) {
			t.Setenv("UPDATE_GITHUB_REPO", "wells974/sub2api")
			data, err := json.Marshal(map[string]any{
				"repository": cachedRepo, "latest": "99.0.0", "timestamp": time.Now().Unix(),
				"release_info": ReleaseInfo{Name: "Wrong fork"},
			})
			require.NoError(t, err)
			cache := &updateServiceCacheStub{data: string(data)}
			client := &updateInstallClient{latestErr: errors.New("GitHub offline")}
			svc := NewUpdateService(cache, client, "2.0.0", "release")
			info, err := svc.CheckUpdate(context.Background(), false)
			require.NoError(t, err)
			require.False(t, info.HasUpdate)
			require.False(t, info.Cached)
			require.Nil(t, info.ReleaseInfo)
			require.Contains(t, info.Warning, "GitHub offline")
			client.latestErr = nil
			client.release = &GitHubRelease{TagName: "v2.1.0", Name: "My fork"}
			info, err = svc.CheckUpdate(context.Background(), false)
			require.NoError(t, err)
			require.Equal(t, "2.1.0", info.LatestVersion)
			require.Equal(t, "wells974/sub2api", client.latestRepo)
		})
	}
}

func TestUpdateServiceSameRepositoryKeepsOfflineCache(t *testing.T) {
	t.Setenv("UPDATE_GITHUB_REPO", "wells974/sub2api")
	client := &updateInstallClient{}
	client.release = &GitHubRelease{TagName: "v2.1.0"}
	cache := &updateServiceCacheStub{}
	svc := NewUpdateService(cache, client, "2.0.0", "release")
	_, err := svc.CheckUpdate(context.Background(), true)
	require.NoError(t, err)
	client.latestErr = errors.New("GitHub offline")
	info, err := svc.CheckUpdate(context.Background(), true)
	require.NoError(t, err)
	require.True(t, info.Cached)
	require.True(t, info.HasUpdate)
	require.Equal(t, "2.1.0", info.LatestVersion)
	require.Contains(t, info.Warning, "GitHub offline")
}
