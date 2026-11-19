package service

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/reauthruntime"
	"github.com/stretchr/testify/require"
)

func initialExcelFixture(t *testing.T) (*OpenAIOAuthReauthService, OpenAIReloginCredentials) {
	t.Helper()
	svc, reader, _, _, _, _ := newReauthTestService("acct-1")
	reader.account = nil // Initial authorization must work before any account exists.
	_, err := svc.ClaimExcelInitialLogin("worker-1")
	require.NoError(t, err)
	t.Cleanup(func() {
		svc.excelInitialMu.Lock()
		ids := make([]int64, 0, len(svc.excelInitialJobs))
		for id := range svc.excelInitialJobs {
			ids = append(ids, id)
		}
		svc.excelInitialMu.Unlock()
		for _, id := range ids {
			svc.DeleteExcelInitialLogin(id)
		}
	})
	return svc, OpenAIReloginCredentials{Email: "USER@example.com", Password: "PRIVATE_PASSWORD", MFASecret: "PRIVATE_TOTP"}
}

func TestExcelInitialLoginLifecycleAndIsolation(t *testing.T) {
	svc, entry := initialExcelFixture(t)
	login := NewOpenAIReloginService(nil, svc)
	job, err := login.StartExcelInitialLogin(context.Background(), entry, nil)
	require.NoError(t, err)
	id, err := strconv.ParseInt(job.ID, 10, 64)
	require.NoError(t, err)
	public, err := json.Marshal(job)
	require.NoError(t, err)
	require.NotContains(t, string(public), "PRIVATE_")
	require.Nil(t, job.Credential)
	_, err = login.StartExcelInitialLogin(context.Background(), entry, nil)
	require.ErrorContains(t, err, "already running")
	claim, err := svc.ClaimExcelInitialLogin("worker-1")
	require.NoError(t, err)
	require.Equal(t, id, claim.TaskID)
	require.Equal(t, "excel", claim.OAuthProfile)
	require.Zero(t, claim.AccountID)
	require.Equal(t, entry.Password, claim.Password)
	require.Equal(t, entry.MFASecret, claim.TOTPSecret)
	require.True(t, svc.ExcelInitialLoginActive(id, "worker-1"))
	require.False(t, svc.ExcelInitialLoginActive(id, "other-worker"))
	other, err := svc.ClaimExcelInitialLogin("worker-2")
	require.NoError(t, err)
	require.Nil(t, other)
	credentials := excelTestCredentials("initial", time.Now().Add(time.Hour))
	credentials["password"] = "PRIVATE_PASSWORD"
	credentials["unexpected_setting"] = "PRIVATE_CONFIG"
	require.Error(t, svc.FinishExcelInitialLogin(id, "worker-2", credentials))
	require.NoError(t, svc.FinishExcelInitialLogin(id, "worker-1", credentials))
	result, err := svc.GetExcelInitialLogin(id)
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.Status)
	require.Equal(t, openai.ExcelClientID, result.Credential["client_id"])
	public, err = json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(public), "PRIVATE_")
	result.Credential["access_token"] = "changed"
	again, err := svc.GetExcelInitialLogin(id)
	require.NoError(t, err)
	require.NotEqual(t, "changed", again.Credential["access_token"])
	require.Error(t, svc.FinishExcelInitialLogin(id, "worker-1", credentials))
	require.Empty(t, svc.repo.(*reauthTestRepo).config)
	svc.DeleteExcelInitialLogin(id)
	require.False(t, svc.ExcelInitialLoginActive(id, "worker-1"))
	_, err = svc.GetExcelInitialLogin(id)
	require.Error(t, err)
}

func TestExcelInitialLoginRejectsCredentialsAndCanceledCallbacks(t *testing.T) {
	for _, scenario := range []string{"cancel", "expired_task", "fail", "wrong_client", "wrong_email", "wrong_workspace", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			svc, entry := initialExcelFixture(t)
			login := NewOpenAIReloginService(nil, svc)
			job, err := login.StartExcelInitialLogin(context.Background(), entry, nil)
			require.NoError(t, err)
			claim, err := svc.ClaimExcelInitialLogin("worker-1")
			require.NoError(t, err)
			require.Equal(t, job.ID, strconv.FormatInt(claim.TaskID, 10))
			credentials := excelTestCredentials("initial", time.Now().Add(time.Hour))
			switch scenario {
			case "cancel":
				svc.DeleteExcelInitialLogin(claim.TaskID)
			case "expired_task":
				svc.excelInitialJobs[claim.TaskID].expiresAt = time.Now().Add(-time.Second)
			case "fail":
				credentials = nil
			case "wrong_client":
				credentials["client_id"] = openai.ClientID
			case "wrong_email":
				svc.excelInitialJobs[claim.TaskID].email = "other@example.com"
			case "wrong_workspace":
				credentials["chatgpt_account_id"] = "other-workspace"
			case "expired":
				credentials = excelTestCredentials("expired", time.Now().Add(-time.Hour))
			}
			err = svc.FinishExcelInitialLogin(claim.TaskID, "worker-1", credentials)
			if scenario == "fail" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "PRIVATE_")
			}
			require.False(t, svc.ExcelInitialLoginActive(claim.TaskID, "worker-1"))
			result, err := svc.GetExcelInitialLogin(claim.TaskID)
			if scenario == "cancel" || scenario == "expired_task" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, "failed", result.Status)
				require.Nil(t, result.Credential)
			}
		})
	}
}

func TestExcelInitialLoginPreconditionsAndConcurrentClaim(t *testing.T) {
	svc, entry := initialExcelFixture(t)
	login := NewOpenAIReloginService(nil, svc)
	svc.excelInitialWorkerSeen.Store(0)
	_, err := login.StartExcelInitialLogin(context.Background(), entry, nil)
	require.ErrorContains(t, err, "worker is not ready")
	svc.excelInitialWorkerSeen.Store(time.Now().UnixNano())
	svc.encryptionKeyConfigured = false
	_, err = login.StartExcelInitialLogin(context.Background(), entry, nil)
	require.Error(t, err)
	svc.encryptionKeyConfigured = true
	badProxy := int64(-1)
	_, err = login.StartExcelInitialLogin(context.Background(), entry, &badProxy)
	require.Error(t, err)
	_, err = login.StartExcelInitialLogin(context.Background(), entry, nil)
	require.NoError(t, err)
	var wg sync.WaitGroup
	claims := make(chan *OpenAIOAuthReauthClaim, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := svc.ClaimExcelInitialLogin("worker")
			if err == nil && claim != nil {
				claims <- claim
			}
		}()
	}
	wg.Wait()
	close(claims)
	require.Len(t, claims, 1)
}

func TestExcelInitialLoginRuntimeFailureIsActionable(t *testing.T) {
	svc, entry := initialExcelFixture(t)
	login := NewOpenAIReloginService(nil, svc)
	svc.excelInitialWorkerSeen.Store(0)
	// Invalid versions cannot start any download, even on Linux.
	svc.worker = reauthruntime.New(t.TempDir(), "dev", "http://localhost", "PRIVATE_WORKER_TOKEN")
	t.Cleanup(svc.stopWorker)
	job, err := login.StartExcelInitialLogin(context.Background(), entry, nil)
	require.Nil(t, job)
	require.Equal(t, "OPENAI_REAUTH_WORKER_UNAVAILABLE", infraerrors.Reason(err))
	require.Equal(t, svc.WorkerStatus().Reason, infraerrors.FromError(err).Metadata["worker_reason"])
	require.NotContains(t, err.Error(), "PRIVATE_")
	require.Empty(t, svc.excelInitialJobs)
}

func TestExcelInitialLoginRuntimeNotYetStartedIsPreparing(t *testing.T) {
	svc, entry := initialExcelFixture(t)
	login := NewOpenAIReloginService(nil, svc)
	svc.excelInitialWorkerSeen.Store(0)
	svc.worker = reauthruntime.New(t.TempDir(), "2.21.5.2", "http://localhost", "PRIVATE_WORKER_TOKEN")
	// Freeze the manager in idle so this unit test cannot download a runtime.
	svc.worker.Stop()
	job, err := login.StartExcelInitialLogin(context.Background(), entry, nil)
	require.Nil(t, job)
	require.Equal(t, "EXCEL_LOGIN_WORKER_PREPARING", infraerrors.Reason(err))
	require.Empty(t, svc.excelInitialJobs)
}

func TestExcelInitialLoginUsesSelectedProxyWithoutExistingAccount(t *testing.T) {
	svc, entry := initialExcelFixture(t)
	login := NewOpenAIReloginService(nil, svc)
	proxyID := int64(7)
	svc.accounts.(*reauthTestAccountReader).proxy = &Proxy{ID: proxyID, Protocol: "socks5h", Host: "203.0.113.9", Port: 1080, Status: StatusActive}
	_, err := login.StartExcelInitialLogin(context.Background(), entry, &proxyID)
	require.NoError(t, err)
	claim, err := svc.ClaimExcelInitialLogin("worker-1")
	require.NoError(t, err)
	require.Equal(t, "socks5h://203.0.113.9:1080", claim.ProxyURL)
	require.Zero(t, claim.AccountID)
}

func TestExcelInitialLoginFailureDetailsAreSafeAndOwned(t *testing.T) {
	for _, tc := range []struct{ code, stage, wantCode, wantStage string }{
		{"transport_error", "protocol_connecting", "transport_error", "protocol_connecting"},
		{"password_rejected", "web_login", "password_rejected", "web_login"},
		{"email_verification", "web_login", "email_verification", "web_login"},
		{"phone_verification", "web_login", "phone_verification", "web_login"},
		{"interaction_required", "web_login", "interaction_required", "web_login"},
		{"unexpected_login_step", "web_login", "unexpected_login_step", "web_login"},
		{"PRIVATE_PASSWORD", "PRIVATE_TOKEN", "worker_failed", "starting"},
		{"", "", "worker_failed", "starting"}, // Older workers remain compatible.
	} {
		t.Run(tc.wantCode+tc.code, func(t *testing.T) {
			svc, entry := initialExcelFixture(t)
			login := NewOpenAIReloginService(nil, svc)
			job, err := login.StartExcelInitialLogin(context.Background(), entry, nil)
			require.NoError(t, err)
			claim, err := svc.ClaimExcelInitialLogin("worker-1")
			require.NoError(t, err)
			require.NotNil(t, claim)
			require.Error(t, svc.FailExcelInitialLogin(claim.TaskID, "other-worker", tc.code, tc.stage))
			require.NoError(t, svc.FailExcelInitialLogin(claim.TaskID, "worker-1", tc.code, tc.stage))
			view, err := svc.GetExcelInitialLogin(claim.TaskID)
			require.NoError(t, err)
			require.Equal(t, job.ID, view.ID)
			require.Equal(t, "failed", view.Status)
			require.Equal(t, tc.wantCode, view.ErrorCode)
			require.Equal(t, tc.wantStage, view.ErrorStage)
			raw, err := json.Marshal(view)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "PRIVATE_")
			require.Nil(t, view.Credential)
			require.Error(t, svc.FailExcelInitialLogin(claim.TaskID, "worker-1", "timeout", "web_login"))
		})
	}
}
