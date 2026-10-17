// Intercepted shipping-client fixtures; no backend, service, game or DB qualification.
// TASK31D2_URL=http://127.0.0.1:15232 node web/tests/pending-settings.mjs
import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { chromium } from 'playwright-core';
const base=process.env.TASK31D2_URL??'http://127.0.0.1:15232';
const shots='/tmp/task31d2-ui';await mkdir(shots,{recursive:true});
const browser=await chromium.launch({headless:true});
try{
 const context=await browser.newContext({viewport:{width:390,height:844}});const page=await context.newPage();page.setDefaultTimeout(5000);
 const errors=[];page.on('pageerror',error=>errors.push(error.message));
 let role='admin',account=1,conflict=false,fail=false,readFail=false,holdRead=false,holdSave=false,releaseRead,releaseSave,heldRead=false,heldSave=false;
 const writes=[];
 const config={motd:'Applied message',difficulty:'normal',game_mode:'survival',max_players:20,online_mode:true,pvp:true,view_distance:10,simulation_distance:10,level_name:'world',level_seed:'',extra:{'custom-property':'preserved'}};
 const capability={available:true,reason:'Fixture native setting'};
 const details=Object.fromEntries(['alpha','beta'].map((id,index)=>[id,{instance:{id,name:id,flavor:'vanilla',minecraft_version:'1.21.5',memory_mib:1024,java_major:21,game_port:25565+index,updated_at:'2026-10-17T10:00:00Z'},config:{...config},settings:{pvp:capability,simulation_distance:capability,seed_reason:'Changing seed does not reset the world.'},eula:{accepted:false},provisioning:'installed'}]));
 const pending={alpha:{revision:0,draft:null,stale:false,base_updated_at:'',updated_at:'',updated_by:0},beta:{revision:3,draft:{name:'Beta desired',memory_mib:4096,config:{...config,motd:'Beta future'}},stale:false,base_updated_at:'2026-10-17T10:00:00Z',updated_at:'2026-10-17T11:00:00Z',updated_by:1}};
 await page.route('**/api/v1/**',async route=>{
  const request=route.request(),path=new URL(request.url()).pathname.replace('/api/v1',''),method=request.method();const json=body=>route.fulfill({json:body});
  if(path==='/auth/status')return json({needs_setup:false,local_login_enabled:true,oidc_enabled:false});
  if(path==='/auth/me')return json({user:{id:account,username:'fixture',display_name:'Fixture',role,local:true,disabled:false},csrf_token:'fixture-csrf'});
  if(path==='/auth/logout')return route.fulfill({status:204});
  if(path==='/jobs')return json({jobs:[]});
  if(path==='/instances')return json({instances:Object.values(details)});
  if(method!=='GET')writes.push({path,method,input:request.postDataJSON(),csrf:request.headers()['x-csrf-token']});
  if(/^\/instances\/(alpha|beta)$/.test(path))return json({detail:details[path.split('/')[2]]});
  if(path.endsWith('/lifecycle'))return json({installed:true,process:'running',process_running:true,readiness:'ready',autostart_desired:false,autostart_applied:false});
  if(path.endsWith('/players'))return json({snapshot:{known:false,players:[],reason:'Fixture'}});
  if(path.endsWith('/metrics'))return json({points:[]});
  if(path.endsWith('/pending-settings')){
   const id=path.split('/')[2];
   if(method==='GET'){
    const captured=structuredClone(pending[id]);
    if(holdRead){holdRead=false;heldRead=true;await new Promise(resolve=>releaseRead=resolve);}
    if(readFail)return route.fulfill({status:503,json:{error:{code:'unavailable',message:'Fixture read unavailable'}}});
    return json(captured);
   }
   const input=request.postDataJSON();
   if(holdSave){holdSave=false;heldSave=true;await new Promise(resolve=>releaseSave=resolve);}
   if(fail)return route.fulfill({status:503,json:{error:{code:'unavailable',message:'Fixture save failed'}}});
   if(conflict){conflict=false;pending[id].revision++;pending[id].stale=true;return route.fulfill({status:409,json:{error:{code:'conflict',message:'Fixture draft changed'}}});}
   assert.equal(input.expected_revision,pending[id].revision);
   if(method==='DELETE'){pending[id]={...pending[id],revision:pending[id].revision+1,draft:null};return json(pending[id]);}
   assert.equal(input.expected_instance_updated_at,details[id].instance.updated_at);
   pending[id]={revision:pending[id].revision+1,draft:{name:input.name,memory_mib:input.memory_mib,config:input.config},stale:false,base_updated_at:input.expected_instance_updated_at,updated_at:'2026-10-17T12:00:00Z',updated_by:account};return json(pending[id]);
  }
  throw Error('Unexpected '+method+' '+path);
 });
 const button=name=>page.getByRole('button',{name,exact:true});const field=name=>page.getByRole('textbox',{name:new RegExp('^'+name)});
 const navigate=async id=>{await page.evaluate(id=>{history.pushState(null,'','/instances/'+id);dispatchEvent(new PopStateEvent('popstate'));},id);};
 await page.goto(base+'/instances/alpha');await button('Prepare desired settings').click();
 assert.equal(await page.getByRole('dialog').getByRole('textbox',{name:/^Game port/}).isDisabled(),true);
 assert.equal(await page.getByRole('dialog').getByText('Minecraft EULA',{exact:true}).count(),0);
 await field('Server message').fill('My future message');await field('Server name').fill('Alpha desired');
 await page.getByLabel('Verify players with Minecraft online authentication',{exact:true}).uncheck();
 await page.getByRole('button',{name:/^(Save desired settings|Re-save against current applied settings)$/}).click();await page.getByRole('status').filter({hasText:'They have not been applied'}).waitFor();
 assert.equal(writes.length,1);assert.equal(writes[0].path,'/instances/alpha/pending-settings');assert.equal(writes[0].csrf,'fixture-csrf');assert.equal(writes[0].input.config.online_mode,false);assert.deepEqual(writes[0].input.config.extra,{'custom-property':'preserved'});assert.equal(details.alpha.config.motd,'Applied message');
 assert.equal(await button('Edit applied settings (stopped)').isDisabled(),true);
 await page.getByText('My future message',{exact:true}).waitFor();
 await page.getByRole('dialog').waitFor({state:'hidden'});await page.waitForTimeout(250);
 await page.screenshot({path:shots+'/pending-mobile.png',fullPage:true});
 await button('Review / edit saved draft').click();assert.equal(await field('Server message').inputValue(),'My future message');await field('Server message').fill('Keep after conflict');conflict=true;await page.getByRole('button',{name:/^(Save desired settings|Re-save against current applied settings)$/}).click();await page.getByRole('alert').filter({hasText:'Your inputs are preserved'}).waitFor();assert.equal(await field('Server message').inputValue(),'Keep after conflict');
 await button('Reload current base, keep my edits').click();await page.getByRole('button',{name:/^(Save desired settings|Re-save against current applied settings)$/}).waitFor();assert.equal(await field('Server message').inputValue(),'Keep after conflict');
 fail=true;await page.getByRole('button',{name:/^(Save desired settings|Re-save against current applied settings)$/}).click();await page.getByRole('alert').filter({hasText:'Fixture save failed'}).waitFor();
 // Polling does not erase mutation feedback or inputs.
 await page.waitForTimeout(10200);assert.equal(await field('Server message').inputValue(),'Keep after conflict');await page.getByRole('alert').filter({hasText:'Fixture save failed'}).waitFor();
 fail=false;await page.getByRole('button',{name:/^(Save desired settings|Re-save against current applied settings)$/}).click();await page.getByRole('status').filter({hasText:'They have not been applied'}).waitFor();assert.equal(writes.at(-1).input.expected_revision,2);
 // Stale draft requires an explicit re-save and uses current active timestamp.
 pending.alpha.stale=true;details.alpha.instance.updated_at='2026-10-17T13:00:00Z';await button('Refresh saved settings').click();await page.getByRole('alert').filter({hasText:'Its applied base changed'}).waitFor();await button('Review / edit saved draft').click();await button('Re-save against current applied settings').click();await page.getByRole('status').filter({hasText:'They have not been applied'}).waitFor();assert.equal(writes.at(-1).input.expected_instance_updated_at,'2026-10-17T13:00:00Z');
 await button('Discard saved draft').click();assert(pending.alpha.draft);await button('Keep draft').click();assert(pending.alpha.draft);await button('Discard saved draft').click();await button('Confirm discard draft').click();await page.getByRole('status').filter({hasText:'discarded'}).waitFor();assert.equal(writes.at(-1).method,'DELETE');assert.equal(writes.at(-1).input.expected_revision,4);
 // A consumed draft refreshes the active record and reports no invented game success.
 await button('Prepare desired settings').click();await field('Server message').fill('New applied message');await page.getByRole('button',{name:/^(Save desired settings|Re-save against current applied settings)$/}).click();await page.getByRole('status').filter({hasText:'They have not been applied'}).waitFor();details.alpha.config={...pending.alpha.draft.config};pending.alpha={...pending.alpha,revision:7,draft:null};await button('Refresh saved settings').click();await button('Prepare desired settings').waitFor();await page.getByText('New applied message',{exact:true}).waitFor();assert.equal(await page.getByRole('status').filter({hasText:'They have not been applied'}).count(),0);
 holdRead=true;await button('Refresh saved settings').click();for(let i=0;i<100&&!heldRead;i++)await page.waitForTimeout(10);assert(heldRead);await navigate('beta');await page.getByText('Beta desired',{exact:true}).waitFor();releaseRead();await page.waitForTimeout(100);assert.equal(await page.getByText('Alpha desired',{exact:true}).count(),0);
 await button('Review / edit saved draft').click();await field('Server name').fill('Beta edited');holdSave=true;await page.getByRole('button',{name:/^(Save desired settings|Re-save against current applied settings)$/}).click();for(let i=0;i<100&&!heldSave;i++)await page.waitForTimeout(10);assert(heldSave);await navigate('alpha');await button('Prepare desired settings').waitFor();releaseSave();await page.waitForTimeout(100);assert.equal(await page.getByRole('status').filter({hasText:'They have not been applied'}).count(),0);
 await button('Prepare desired settings').click();await field('Server name').focus();await page.keyboard.press('Tab');assert(await page.getByRole('dialog').getByRole('textbox',{name:/^Memory/}).evaluate(el=>el===document.activeElement));assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));await page.waitForTimeout(300);await page.screenshot({path:shots+'/editor-mobile.png',fullPage:false});await button('Cancel').click();
 readFail=true;await button('Refresh saved settings').click();await page.getByRole('alert').filter({hasText:'Fixture read unavailable'}).waitFor();assert(await button('Prepare desired settings').isDisabled());readFail=false;await button('Refresh saved settings').click();
 for(const nextRole of ['operator','viewer']){role=nextRole;account++;await page.reload();await button('Refresh saved settings').waitFor();assert.equal(await button('Prepare desired settings').count(),0);assert.equal(await button('Edit applied settings (stopped)').count(),0);await navigate('beta');await page.getByText('Beta edited',{exact:true}).waitFor();assert.equal(await button('Review / edit saved draft').count(),0);assert.equal(await button('Discard saved draft').count(),0);}
 await page.screenshot({path:shots+'/viewer-mobile.png',fullPage:true});
 // Account replacement with the same role cancels an old read, even at the same instance.
 role='admin';account++;await page.reload();await button('Review / edit saved draft').waitFor();
 await page.setViewportSize({width:1440,height:1000});
 heldRead=false;holdRead=true;await button('Refresh saved settings').click();for(let i=0;i<100&&!heldRead;i++)await page.waitForTimeout(10);assert(heldRead);
 pending.beta.draft.name='Fresh account draft';pending.beta.revision++;account++;
 await button('Sign out').click();await page.getByText('Fresh account draft',{exact:true}).waitFor();releaseRead();await page.waitForTimeout(100);assert.equal(await page.getByText('Beta edited',{exact:true}).count(),0);
 // A role change during a save cannot restore admin actions or old success feedback.
 await button('Review / edit saved draft').click();await field('Server name').fill('Revoked in flight');holdSave=true;heldSave=false;await page.getByRole('button',{name:/^(Save desired settings|Re-save against current applied settings)$/}).click();
 for(let i=0;i<100&&!heldSave;i++)await page.waitForTimeout(10);assert(heldSave);
 role='viewer';account++;/* Harness triggers existing logout handler to simulate account replacement while the modal owns focus. */await button('Sign out').evaluate(el=>el.click());await page.getByText('Fresh account draft',{exact:true}).waitFor();releaseSave();await page.waitForTimeout(100);assert.equal(await button('Review / edit saved draft').count(),0);assert.equal(await page.getByRole('status').filter({hasText:'They have not been applied'}).count(),0);
 assert.deepEqual(errors,[]);
 assert(writes.every(write=>write.path.endsWith('/pending-settings')));
 console.log('PASS: running draft save via CSRF, false/unknown properties, active separation, pending/direct edit guard, failure and 409 preserved edits, explicit stale re-save, discard confirmation/CAS, consumed draft active refresh, instance/account/role delayed reads/saves, viewer/operator, read failure, 390px and keyboard. Intercepted fixtures only.');
 await context.close();
}finally{await browser.close();}
