const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const source = readFileSync(require('node:path').join(__dirname, '../app.js'), 'utf8');

function fixture() {
  const requests = [];
  class Clock extends Date {
    constructor(...args) { super(...(args.length ? args : ['2026-09-26T17:00:00Z'])); }
  }
  const context = vm.createContext({
    Date: Clock, Intl, URLSearchParams, console,
    copy: { relativeDates: ['Today', 'Yesterday', '2 days ago', '3 days ago'], dateLocale: 'en', untitled: 'Untitled' },
    demoContent: { sources: ['Source 1', 'Source 2', 'Source 3'], titles: Array.from({ length: 8 }, (_, i) => `Episode ${i + 1}`) },
    DEMO_AUDIO: '/demo.mp3',
    state: { activeDate: "2026-09-26" }, isAdminPage: false,
    fetch: async url => {
      requests.push(url);
      return { ok: true, json: async () => url.endsWith('/sources')
        ? { sources: [], timezone: 'Asia/Shanghai' } : { episodes: [] } };
    },
  });
  for (const name of ['fetchPlayerData', 'normalizeEpisode', 'createDateOptions', 'dateKey', 'startOfDay', 'addDays', 'parseDate', 'demoPayload', 'demoEpisode']) {
    const start = source.indexOf(`${name === 'fetchPlayerData' ? 'async ' : ''}function ${name}(`);
    const rest = source.slice(start);
    vm.runInContext(rest.slice(0, rest.indexOf('\n}\n') + 3), context);
  }
  return { context, requests };
}

test('cross-midnight publication keeps its edition day', () => {
  const { context: c } = fixture();
  assert.equal(c.normalizeEpisode({ edition_date: '2026-09-26', published_at: '2026-09-27T01:00:00+08:00' }).dayKey, '2026-09-26');
});

test('episodes without an edition day do not fall back to publication time', () => {
  const { context: c } = fixture();
  assert.equal(c.normalizeEpisode({ published_at: '2026-09-27T01:00:00+08:00' }).dayKey, '');
});

test('demo episodes explicitly provide their edition days', () => {
  const { context: c } = fixture();
  c.state.dateOptions = c.createDateOptions();
  const [today, yesterday, dayBefore] = c.state.dateOptions;
  const episodes = c.demoPayload().episodes;
  assert.deepEqual(Array.from(episodes, episode => c.normalizeEpisode(episode).dayKey), [
    today.key, today.key, today.key, today.key, today.key, today.key, yesterday.key, dayBefore.key,
  ]);
});

test('calendar and query boundaries use project timezone', async () => {
  const { context: c, requests } = fixture();
  await c.fetchPlayerData();
  assert.equal(c.state.dateOptions[0].key, '2026-09-27');
  assert.equal(c.state.activeDate, '2026-09-27');
  assert.equal(c.state.dateOptions[1].key, '2026-09-26');
  assert.equal(c.state.dateOptions.length, 4);
  assert.equal(c.state.dateOptions[3].key, '2026-09-24');
  assert.equal(c.state.dateOptions[3].relativeLabel, '3 days ago');
  const params = new URL(requests[1], 'https://example.com').searchParams;
  assert.equal(params.get('since'), '2026-09-24');
  assert.equal(params.get('before'), '2026-09-28');
});
