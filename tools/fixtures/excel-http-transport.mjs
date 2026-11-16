// Exercise the pinned CLI and its real login helpers without any network access.
import assert from 'node:assert/strict';
import {createHmac} from 'node:crypto';
let fixtureTime = 1800000029999, mfaIssued = false;
if (process.env.EXCEL_TEST_MODE === 'totp-window') Date.now = () => fixtureTime;
function currentTotp() {
  const counter = Buffer.alloc(8); counter.writeBigUInt64BE(BigInt(Math.floor(Date.now()/30000)));
  const digest = createHmac('sha1', Buffer.from('48656c6c6f21deadbeef','hex')).update(counter).digest();
  const offset = digest[digest.length-1] & 15;
  return String((digest.readUInt32BE(offset) & 0x7fffffff) % 1000000).padStart(6,'0');
}
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
  async generateSentinelTokens() {
    if (process.env.EXCEL_TEST_MODE==='totp-window' && mfaIssued) fixtureTime += 30000;
    return {token:'synthetic-sentinel'};
  }
  async fetch(url, options) {
    const target = new URL(url);
    assert.equal(target.origin, 'https://auth.openai.com');
    const route = `${options.method} ${target.pathname}`;
    const mode = process.env.EXCEL_TEST_MODE || 'success';
    const callback = () => {
      const url = new URL(redirect);
      url.search = new URLSearchParams({code:'synthetic-code',state:mode==='wrong-state'?'wrong':state});
      if (mode==='duplicate-state') url.searchParams.append('state','wrong');
      if (mode==='duplicate-code') url.searchParams.append('code','other');
      if (mode==='callback-error') url.searchParams.set('error','access_denied');
      if (mode==='callback-fragment') url.hash='unexpected';
      return url.href;
    };
    const workspace = () => mode==='workspace-relative'
      ? {page:{payload:{url:'/workspace'}},'oai-client-auth-session':{workspaces:[{id:'expected-workspace',kind:'organization'}]}}
      : {page:{type:'workspace'},continue_url:'https://auth.openai.com/workspace','oai-client-auth-session':{workspaces:[{id:'expected-workspace',kind:'organization'}]}};
    if (route==='GET /api/accounts/authorize') {
      state = target.searchParams.get('state');
      challenge = target.searchParams.get('code_challenge');
      assert.equal(target.searchParams.get('client_id'), clientId);
      assert.equal(target.searchParams.get('redirect_uri'), redirect);
      if (mode==='transport') throw new Error('fetch failed PRIVATE_TOKEN');
      if (mode==='security') throw new Error('PROXY_RISK_CONTROL PRIVATE_TOKEN');
      return new Response('',{status:302,headers:{location:mode==='direct-password'?'/log-in/password':'/log-in','set-cookie':'oai-did=synthetic-device; Path=/; Secure'}});
    }
    if (route==='GET /login-transition') return new Response('',{status:302,headers:{location:mode==='foreign-redirect'?'https://untrusted.example/log-in/password':'/log-in/password'}});
    if (route==='GET /oauth-finish') return new Response('',{status:302,headers:{location:callback()}});
    if (['GET /log-in','GET /log-in/password','GET /unknown-step','GET /email-verification','GET /add-phone','GET /consent'].includes(route)) return new Response('<html>Offline</html>',{headers:{'content-type':'text/html'}});
    if (route==='POST /api/accounts/authorize/continue') {
      assert.equal(JSON.parse(options.body).username.value,'test@example.test');
      if (mode==='email-before-password-metadata') return Response.json({page:{type:'email_verification'}});
      if (mode==='missing-step') return Response.json({page:{type:'unknown'}});
      if (mode==='nested-relative') return Response.json({page:{payload:{url:'/log-in/password'}}});
      const next = {'foreign-redirect':'/login-transition',relative:'/log-in/password',redirect:'/login-transition',unknown:'/unknown-step',
        'foreign-origin':'https://untrusted.example/log-in/password',consent:'/consent',phone:'/add-phone'}[mode];
      if (next) return Response.json({continue_url:next});
      return Response.json({continue_url:'https://auth.openai.com/'+(mode==='additional'?'email-verification':'log-in/password')});
    }
    if (route==='POST /api/accounts/password/verify') {
      assert.equal(JSON.parse(options.body).password,'synthetic-password');
      if (mode==='password') return Response.json({error:{message:'invalid_password'}},{status:400});
      if (mode==='no-mfa') return Response.json(workspace());
      if (mode==='post-password-foreign') return Response.json({continue_url:'https://untrusted.example/step'});
      if (mode==='email-metadata') return Response.json({page:{type:'email_verification'}});
      if (mode==='non-totp-factor') return Response.json({page:{type:'mfa_challenge'},'oai-client-auth-session':{mfa_factors:[{factor_type:'sms',id:'test-factor'}]}});
      if (mode==='mfa-relative') return Response.json({continue_url:'/mfa-challenge/test-factor','oai-client-auth-session':{mfa_factors:[{factor_type:'totp',id:'test-factor'}]}});
      return Response.json({page:{type:'mfa_challenge'},'oai-client-auth-session':{mfa_factors:[{factor_type:'totp',id:'test-factor'}]}});
    }
    if (route==='POST /api/accounts/mfa/issue_challenge') {mfaIssued=true; return Response.json({});}
    if (route==='POST /api/accounts/mfa/verify') {
      assert.match(JSON.parse(options.body).code,/^\d{6}$/);
      if (mode==='totp-window') assert.equal(JSON.parse(options.body).code,currentTotp());
      if (mode==='totp') return Response.json({error:{message:'invalid_totp'}},{status:400});
      if (mode==='no-workspace') return Response.json({continue_url:callback()});
      if (mode==='workspace-relative') assert.ok(options.headers.get('referer').startsWith('https://auth.openai.com/'));
      return Response.json(workspace());
    }
    if (route==='POST /api/accounts/workspace/select') {
      assert.equal(JSON.parse(options.body).workspace_id,'expected-workspace');
      assert.equal(options.headers.get('referer'),'https://auth.openai.com/workspace');
      return Response.json({continue_url:mode==='final-relative'?'/oauth-finish':callback()});
    }
    if (route==='POST /oauth/token') {
      const form = new URLSearchParams(options.body);
      const {createHash} = await import('node:crypto');
      assert.equal(form.get('client_id'),clientId);
      assert.equal(form.get('code'),'synthetic-code');
      assert.equal(createHash('sha256').update(form.get('code_verifier')).digest('base64url'),challenge);
      assert.equal(target.searchParams.get('unified'),'true');
      return Response.json({access_token:jwt({client_id:clientId,exp:4000000000,'https://api.openai.com/auth':{chatgpt_account_id:'expected-workspace',...(mode==='user-id-alias'?{user_id:'synthetic-user'}:{chatgpt_user_id:'synthetic-user'})}}),
        refresh_token:'synthetic-refresh',id_token:jwt({aud:clientId,email:'test@example.test',exp:4000000000})});
    }
    throw new Error('UNEXPECTED_OFFLINE_HTTP_REQUEST');
  }
}
