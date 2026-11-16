package reauthruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func archive(t *testing.T, entries []*tar.Header, content []string) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	for i, h := range entries {
		require.NoError(t, tw.WriteHeader(h))
		if h.Typeflag == tar.TypeReg {
			_, err := tw.Write([]byte(content[i]))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return data.Bytes()
}

func TestExtractRejectsEscapesAndLinks(t *testing.T) {
	for _, h := range []*tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg, Size: 1},
		{Name: "/escape", Typeflag: tar.TypeReg, Size: 1},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../escape"},
		{Name: "device", Typeflag: tar.TypeChar},
	} {
		t.Run(h.Name, func(t *testing.T) {
			require.Error(t, extract(archive(t, []*tar.Header{h}, []string{"x"}), t.TempDir()))
		})
	}
}

func TestExtractPreservesExecutableAndRejectsDuplicate(t *testing.T) {
	root := t.TempDir()
	h := &tar.Header{Name: "bin/run", Typeflag: tar.TypeReg, Size: 4, Mode: 0755}
	require.NoError(t, extract(archive(t, []*tar.Header{h}, []string{"test"}), root))
	info, err := os.Stat(filepath.Join(root, "bin/run"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	require.Error(t, extract(archive(t, []*tar.Header{h}, []string{"test"}), root))
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPrepareVerifiesDigestBeforeExecutingAndReusesCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock runtime requires a POSIX shell")
	}
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{true: "valid", false: "tampered"}[valid], func(t *testing.T) {
			root := t.TempDir()
			script := "#!/bin/sh\nexit 0\n"
			data := archive(t, []*tar.Header{{Name: "python", Typeflag: tar.TypeReg, Size: int64(len(script)), Mode: 0755}}, []string{script})
			sum := sha256.Sum256(data)
			digest := hex.EncodeToString(sum[:])
			if !valid {
				digest = strings.Repeat("0", 64)
			}
			requests := 0
			m := New(root, "2.21.5.2", "http://127.0.0.1:4040", strings.Repeat("x", 64))
			m.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				require.NotContains(t, req.URL.String(), m.token)
				body := data
				if strings.Contains(req.URL.Path, "/releases/tags/") {
					require.Equal(t, "https://api.github.com/repos/"+m.repository+"/releases/tags/v2.21.5.2", req.URL.String())
					body, _ = json.Marshal(map[string]any{"assets": []map[string]string{{"name": "sub2api-reauth_2.21.5.2_linux_" + runtime.GOARCH + ".tar.gz", "digest": "sha256:" + digest}}})
				} else {
					require.Equal(t, "https://github.com/"+m.repository+"/releases/download/v2.21.5.2/sub2api-reauth_2.21.5.2_linux_"+runtime.GOARCH+".tar.gz", req.URL.String())
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}, nil
			})}
			dir, err := m.prepare(context.Background())
			if !valid {
				require.ErrorContains(t, err, "checksum")
				return
			}
			require.NoError(t, err)
			require.FileExists(t, filepath.Join(dir, "ready"))
			_, err = m.prepare(context.Background())
			require.NoError(t, err)
			require.Equal(t, 2, requests)
		})
	}
}

func TestStopCancelsPreparationAndCannotRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux runtime preparation")
	}
	m := New(t.TempDir(), "1.2.3", "http://127.0.0.1:4040", strings.Repeat("x", 64))
	entered := make(chan struct{})
	m.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		close(entered)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	m.Ensure()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("preparation did not start")
	}
	m.Stop()
	require.Equal(t, "unavailable", m.Status().State)
	m.Ensure()
	require.False(t, m.started)
}

func TestManagedProcessGetsOnlyWorkerEnvironmentAndStops(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux process supervision")
	}
	root := t.TempDir()
	m := New(root, "2.21.5.2", "http://127.0.0.1:4040", "synthetic-worker-token")
	dir := m.runtimeDir()
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ready"), []byte("cached"), 0600))
	script := "#!/bin/sh\nprintf '%s' \"$OPENAI_REAUTH_WORKER_TOKEN\" > worker-token\nprintf '%s' \"$DATABASE_PASSWORD\" > unrelated-secret\nprintf '%s' \"$OPENAI_REAUTH_CONCURRENCY\" > concurrency\nexec /bin/sleep 60\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "python"), []byte(script), 0700))
	t.Setenv("OPENAI_REAUTH_CONCURRENCY", "4")
	t.Setenv("DATABASE_PASSWORD", "must-not-inherit")
	m.Ensure()
	defer m.Stop()
	// Child output can precede the parent's running status update after Start.
	// Wait for both independently, including the last environment file write.
	require.Eventually(t, func() bool {
		return m.Status().State == "running"
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		got, err := os.ReadFile(filepath.Join(dir, "concurrency"))
		return err == nil && string(got) == "4"
	}, 5*time.Second, 10*time.Millisecond)
	got, err := os.ReadFile(filepath.Join(dir, "worker-token"))
	require.NoError(t, err)
	require.Equal(t, "synthetic-worker-token", string(got))
	got, err = os.ReadFile(filepath.Join(dir, "unrelated-secret"))
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = os.ReadFile(filepath.Join(dir, "concurrency"))
	require.NoError(t, err)
	require.Equal(t, "4", string(got))
	m.Stop()
	require.Equal(t, "stopped", m.Status().State)
}

func TestReleaseVersionAndRepositoryValidation(t *testing.T) {
	t.Setenv("UPDATE_GITHUB_REPO", "")
	for _, version := range []string{"2.21.5", "v2.21.5", "2.21.5.2", "v2.21.5.2", "2.21.5-rc.1", "2.21.5.2-rc.1"} {
		m := New(t.TempDir(), version, "http://localhost", "test")
		require.Empty(t, m.releaseUnavailableReason(), version)
		require.Equal(t, "wells974/sub2api", m.repository)
		m.Stop()
	}
	for _, version := range []string{"dev", "2.21", "2.21.5.2.1", "2.21.5/../../escape", "2.21.5?x=y"} {
		m := New(t.TempDir(), version, "http://localhost", "test")
		require.Equal(t, "release_required", m.releaseUnavailableReason())
		m.client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid release must not download")
			return nil, nil
		})}
		_, err := m.prepare(context.Background())
		require.Error(t, err)
		m.Stop()
	}
	t.Setenv("UPDATE_GITHUB_REPO", "example/../escape")
	m := New(t.TempDir(), "2.21.5.2", "http://localhost", "test")
	defer m.Stop()
	require.Equal(t, "runtime_source_invalid", m.releaseUnavailableReason())
	_, err := m.prepare(context.Background())
	require.ErrorContains(t, err, "runtime_source_invalid")
}

func TestRuntimeRepositoryChangeDoesNotReuseCachedExecutable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UPDATE_GITHUB_REPO", "")
	original := New(root, "2.21.5.2", "http://localhost", "test")
	defer original.Stop()
	require.NoError(t, os.MkdirAll(original.runtimeDir(), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(original.runtimeDir(), "ready"), []byte("cached"), 0600))
	t.Setenv("UPDATE_GITHUB_REPO", "example/custom-fork")
	m := New(root, "2.21.5.2", "http://localhost", "test")
	defer m.Stop()
	require.NotEqual(t, original.runtimeDir(), m.runtimeDir())
	requested := false
	m.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		requested = true
		require.Equal(t, "https://api.github.com/repos/example/custom-fork/releases/tags/v2.21.5.2", req.URL.String())
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	_, err := m.prepare(context.Background())
	require.Error(t, err)
	require.True(t, requested)
}
