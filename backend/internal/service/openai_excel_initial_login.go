package service

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Initial authorization has no account yet. Reuse the Excel worker profile,
// then let the existing Session importer create/deduplicate a standalone grant.
// Secrets/results are encrypted, bounded, short lived, and never written to the
// legacy guard or to a placeholder account.
type excelInitialLogin struct {
	id                                   int64
	email, worker, status, input, result string
	errorCode, errorStage                string
	expiry                               *time.Timer
	expiresAt                            time.Time
}

type excelInitialInput struct {
	AccountTokenGuardReloginAccount
	ProxyURL string `json:"proxy_url"`
}

func (s *OpenAIOAuthReauthService) StartExcelInitialLogin(ctx context.Context, entry AccountTokenGuardReloginAccount, proxyID *int64) (*OpenAITwoFALoginJob, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}
	if err := ValidateOpenAITwoFALogin(entry); err != nil {
		return nil, infraerrors.BadRequest("EXCEL_LOGIN_INVALID", "Valid email, password and 2FA secret are required")
	}
	if err := s.ensureDurableEncryption(); err != nil {
		return nil, err
	}
	if err := s.checkWorkerMode(OpenAIOAuthReauthModePasswordTOTP); err != nil {
		return nil, err
	}
	if seen := s.excelInitialWorkerSeen.Load(); seen == 0 || time.Since(time.Unix(0, seen)) > 26*time.Minute {
		if status := s.WorkerStatus(); status.State == "preparing" || status.State == "idle" {
			return nil, infraerrors.ServiceUnavailable("EXCEL_LOGIN_WORKER_PREPARING", "BPS automatic login runtime is preparing; retry when Credential Operations shows it is ready")
		}
		return nil, infraerrors.ServiceUnavailable("EXCEL_LOGIN_WORKER_UNAVAILABLE", "BPS automatic login worker is not ready; check Credential Operations or use manual authorization")
	}
	proxy, err := s.reauthProxyURL(ctx, proxyID)
	if err != nil {
		return nil, infraerrors.BadRequest("EXCEL_LOGIN_PROXY_UNAVAILABLE", "Selected login proxy is unavailable")
	}
	entry.Email = strings.ToLower(strings.TrimSpace(entry.Email))
	entry.MFASecret = strings.TrimSpace(entry.MFASecret)
	raw, err := json.Marshal(excelInitialInput{entry, proxy})
	if err != nil {
		return nil, credentialEncryptionStorageError()
	}
	encrypted, err := s.encryptor.Encrypt(string(raw))
	if err != nil {
		return nil, credentialEncryptionStorageError()
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, infraerrors.ServiceUnavailable("EXCEL_LOGIN_UNAVAILABLE", "Cannot create login task")
	}
	// Safe through JSON/JavaScript, separate namespace from persisted reauth IDs.
	id := int64(binary.BigEndian.Uint64(random[:])&((1<<52)-1)) + 1
	s.excelInitialMu.Lock()
	defer s.excelInitialMu.Unlock()
	if len(s.excelInitialJobs) >= 32 || s.excelInitialJobs[id] != nil {
		return nil, infraerrors.New(http.StatusTooManyRequests, "EXCEL_LOGIN_BUSY", "Too many login tasks; retry later")
	}
	for _, job := range s.excelInitialJobs {
		if job.email == entry.Email && job.status == "running" {
			return nil, infraerrors.New(http.StatusConflict, "EXCEL_LOGIN_IN_PROGRESS", "A BPS login for this email is already running")
		}
	}
	if s.excelInitialJobs == nil {
		s.excelInitialJobs = make(map[int64]*excelInitialLogin)
	}
	job := &excelInitialLogin{id: id, email: entry.Email, status: "running", input: encrypted, expiresAt: time.Now().Add(30 * time.Minute)}
	s.excelInitialJobs[id] = job
	job.expiry = time.AfterFunc(30*time.Minute, func() { s.DeleteExcelInitialLogin(id) })
	return &OpenAITwoFALoginJob{ID: strconv.FormatInt(id, 10), Status: job.status}, nil
}

func (s *OpenAIOAuthReauthService) GetExcelInitialLogin(id int64) (*OpenAITwoFALoginJob, error) {
	s.excelInitialMu.Lock()
	defer s.excelInitialMu.Unlock()
	job := s.excelInitialJobs[id]
	if job == nil || !time.Now().Before(job.expiresAt) {
		return nil, infraerrors.NotFound("EXCEL_LOGIN_NOT_FOUND", "Login task expired or was deleted")
	}
	view := &OpenAITwoFALoginJob{ID: strconv.FormatInt(id, 10), Status: job.status, ErrorCode: job.errorCode, ErrorStage: job.errorStage}
	if job.result != "" {
		raw, err := s.encryptor.Decrypt(job.result)
		if err != nil || json.Unmarshal([]byte(raw), &view.Credential) != nil {
			return nil, credentialEncryptionStorageError()
		}
	}
	return view, nil
}

func (s *OpenAIOAuthReauthService) DeleteExcelInitialLogin(id int64) {
	s.excelInitialMu.Lock()
	defer s.excelInitialMu.Unlock()
	if job := s.excelInitialJobs[id]; job != nil {
		job.expiry.Stop()
		job.input, job.result = "", ""
		delete(s.excelInitialJobs, id)
	}
}

func (s *OpenAIOAuthReauthService) ClaimExcelInitialLogin(worker string) (*OpenAIOAuthReauthClaim, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}
	worker = strings.TrimSpace(worker)
	if worker == "" || len(worker) > 128 {
		return nil, infraerrors.BadRequest("EXCEL_LOGIN_WORKER_INVALID", "Invalid worker ID")
	}
	s.excelInitialWorkerSeen.Store(time.Now().UnixNano())
	s.excelInitialMu.Lock()
	defer s.excelInitialMu.Unlock()
	for _, job := range s.excelInitialJobs {
		if job.status != "running" || job.worker != "" || !time.Now().Before(job.expiresAt) {
			continue
		}
		raw, err := s.encryptor.Decrypt(job.input)
		var input excelInitialInput
		if err != nil || json.Unmarshal([]byte(raw), &input) != nil {
			job.status = "failed"
			job.errorCode, job.errorStage = "credential_storage_failed", "starting"
			job.input = ""
			continue
		}
		job.worker, job.input = worker, ""
		return &OpenAIOAuthReauthClaim{TaskID: job.id, OAuthProfile: "excel", CredentialMode: OpenAIOAuthReauthModePasswordTOTP,
			Engine: OpenAIOAuthReauthEngineLocal, LoginEmail: input.Email, Password: input.Password, TOTPSecret: input.MFASecret, ProxyURL: input.ProxyURL}, nil
	}
	return nil, nil
}

func (s *OpenAIOAuthReauthService) ExcelInitialLoginActive(id int64, worker string) bool {
	s.excelInitialMu.Lock()
	defer s.excelInitialMu.Unlock()
	job := s.excelInitialJobs[id]
	return job != nil && time.Now().Before(job.expiresAt) && job.status == "running" && job.worker != "" && job.worker == worker
}

// A callback cannot write to an account or revive a canceled/expired task.
func (s *OpenAIOAuthReauthService) FinishExcelInitialLogin(id int64, worker string, credentials map[string]any) error {
	if credentials == nil {
		return s.FailExcelInitialLogin(id, worker, "worker_failed", "starting")
	}
	s.excelInitialMu.Lock()
	defer s.excelInitialMu.Unlock()
	job := s.excelInitialJobs[id]
	if job == nil || !time.Now().Before(job.expiresAt) || job.status != "running" || worker == "" || job.worker != worker {
		return infraerrors.New(http.StatusConflict, "EXCEL_LOGIN_NOT_ACTIVE", "Login task is no longer active")
	}
	info, err := excelOAuthTokenInfo(credentials)
	if err != nil || !strings.EqualFold(info.Email, job.email) {
		job.status = "failed"
		job.errorCode, job.errorStage = "invalid_credentials", "applying_credentials"
		return infraerrors.BadRequest("EXCEL_LOGIN_IDENTITY_MISMATCH", "BPS credentials do not match the requested account")
	}
	// Build a fresh allowlisted result; worker-provided configuration is ignored.
	clean := s.oauth.BuildAccountCredentials(info)
	clean["email"] = job.email
	raw, err := json.Marshal(clean)
	if err == nil {
		job.result, err = s.encryptor.Encrypt(string(raw))
	}
	if err != nil {
		job.status = "failed"
		job.errorCode, job.errorStage = "credential_storage_failed", "applying_credentials"
		return credentialEncryptionStorageError()
	}
	job.status = "succeeded"
	return nil
}

// Only bounded public diagnostics survive the private worker callback.
func (s *OpenAIOAuthReauthService) FailExcelInitialLogin(id int64, worker, code, stage string) error {
	s.excelInitialMu.Lock()
	defer s.excelInitialMu.Unlock()
	job := s.excelInitialJobs[id]
	if job == nil || !time.Now().Before(job.expiresAt) || job.status != "running" || worker == "" || job.worker != worker {
		return infraerrors.New(http.StatusConflict, "EXCEL_LOGIN_NOT_ACTIVE", "Login task is no longer active")
	}
	if !validExcelLoginFailureCode(code) {
		code = "worker_failed"
	}
	if !validExcelLoginFailureStage(stage) {
		stage = "starting"
	}
	job.status, job.errorCode, job.errorStage = "failed", code, stage
	return nil
}

func validExcelLoginFailureCode(code string) bool {
	switch code {
	case "runtime_error", "transport_error", "security_check", "additional_verification",
		"email_verification", "phone_verification", "interaction_required", "unexpected_login_step",
		"password_rejected", "totp_rejected", "callback_state_mismatch", "workspace_not_listed",
		"client_id", "session_selection", "protocol_error", "timeout", "invalid_credentials", "worker_failed":
		return true
	}
	return false
}

func validExcelLoginFailureStage(stage string) bool {
	switch stage {
	case "starting", "protocol_connecting", "web_login", "password_accepted", "mfa_challenge",
		"mfa_accepted", "excel_authorize", "excel_token_exchange", "applying_credentials":
		return true
	}
	return false
}
