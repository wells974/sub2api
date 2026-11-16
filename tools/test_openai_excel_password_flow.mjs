import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import {test} from 'node:test';
import {loginExcelPassword} from './openai_excel_password_flow.mjs';

function fixture(override={}) {
  const options={authBase:'https://auth.openai.com',clientId:'excel-client',
    redirectUri:'https://bps.openai.com/basispoints/extension/test/auth/callback',
    email:'fixture@example.invalid',password:'synthetic-password',totpSecret:'synthetic-totp'};
  const calls=[];let auth;
  const client={jar:{value:()=> 'device'},follow:async url=>{
    calls.push('follow');
    if(url.includes('/api/accounts/authorize?')) {auth=new URL(url);return {finalUrl:'https://auth.openai.com/log-in'};}
    return {finalUrl:url};
  }};
  const helpers={
    isPasswordLoginPage:url=>{try{return new URL(url).pathname==='/log-in/password';}catch{return false;}},
    isAuthLoginPage:url=>new URL(url).pathname==='/log-in',
    getContinueUrl:data=>data?.continue_url,
    createSentinelHeaders:async()=>({'fixture':'sentinel'}),
    authJsonStep:async(c,base,method,path,body)=>{assert.equal(body.username.value,options.email);return {data:{continue_url:base+'/log-in/password'}};},
    verifyPassword:async(c,args)=>{assert.equal(args.password,options.password);calls.push('password');return {mfa:true};},
    isMfaChallengePayload:p=>Boolean(p.mfa),
    completeTotpMfaIfNeeded:async(c,args)=>{assert.equal(args.totpSecret,options.totpSecret);calls.push('totp');return {};},
    selectChatgptLoginWorkspaceIfNeeded:async()=>{calls.push('workspace');return {};},
    continueFlow:async()=>({finalUrl:options.redirectUri+'?code=fixture-code&state='+encodeURIComponent(auth.searchParams.get('state'))}),
    ...override};
  return {client,options,helpers,calls,getAuth:()=>auth};
}

test('password and TOTP stay within the Excel PKCE authorization',async()=>{
  const f=fixture();const result=await loginExcelPassword(f.client,f.options,f.helpers);
  assert.equal(result.mfaVerified,true);
  assert.deepEqual(f.calls,['follow','follow','password','totp','workspace']);
  const q=f.getAuth().searchParams;
  assert.equal(q.get('client_id'),'excel-client');assert.match(q.get('state'),/^bps\..+\.PC$/);
  assert.equal(q.get('scope'),'openid offline_access email profile organization.read');
  assert.equal(q.get('code_challenge'),crypto.createHash('sha256').update(f.client.excelOAuth.codeVerifier).digest('base64url'));
});
test('wrong callback state is never accepted',async()=>{
  const f=fixture({continueFlow:async()=>({finalUrl:'https://bps.openai.com/basispoints/extension/test/auth/callback?code=x&state=wrong'})});
  await assert.rejects(loginExcelPassword(f.client,f.options,f.helpers),/STATE_MISMATCH/);
  assert.equal(f.client.excelOAuth,undefined);
});
test('an email OTP requirement is not bypassed',async()=>{
  const f=fixture({authJsonStep:async()=>({data:{continue_url:'https://auth.openai.com/email-verification'}})});
  await assert.rejects(loginExcelPassword(f.client,f.options,f.helpers),/EMAIL_VERIFICATION_REQUIRED/);
  assert.equal(f.calls.includes('password'),false);
});

test('relative password steps resolve against the authorization origin', async()=>{
  const f=fixture({authJsonStep:async()=>({data:{continue_url:'/log-in/password'}})});
  await loginExcelPassword(f.client,f.options,f.helpers);
  assert.ok(f.client.excelOAuth);
  assert.ok(f.calls.includes('password'));
});

test('an intermediate server redirect may lead to the password page', async()=>{
  const f=fixture({authJsonStep:async()=>({data:{continue_url:'/login-transition'}})});
  const follow=f.client.follow;
  f.client.follow=async(url,options)=>url.endsWith('/login-transition')
    ? {finalUrl:'https://auth.openai.com/log-in/password'} : follow(url,options);
  await loginExcelPassword(f.client,f.options,f.helpers);
  assert.ok(f.client.excelOAuth);
});

for (const [next, code] of [
  [undefined,'EXCEL_UNEXPECTED_LOGIN_STEP'],
  ['/unknown-step','EXCEL_UNEXPECTED_LOGIN_STEP'],
  ['https://untrusted.example/log-in/password','EXCEL_UNEXPECTED_LOGIN_STEP'],
  ['/consent','EXCEL_INTERACTION_REQUIRED'],
  ['/add-phone','EXCEL_PHONE_VERIFICATION_REQUIRED'],
]) {
  test(`classifies ${next || 'missing next step'} without attempting credentials`, async()=>{
    const f=fixture({authJsonStep:async()=>({data:{continue_url:next}})});
    await assert.rejects(loginExcelPassword(f.client,f.options,f.helpers), {message:code});
    assert.equal(f.calls.includes('password'),false);
    assert.equal(f.client.excelOAuth,undefined);
  });
}

test('an email verification redirect after password is identified separately', async()=>{
  const f=fixture({continueFlow:async()=>({finalUrl:'https://auth.openai.com/email-verification'})});
  await assert.rejects(loginExcelPassword(f.client,f.options,f.helpers), {message:'EXCEL_EMAIL_VERIFICATION_REQUIRED'});
  assert.equal(f.client.excelOAuth,undefined);
});
