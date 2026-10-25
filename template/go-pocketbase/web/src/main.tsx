import { render } from 'preact';
import { useEffect, useState } from 'preact/hooks';
import { api, link, login, logout, routeStore, useStore, type Me } from './lib.ts';

function Home({ me }: { me: Me }) {
  if (!me.signedIn) {
    return (
      <>
        <p>You are signed out.</p>
        <button onClick={() => login('/')}>Sign in</button>
      </>
    );
  }
  return (
    <>
      <p>
        Signed in as <b>{me.name || me.email}</b>
        {me.programme && <> · {me.programme}{me.year ? ` year ${me.year}` : ''}</>}
      </p>
      <p>Roles here: {me.roles?.length ? me.roles.join(', ') : 'none'}</p>
      <p><a {...link('/about')}>About</a> · <button onClick={() => logout('/')}>Sign out</button></p>
    </>
  );
}

function App() {
  const route = useStore(routeStore);
  const [me, setMe] = useState<Me | null>(null);
  const [error, setError] = useState('');
  useEffect(() => { api<Me>('/api/myapp/me').then(setMe).catch((e) => setError(e.message)); }, []);

  return (
    <main style={{ maxWidth: '40rem', margin: '3rem auto', padding: '0 1rem', font: '16px/1.5 system-ui, sans-serif' }}>
      <h1>My App</h1>
      {error && <p role="alert">{error}</p>}
      {route === '/about' ? <p>An app on the IIMA students platform. <a {...link('/')}>Home</a></p> : me && <Home me={me} />}
    </main>
  );
}

render(<App />, document.getElementById('app')!);
