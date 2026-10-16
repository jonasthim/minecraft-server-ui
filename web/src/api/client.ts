export type FieldError = {field:string;message:string};
export class APIError extends Error { code:string; fields:FieldError[]; constructor(code:string,message:string,fields:FieldError[]=[]){super(message);this.code=code;this.fields=fields} }
let csrfToken='';
export function setCSRFToken(value:string){csrfToken=value}
export async function api<T=unknown>(path:string, options:RequestInit={}):Promise<T>{
 const url=path.startsWith('/api/')?path:'/api/v1'+(path.startsWith('/')?path:'/'+path);
 const method=(options.method??'GET').toUpperCase();
 if(!['GET','HEAD','OPTIONS'].includes(method)&&!csrfToken&&!['/api/v1/auth/login','/api/v1/auth/setup'].includes(url)){
  const me=await fetch('/api/v1/auth/me',{credentials:'same-origin'});if(me.ok){const value=await me.json();csrfToken=value.csrf_token??''}
 }
 const headers=new Headers(options.headers);if(options.body!==undefined&&!headers.has('Content-Type'))headers.set('Content-Type','application/json');
 if(csrfToken)headers.set('X-CSRF-Token',csrfToken);
 const response=await fetch(url,{...options,method,headers,credentials:'same-origin'});
 if(!response.ok){const body=await response.json().catch(()=>({}));throw new APIError(body.error?.code??'request_failed',body.error?.message??`Request failed (${response.status})`,body.error?.fields??[])}
 if(response.status===204)return undefined as T;
 return response.json() as Promise<T>;
}
export const apiRequest=api;
