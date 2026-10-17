import {createContext,useCallback,useContext,useEffect,useState,type ReactNode} from 'react';
import {Alert,Button,Center,Checkbox,Container,Loader,Paper,PasswordInput,Stack,Text,TextInput,Title} from '@mantine/core';
import {api,APIError,setCSRFToken} from '../../api/client';
import type {APIRequest,APIResponse,User,Status} from './types';
export type {User} from './types';
type AuthContextType={user:User|null;status:Status|null;loading:boolean;error:string;refresh:()=>Promise<void>;logout:()=>Promise<void>};
const AuthContext=createContext<AuthContextType|null>(null);
export function useAuth(){const value=useContext(AuthContext);if(!value)throw Error('AuthProvider required');return value}
export function AuthProvider({children}:{children:ReactNode}){const [user,setUser]=useState<User|null>(null);const [status,setStatus]=useState<Status|null>(null);const [loading,setLoading]=useState(true);const [error,setError]=useState('');
 const refresh=useCallback(async()=>{setLoading(true);setError('');try{setStatus(await api<Status>('/auth/status'));try{const me=await api<APIResponse<'authMe',200>>('/auth/me');setUser(me.user);setCSRFToken(me.csrf_token??'')}catch(e){if(!(e instanceof APIError)||e.code!=='unauthorized')throw e;setUser(null);setCSRFToken('')}}catch(e){setError(e instanceof Error?e.message:'Could not connect')}finally{setLoading(false)}},[]);
 useEffect(()=>{void refresh()},[refresh]);
 async function logout(){await api('/auth/logout',{method:'POST'});setCSRFToken('');await refresh()}
 return <AuthContext.Provider value={{user,status,loading,error,refresh,logout}}>{children}</AuthContext.Provider>
}
export function AuthGate({children}:{children:ReactNode}){const auth=useAuth();if(auth.loading)return <Center h="100vh"><Loader aria-label="Loading account"/></Center>;if(auth.error)return <Container size="xs" py="xl"><Alert color="red">{auth.error}</Alert><Button mt="md" onClick={()=>void auth.refresh()}>Retry connection</Button></Container>;if(auth.user)return <>{children}</>;return <IdentityForm setup={auth.status?.needs_setup??false}/>}
function IdentityForm({setup}:{setup:boolean}){const auth=useAuth();const [username,setUsername]=useState('');const [password,setPassword]=useState('');const [confirm,setConfirm]=useState('');const [displayName,setDisplayName]=useState('');const [email,setEmail]=useState('');const [busy,setBusy]=useState(false);const [error,setError]=useState('');const [showLocal,setShowLocal]=useState(false);
 async function submit(e:React.FormEvent){e.preventDefault();setError('');if(setup&&confirm!==password){setError('Passwords must match');return};setBusy(true);try{await api(setup?'/auth/setup':'/auth/login',{method:'POST',body:JSON.stringify(setup?({username,password,display_name:displayName,email} satisfies APIRequest<'authSetup'>):({username,password} satisfies APIRequest<'authLogin'>))});await auth.refresh()}catch(e){setError(e instanceof Error?e.message:'Sign in failed')}finally{setBusy(false)}}
 const oidcError=new URLSearchParams(window.location.search).has('error');
 return <Container size="xs" py={80}><Text fw={800} size="sm" c="dimmed" mb="xs">Minecraft Server UI</Text><Title order={1} mb="sm">{setup?'Create your administrator':'Welcome back'}</Title><Text c="dimmed" mb="xl">{setup?'Set up the first account to manage your Minecraft servers.':'Sign in to manage your servers.'}</Text><Paper withBorder radius="md" p="xl"><Stack>
 {(error||oidcError)&&<Alert color="red" role="alert">{error||'Single sign-on failed. Try again or contact your administrator.'}</Alert>}
 {!setup&&auth.status?.oidc_enabled&&<Button component="a" href="/api/v1/auth/oidc/login">Continue with {auth.status.oidc_provider_name||'SSO'}</Button>}
 {!setup&&auth.status?.oidc_enabled&&auth.status?.local_login_enabled&&<Checkbox label="Use a local account" checked={showLocal} onChange={e=>setShowLocal(e.currentTarget.checked)}/>}
 {(setup||(auth.status?.local_login_enabled&&(!auth.status?.oidc_enabled||showLocal)))&&<form onSubmit={submit}><Stack><TextInput label="Username" autoComplete="username" required minLength={2} maxLength={32} value={username} onChange={e=>setUsername(e.currentTarget.value)}/>{setup&&<><TextInput label="Display name" value={displayName} onChange={e=>setDisplayName(e.currentTarget.value)}/><TextInput label="Email (optional)" type="email" value={email} onChange={e=>setEmail(e.currentTarget.value)}/></>}<PasswordInput label="Password" autoComplete={setup?'new-password':'current-password'} required minLength={setup?10:undefined} maxLength={128} value={password} onChange={e=>setPassword(e.currentTarget.value)}/>{setup&&<PasswordInput label="Confirm password" autoComplete="new-password" required value={confirm} onChange={e=>setConfirm(e.currentTarget.value)}/>}<Button type="submit" loading={busy}>{setup?'Create administrator':'Sign in'}</Button></Stack></form>}
 </Stack></Paper></Container>
}
