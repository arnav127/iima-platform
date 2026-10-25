import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ApiError, api, asset, createStore, loginURL, setBase } from './index.ts';

test('asset and loginURL use the base path', () => {
  setBase('/auxesiscapital/');
  assert.equal(asset('logo.png'), '/auxesiscapital/logo.png');
  assert.equal(loginURL('/portfolio?x=1'), '/auth/login?rd=%2Fauxesiscapital%2Fportfolio%3Fx%3D1');
  assert.equal(loginURL('/', true), '/auth/login?rd=%2Fauxesiscapital%2F&switch=1');
});

test('api sends JSON under the base and surfaces server messages', async () => {
  setBase('/app');
  const calls: { url: string; init: RequestInit }[] = [];
  globalThis.fetch = (async (url: string, init: RequestInit) => {
    calls.push({ url, init });
    if (url.endsWith('/fail')) return new Response(JSON.stringify({ status: 403, message: 'Not on the list', data: {} }), { status: 403 });
    return new Response(JSON.stringify({ ok: true }), { status: 200 });
  }) as typeof fetch;

  assert.deepEqual(await api('/api/x', { body: { a: 1 } }), { ok: true });
  assert.equal(calls[0].url, '/app/api/x');
  assert.equal(calls[0].init.method, 'POST');
  assert.equal(calls[0].init.body, '{"a":1}');
  assert.equal(calls[0].init.credentials, 'same-origin');

  await assert.rejects(api('/fail'), (e: unknown) => e instanceof ApiError && e.status === 403 && e.message === 'Not on the list');

  globalThis.fetch = (async () => { throw new TypeError('network'); }) as typeof fetch;
  await assert.rejects(api('/x'), (e: unknown) => e instanceof ApiError && e.status === 0);
});

test('createStore notifies subscribers until they unsubscribe', () => {
  const s = createStore(1);
  let n = 0;
  const off = s.subscribe(() => n++);
  s.set(2);
  off();
  s.set(3);
  assert.equal(n, 1);
  assert.equal(s.get(), 3);
});
