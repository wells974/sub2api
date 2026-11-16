import crypto from 'node:crypto';

export async function loginExcelPassword(client, options, helpers) {
  const {authBase, email, password, totpSecret, rl, clientId, redirectUri} = options;
  if (authBase !== 'https://auth.openai.com') throw new Error('EXCEL_AUTH_ORIGIN_INVALID');
  function authStepUrl(value) {
    let url;
    try {
      if (typeof value !== 'string' || !value.trim()) throw new Error();
      url = new URL(value, authBase);
    } catch { throw new Error('EXCEL_UNEXPECTED_LOGIN_STEP'); }
    if (url.origin !== authBase || url.username || url.password) throw new Error('EXCEL_UNEXPECTED_LOGIN_STEP');
    return url;
  }
  function checkInteraction(url) {
    if (url.pathname === '/email-verification') throw new Error('EXCEL_EMAIL_VERIFICATION_REQUIRED');
    if (['/add-phone', '/phone-verification', '/phone-verification/verify'].includes(url.pathname)) {
      throw new Error('EXCEL_PHONE_VERIFICATION_REQUIRED');
    }
    if (['/consent', '/about-you', '/choose-an-account'].includes(url.pathname)) throw new Error('EXCEL_INTERACTION_REQUIRED');
  }
  function requirePasswordPage(value) {
    const url = authStepUrl(value);
    checkInteraction(url);
    if (!helpers.isPasswordLoginPage(url.href)) throw new Error('EXCEL_UNEXPECTED_LOGIN_STEP');
  }
  const expected = new URL(redirectUri);
  function normalizePayload(payload) {
    if (!payload || typeof payload !== 'object' || Array.isArray(payload)) throw new Error('EXCEL_UNEXPECTED_LOGIN_STEP');
    const value = helpers.getContinueUrl(payload);
    if (!value) {
      const page = payload.page?.type;
      if (page === 'email_verification') throw new Error('EXCEL_EMAIL_VERIFICATION_REQUIRED');
      if (page === 'add_phone' || page === 'phone_verification') throw new Error('EXCEL_PHONE_VERIFICATION_REQUIRED');
      if (page === 'consent' || page === 'about_you') throw new Error('EXCEL_INTERACTION_REQUIRED');
      return payload;
    }
    let url;
    try {
      if (typeof value !== 'string') throw new Error();
      url = new URL(value, authBase);
    } catch { throw new Error('EXCEL_UNEXPECTED_LOGIN_STEP'); }
    if (url.username || url.password || (url.origin !== authBase &&
        (url.origin !== expected.origin || url.pathname !== expected.pathname))) {
      throw new Error('EXCEL_UNEXPECTED_LOGIN_STEP');
    }
    // All downstream helpers parse absolute URLs, including MFA/workspace
    // detection and Referer construction. Preserve the rest of the payload.
    return {...payload, continue_url: url.href};
  }
  const verifier = crypto.randomBytes(48).toString('base64url');
  const state = `bps.${crypto.randomBytes(24).toString('base64url')}.PC`;
  const query = new URLSearchParams({client_id:clientId, redirect_uri:redirectUri,
    response_type:'code', scope:'openid offline_access email profile organization.read',
    audience:'https://api.openai.com/v1', platform:'PC', state,
    code_challenge_method:'S256', code_challenge:crypto.createHash('sha256').update(verifier).digest('base64url')});
  console.log('[excel] Start official Excel PKCE authorization.');
  let page = await client.follow(`${authBase}/api/accounts/authorize?${query}`);
  const deviceId = client.jar.value('oai-did', authBase) || crypto.randomUUID();
  if (!helpers.isPasswordLoginPage(page.finalUrl)) {
    const initial = authStepUrl(page.finalUrl);
    checkInteraction(initial);
    if (!helpers.isAuthLoginPage(initial.href)) throw new Error('EXCEL_UNEXPECTED_LOGIN_STEP');
    const headers = await helpers.createSentinelHeaders(client, {authBase, deviceId, flow:'authorize_continue'});
    const {data} = await helpers.authJsonStep(client, authBase, 'POST', '/api/accounts/authorize/continue',
      {username:{kind:'email',value:email}}, {referer:page.finalUrl, headers});
    // The server can return a relative URL or an intermediate redirect. Resolve
    // and follow that step before deciding whether it is a password page.
    const next = authStepUrl(helpers.getContinueUrl(normalizePayload(data)));
    page = await client.follow(next.href, {referer:page.finalUrl});
  }
  requirePasswordPage(page.finalUrl);
  let payload = normalizePayload(await helpers.verifyPassword(client,{authBase, rl, deviceId, password, referer:page.finalUrl}));
  const mfaRequired = helpers.isMfaChallengePayload(payload);
  payload = normalizePayload(await helpers.completeTotpMfaIfNeeded(client,{authBase,rl,deviceId,payload,totpSecret,referer:page.finalUrl}));
  payload = normalizePayload(await helpers.selectChatgptLoginWorkspaceIfNeeded(client,{authBase,payload}));
  const continued = await helpers.continueFlow(client,payload);
  const callback = new URL(continued?.finalUrl || helpers.getContinueUrl(payload) || authBase, authBase);
  if (callback.origin === authBase) {
    checkInteraction(callback);
    throw new Error('EXCEL_UNEXPECTED_LOGIN_STEP');
  }
  if (callback.origin !== expected.origin || callback.pathname !== expected.pathname ||
      callback.username || callback.password || callback.hash || callback.searchParams.has('error') ||
      callback.searchParams.getAll('state').length !== 1 || callback.searchParams.get('state') !== state ||
      callback.searchParams.getAll('code').length !== 1 || !callback.searchParams.get('code')) throw new Error('EXCEL_CALLBACK_STATE_MISMATCH');
  client.excelOAuth = {callbackUrl:callback.toString(),codeVerifier:verifier,state};
  console.log('[excel] Password/TOTP authorization completed.');
  return {deviceId,emailVerified:true,loginMethod:'password',mfaVerified:mfaRequired};
}
