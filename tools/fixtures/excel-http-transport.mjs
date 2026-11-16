// Exercise the pinned CLI and its real login helpers without any network access.
import assert from 'node:assert/strict';
export const shouldUseTlsTransport = () => true;
export const browserIdentityForTlsProfile = () => ({userAgent:'offline-test',acceptLanguage:'en-US'});
let state, challenge;
const clientId = 'app_fnr0pYvVwwFDocDumLG3H2Bp';
const redirect = 'https://bps.openai.com/basispoints/extension/360590d7-f8f9-4d88-bf75-0edfe0a4b9f3/auth/callback';
const jwt = claims => 'e30.' + Buffer.from(JSON.stringify(claims)).toString('base64url') + '.synthetic';
export class TlsFingerprintTransport {
  enabled = true;
  async prepareProxy(_proxy, url) { assert.equal(new URL(url).origin, 'https://auth.openai.com'); }
  async close() {}
  async generateSentinelTokens() { return {token:'synthetic-sentinel'}; }
  async fetch(url, options) {
    const target = new URL(url);
    assert.equal(target.origin, 'https://auth.openai.com');
    const route = `${options.method} ${target.pathname}`;
    const mode = process.env.EXCEL_TEST_MODE || 'success';
    const callback = () => redirect + '?' + new URLSearchParams({code:'synthetic-code',state:mode==='wrong-state'?'wrong':state});
    if (route==='GET /api/accounts/authorize') {
      state = target.searchParams.get('state');
      challenge = target.searchParams.get('code_challenge');
      assert.equal(target.searchParams.get('client_id'), clientId);
      assert.equal(target.searchParams.get('redirect_uri'), redirect);
      if (mode==='transport') throw new Error('fetch failed PRIVATE_TOKEN');
      if (mode==='security') throw new Error('PROXY_RISK_CONTROL PRIVATE_TOKEN');
      return new Response('',{status:302,headers:{location:'/log-in','set-cookie':'oai-did=synthetic-device; Path=/; Secure'}});
    }
    if (route==='GET /log-in' || route==='GET /log-in/password') return new Response('<html>Offline</html>',{headers:{'content-type':'text/html'}});
    if (route==='POST /api/accounts/authorize/continue') {
      assert.equal(JSON.parse(options.body).username.value,'test@example.test');
      return Response.json({continue_url:'https://auth.openai.com/'+(mode==='additional'?'email-verification':'log-in/password')});
    }
    if (route==='POST /api/accounts/password/verify') {
      assert.equal(JSON.parse(options.body).password,'synthetic-password');
      if (mode==='password') return Response.json({error:{message:'invalid_password'}},{status:400});
      return Response.json({page:{type:'mfa_challenge'},'oai-client-auth-session':{mfa_factors:[{factor_type:'totp',id:'test-factor'}]}});
    }
    if (route==='POST /api/accounts/mfa/issue_challenge') return Response.json({});
    if (route==='POST /api/accounts/mfa/verify') {
      assert.match(JSON.parse(options.body).code,/^\d{6}$/);
      if (mode==='totp') return Response.json({error:{message:'invalid_totp'}},{status:400});
      return Response.json({page:{type:'workspace'},continue_url:'https://auth.openai.com/workspace','oai-client-auth-session':{workspaces:[{id:'expected-workspace',kind:'organization'}]}});
    }
    if (route==='POST /api/accounts/workspace/select') {
      assert.equal(JSON.parse(options.body).workspace_id,'expected-workspace');
      return Response.json({continue_url:callback()});
    }
    if (route==='POST /oauth/token') {
      const form = new URLSearchParams(options.body);
      const {createHash} = await import('node:crypto');
      assert.equal(form.get('client_id'),clientId);
      assert.equal(form.get('code'),'synthetic-code');
      assert.equal(createHash('sha256').update(form.get('code_verifier')).digest('base64url'),challenge);
      assert.equal(target.searchParams.get('unified'),'true');
      return Response.json({access_token:jwt({client_id:clientId,exp:4000000000,'https://api.openai.com/auth':{chatgpt_account_id:'expected-workspace',chatgpt_user_id:'synthetic-user'}}),
        refresh_token:'synthetic-refresh',id_token:jwt({aud:clientId,email:'test@example.test',exp:4000000000})});
    }
    throw new Error('UNEXPECTED_OFFLINE_HTTP_REQUEST');
  }
}
