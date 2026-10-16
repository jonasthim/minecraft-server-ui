// Run only against an explicitly provisioned disposable local manager:
// CATALOG_SMOKE_URL=http://127.0.0.1:18085 node internal/catalog/testdata/ui-smoke.cjs
// Exercises real API metadata and Chromium at390px; creates the initial local admin.
const assert = require('node:assert/strict');
const path = require('node:path');
const { chromium } = require(path.resolve('web/node_modules/playwright'));
(async () => {
 const base=process.env.CATALOG_SMOKE_URL;
 assert(base && /^http:\/\/127\.0\.0\.1:\d+$/.test(base), 'explicit disposable loopback CATALOG_SMOKE_URL required');
 const browser=await chromium.launch({headless:true});
 try {
 const context=await browser.newContext({viewport:{width:390,height:844}});
 const status=await (await context.request.get(base+'/api/v1/auth/status')).json();
 assert.equal(status.needs_setup,true,'requires fresh disposable data directory');
 const password='catalog-smoke-Only-49!';
 const setup=await context.request.post(base+'/api/v1/auth/setup',{data:{username:'catalog-smoke',password,display_name:'Catalog smoke',email:''}});
 assert.equal(setup.status(),201);
 const page=await context.newPage(); const errors=[]; page.on('pageerror',error=>errors.push(error.message));
 await page.goto(base+'/catalog');
 await page.getByRole('heading',{name:'Content catalogs',exact:true}).waitFor();
 await page.getByText('CurseForge unavailable',{exact:true}).waitFor();
 await page.getByRole('heading',{name:'Private CurseForge key',exact:true}).waitFor();
 await page.getByLabel('Search publishers',{exact:true}).fill('fabric api');
 await page.getByLabel('Minecraft version (optional)',{exact:true}).fill('1.21.1');
 const search=page.getByRole('button',{name:'Search catalog',exact:true});
 await search.focus(); await page.keyboard.press('Enter');
 await page.getByRole('button',{name:'Browse Fabric API releases',exact:true}).waitFor({timeout:25000});
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),'390px search horizontal overflow');
 await page.getByRole('button',{name:'Browse Fabric API releases',exact:true}).click();
 await page.getByRole('button',{name:/^Inspect \[1\.21\.1\] Fabric API/}).first().click();
 await page.getByText('Publisher server compatibility: supported. Tested support: unqualified.',{exact:true}).waitFor();
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth),'390px release horizontal overflow');
 assert.equal(await page.getByRole('button',{name:/^Install/}).count(),0);
 assert.equal(await page.getByRole('button',{name:'Next releases',exact:true}).count(),0,'release details should replace the long release list');
 await page.screenshot({path:'/tmp/catalog-mobile-smoke.png',fullPage:true});
 const me=await (await context.request.get(base+'/api/v1/auth/me')).json();
 const create=await context.request.post(base+'/api/v1/users',{headers:{'X-CSRF-Token':me.csrf_token},data:{username:'catalog-viewer',password,display_name:'Viewer',email:'',role:'viewer'}});assert.equal(create.status(),201);
 const viewer=await browser.newContext({viewport:{width:390,height:844}});
 assert.equal((await viewer.request.post(base+'/api/v1/auth/login',{data:{username:'catalog-viewer',password}})).status(),200);
 const vp=await viewer.newPage();await vp.goto(base+'/catalog');await vp.getByRole('heading',{name:'Content catalogs',exact:true}).waitFor();
 assert.equal(await vp.getByLabel('Replace private API key',{exact:true}).count(),0,'viewer sees credential mutation');
 const vm=await (await viewer.request.get(base+'/api/v1/auth/me')).json();
 const denied=await viewer.request.put(base+'/api/v1/catalog/curseforge-key',{headers:{'X-CSRF-Token':vm.csrf_token},data:{key:'never-store-this'}});assert.equal(denied.status(),403);
 assert.equal(errors.length,0,errors.join('\n'));
 console.log('PASS: real Modrinth search/releases,390px no overflow, keyboard submit, CF disabled, viewer UI/API key denial, no installation claims, no browser errors. Screenshot:/tmp/catalog-mobile-smoke.png');
 await context.close();await viewer.close();
 } finally {await browser.close()}
})().catch(error=>{console.error(error);process.exitCode=1});
