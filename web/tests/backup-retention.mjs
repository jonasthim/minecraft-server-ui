// Intercepted fixtures only, no game, services, EULA or real archives.
// TASK31D_URL=http://127.0.0.1:15231 node web/tests/backup-retention.mjs
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { chromium } from 'playwright-core';
const base = process.env.TASK31D_URL ?? 'http://127.0.0.1:15231';
const shots = process.env.TASK31D_SCREENSHOTS ?? '/tmp/task31d-retention';
await mkdir(shots, { recursive: true });
const browser = await chromium.launch({ headless: true });
try {
 const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
 const page = await context.newPage();page.setDefaultTimeout(5000);const errors=[];page.on('pageerror',err=>errors.push(err.message));
 let role='admin',account=1,fail=false,conflict=false,holdRead=false,holdSave=false;
 let releaseRead,releaseSave;let heldRead=false,heldSave=false;const puts=[];
 const policies={alpha:{keep_last:10,keep_days:30,revision:0},beta:{keep_last:5,keep_days:7,revision:2}};
 const detail=id=>({instance:{id,name:id,flavor:'vanilla',minecraft_version:'1.21.5',memory_mib:1024,game_port:25565},config:{},eula:{accepted:false},provisioning:'not_installed'});
 await page.route('**/api/v1/**',async route=>{
  const path=new URL(route.request().url()).pathname.replace('/api/v1','');const json=body=>route.fulfill({json:body});
  if(path==='/auth/status')return json({needs_setup:false,local_login_enabled:true,oidc_enabled:false});
  if(path==='/auth/me')return json({user:{id:account,username:'fixture',display_name:'Fixture',email:'',role,disabled:false,local:true},csrf_token:'fixture'});
  if(path==='/auth/logout')return route.fulfill({status:204});
  if(path==='/jobs')return json({jobs:[]});
  if(path==='/instances')return json({instances:[detail('alpha'),detail('beta')]});
  if(/^\/instances\/(alpha|beta)$/.test(path))return json({detail:detail(path.split('/')[2])});
  if(path.endsWith('/lifecycle'))return json({installed:false,process:'stopped',process_running:false,readiness:'stopped',autostart_desired:false,autostart_applied:false});
  if(path.endsWith('/players'))return json({snapshot:{known:false,players:[],reason:'Intercepted fixture'}});
  if(path.endsWith('/metrics'))return json({points:[]});
  if(path.endsWith('/backups'))return json({backups:[]});
  if(path.endsWith('/backups/retention')){
   const id=path.split('/')[2];const captured={...policies[id]};
   if(route.request().method()==='GET'){
    if(holdRead){holdRead=false;heldRead=true;await new Promise(resolve=>{releaseRead=resolve});}
    return json(captured);
   }
   const input=route.request().postDataJSON();puts.push({id,input});
   if(holdSave){holdSave=false;heldSave=true;await new Promise(resolve=>{releaseSave=resolve});}
   if(fail)return route.fulfill({status:503,json:{error:{code:'unavailable',message:'Fixture policy save failed'}}});
   if(conflict){conflict=false;policies[id]={keep_last:8,keep_days:14,revision:policies[id].revision+1};return route.fulfill({status:409,json:{error:{code:'conflict',message:'Fixture policy changed'}}});}
   policies[id]={keep_last:input.keep_last,keep_days:input.keep_days,revision:policies[id].revision+1};return json(policies[id]);
  }
  throw Error('Unexpected fixture request '+path);
 });
 const count=()=>page.getByRole('textbox',{name:'Automatic archives to retain',exact:true});
 const days=()=>page.getByRole('textbox',{name:'Maximum age in days',exact:true});
 const save=()=>page.getByRole('button',{name:'Save retention policy',exact:true});
 await page.goto(base+'/instances/alpha/backups');
 await page.getByText('Effective policy: 10 automatic archives; 30 days.',{exact:true}).waitFor();
 await page.getByText(/0 disables that limit/).waitFor();
 await count().fill('0');await days().fill('0');await save().click();
 await page.getByRole('status').filter({hasText:'Retention policy saved'}).waitFor();
 assert.deepEqual(puts.at(-1),{id:'alpha',input:{keep_last:0,keep_days:0,expected_revision:0}});
 await page.getByText('Effective policy: unlimited automatic archives; no age limit.',{exact:true}).waitFor();
 await page.getByRole('button',{name:'Use default values',exact:true}).click();
 assert.equal(await count().inputValue(),'10');assert.equal(await days().inputValue(),'30');
 assert.equal(policies.alpha.keep_last,0,'reset only edits until explicit save');
 await save().click();await page.getByRole('status').filter({hasText:'Retention policy saved'}).waitFor();
 await count().fill('3');fail=true;await save().click();
 await page.getByRole('alert').filter({hasText:'Fixture policy save failed'}).waitFor();
 assert.equal(await count().inputValue(),'3');fail=false;conflict=true;await save().click();
 await page.getByRole('alert').filter({hasText:'Fixture policy changed'}).waitFor();
 await page.getByRole('button',{name:'Reload current policy, keep my edits',exact:true}).click();
 await page.getByText('Effective policy: 8 automatic archives; 14 days.',{exact:true}).waitFor();
 assert.equal(await count().inputValue(),'3','stale reload must preserve edits');assert.equal(await days().inputValue(),'30');
 await save().click();await page.getByRole('status').filter({hasText:'Retention policy saved'}).waitFor();
 assert.equal(puts.at(-1).input.expected_revision,3);
 await count().fill('-1');assert.equal(await save().isDisabled(),true);await count().fill('3');
 await count().focus();await page.keyboard.press('Tab');assert.equal(await days().evaluate(el=>el===document.activeElement),true);
 assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
 await page.evaluate(()=>{document.activeElement?.blur();scrollTo(0,0);});
 await page.screenshot({path:shots+'/retention-admin-mobile.png',fullPage:true});
 holdRead=true;await page.getByRole('button',{name:'Refresh retention policy',exact:true}).click();
 for(let i=0;i<100&&!heldRead;i++)await page.waitForTimeout(10);assert(heldRead);
 await page.evaluate(()=>{history.pushState(null,'','/instances/beta/backups');dispatchEvent(new PopStateEvent('popstate'));});
 await page.getByText('Effective policy: 5 automatic archives; 7 days.',{exact:true}).waitFor();releaseRead();
 await page.waitForTimeout(100);assert.equal(await count().inputValue(),'5');
 holdSave=true;await count().fill('4');await save().click();for(let i=0;i<100&&!heldSave;i++)await page.waitForTimeout(10);assert(heldSave);
 await page.evaluate(()=>{history.pushState(null,'','/instances/alpha/backups');dispatchEvent(new PopStateEvent('popstate'));});
 await page.getByText('Effective policy: 3 automatic archives; 30 days.',{exact:true}).waitFor();releaseSave();await page.waitForTimeout(100);
 assert.equal(await page.getByRole('status').filter({hasText:'Retention policy saved'}).count(),0);
 // Same-role account replacement discards an outstanding old-account read.
 await page.setViewportSize({width:1440,height:1000});
 holdRead=true;heldRead=false;await page.getByRole('button',{name:'Refresh retention policy',exact:true}).click();
 for(let i=0;i<100&&!heldRead;i++)await page.waitForTimeout(10);assert(heldRead);
 policies.alpha={keep_last:6,keep_days:15,revision:5};account++;
 await page.getByRole('button',{name:'Sign out',exact:true}).click();
 await page.getByText('Effective policy: 6 automatic archives; 15 days.',{exact:true}).waitFor();releaseRead();await page.waitForTimeout(100);
 assert.equal(await count().inputValue(),'6');
 await page.evaluate(()=>{document.activeElement?.blur();scrollTo(0,0);});
 await page.screenshot({path:shots+'/retention-admin-desktop.png',fullPage:true});
 // Role replacement while a save is in flight cannot restore an admin action or notice.
 holdSave=true;heldSave=false;await count().fill('4');await save().click();
 for(let i=0;i<100&&!heldSave;i++)await page.waitForTimeout(10);assert(heldSave);
 role='viewer';account++;await page.getByRole('button',{name:'Sign out',exact:true}).click();
 await page.getByText('Effective policy: 6 automatic archives; 15 days.',{exact:true}).waitFor();releaseSave();await page.waitForTimeout(100);
 assert.equal(await save().count(),0);assert.equal(await page.getByRole('status').filter({hasText:'Retention policy saved'}).count(),0);
 for(const nextRole of ['operator','viewer']){role=nextRole;account++;await page.reload();await page.getByText('Effective policy: 4 automatic archives; 15 days.',{exact:true}).waitFor();assert.equal(await save().count(),0);assert.equal(await count().count(),0);}
 assert.deepEqual(errors,[]);
 console.log('PASS: defaults, zero limits, save/reset, preserved failed/stale edits, CAS reload, role visibility, instance/account/role read/save cancellation, keyboard and 390px. Intercepted fixtures only.');
 await context.close();
}finally{await browser.close();}
