package releasesource

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRepository(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"", "wells974/sub2api"},
		{"  example/my-fork  ", "example/my-fork"},
		{"ranxi2001/sub2api", "ranxi2001/sub2api"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("UPDATE_GITHUB_REPO", tc.env)
			got, err := Repository()
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	for _, env := range []string{"https://github.com/example/repo", "example", "example/repo/extra", "example/..", "example/.", "example/repo?x=y", "example/re po"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("UPDATE_GITHUB_REPO", env)
			_, err := Repository()
			require.Error(t, err)
		})
	}
}
