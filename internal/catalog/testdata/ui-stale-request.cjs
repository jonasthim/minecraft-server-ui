// Deterministic browser regression: serves the built app locally, supplies API
// fixtures and delays fetch completion even when its AbortSignal is canceled.
// Run npm --prefix web run build first, then node this file from repository root.
const assert=require('node:assert/strict');
const fs=require('node:fs');const path=require('node:path');const http=require('node:http');
const{chromium}=require(path.resolve('web/node_modules/playwright'));
(async()=>{
 const dist=path.resolve('web/dist');
 const server=http.createServer((req,res)=>{const filename=req.url.startsWith('/assets/')?path.join(dist,new URL(req.url,'http://localhost').pathname):path.join(dist,'index.html');if(!filename.startsWith(dist+path.sep)){res.writeHead(404);return res.end()};fs.readFile(filename,(err,bytes)=>{if(err){res.writeHead(404);return res.end()};res.setHeader('Content-Type',filename.endsWith('.js')?'application/javascript':filename.endsWith('.css')?'text/css':'text/html');res.end(bytes)})});
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const browser=await chromium.launch();
 try{
 const context=await browser.newContext({viewport:{width:390,height:844}});
 const compatibility={minecraft:['1.21.1'],loaders:['fabric'],environment:'server_only',server_side:'supported',evidence:'Publisher fixture metadata',tested:false};
 const project={source:'modrinth',fetched_at:'2026-10-16T12:00:00Z',id:'old123',slug:'old-mod',kind:'mod',title:'Old mod',description:'Delayed fixture',official_url:'https://modrinth.com/mod/old123',compatibility};
 const release={source:'modrinth',fetched_at:'2026-10-16T12:00:00Z',id:'v123',upstream_id:'v123',project_id:'old123',name:'Old release',number:'1.0',published_at:'2026-10-16T12:00:00Z',official_url:'https://modrinth.com/mod/old123/version/v123',compatibility,files:[],dependencies:[],server_pack_file_id:'',is_server_pack:false};
 const search={source:'modrinth',fetched_at:'2026-10-16T12:00:00Z',projects:[project],offset:0,limit:20,total:1};
 const versions={source:'modrinth',fetched_at:'2026-10-16T12:00:00Z',versions:[release],offset:0,limit:20,total:1};
 await context.route('**/api/v1/**',async route=>{const url=new URL(route.request().url());let data;if(url.pathname.endsWith('/auth/status'))data={needs_setup:false,local_login_enabled:true,oidc_enabled:false,oidc_provider_name:''};else if(url.pathname.endsWith('/auth/me'))data={user:{id:1,username:'viewer',display_name:'Viewer',email:'',role:'viewer',disabled:false},csrf_token:'fixture-csrf'};else if(url.pathname.endsWith('/catalog/providers'))data={providers:[{provider:'modrinth',enabled:true,reason:''},{provider:'curseforge',enabled:false,reason:'Private key required'},{provider:'hangar',enabled:true,reason:''}]};else if(url.pathname.endsWith('/search'))data=search;else if(url.pathname.endsWith('/versions'))data=versions;else throw Error('Unexpected fixture API '+url.pathname);await route.fulfill({json:data})});
 await context.addInitScript(({search,versions})=>{const realFetch=window.fetch.bind(window);window.fetch=async(...args)=>{const url=String(args[0]);const phase=url.includes('/search?')?'search':url.includes('/versions?')?'versions':'';if(phase&&window.__catalogHold===phase){window.__catalogPending=true;await new Promise(resolve=>{window.__catalogRelease=resolve});window.__catalogSettled=true;return new Response(JSON.stringify(phase==='search'?search:versions),{status:200,headers:{'Content-Type':'application/json'}})}return realFetch(...args)}},{search,versions});
 const page=await context.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));const base='http://127.0.0.1:'+server.address().port;
 for(const phase of ['search','versions']){
  await page.goto(base+'/catalog');await page.getByRole('heading',{name:'Content catalogs',exact:true}).waitFor();await page.getByRole('button',{name:'Search catalog',exact:true}).waitFor();
  if(phase==='search'){await page.evaluate(()=>{window.__catalogHold='search'});await page.getByRole('button',{name:'Search catalog',exact:true}).click()}
  else{await page.getByRole('button',{name:'Search catalog',exact:true}).click();await page.getByRole('button',{name:'Browse Old mod releases',exact:true}).waitFor();await page.evaluate(()=>{window.__catalogHold='versions'});await page.getByRole('button',{name:'Browse Old mod releases',exact:true}).click()}
  await page.waitForFunction(()=>window.__catalogPending===true);
  await page.getByRole('combobox',{name:'Content type',exact:true}).click();await page.getByRole('option',{name:'Plugins',exact:true}).click();
  await page.evaluate(()=>window.__catalogRelease());await page.waitForFunction(()=>window.__catalogSettled===true);await page.evaluate(async()=>{await new Promise(requestAnimationFrame);await new Promise(requestAnimationFrame)});
  assert.equal(await page.getByRole('heading',{name:'Old mod',exact:true}).count(),0,phase+' restored stale project after kind change');
  assert.equal(await page.getByRole('button',{name:'Inspect Old release',exact:true}).count(),0,phase+' restored stale release after kind change');
  assert.equal(await page.getByLabel('Loading publisher metadata',{exact:true}).count(),0,phase+' left stale busy indicator');
  assert.equal(await page.getByRole('button',{name:'Search catalog',exact:true}).isEnabled(),true);
 }
 assert.deepEqual(errors,[]);console.log('PASS: delayed search and release responses cannot restore old content or busy state after kind changes at390px.');await context.close();
 }finally{await browser.close();server.closeAllConnections();await new Promise(resolve=>server.close(resolve))}
})().catch(error=>{console.error(error);process.exitCode=1});
