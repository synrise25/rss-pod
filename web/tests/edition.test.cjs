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
    copy: { relativeDates: ['Today', 'Yesterday', 'Earlier'], dateLocale: 'en', untitled: 'Untitled' },
    state: { activeDate: "2026-09-26" }, isAdminPage: false,
    fetch: async url => {
      requests.push(url);
      return { ok: true, json: async () => url.endsWith('/sources')
        ? { sources: [], timezone: 'Asia/Shanghai' } : { episodes: [] } };
    },
  });
  for (const name of ['fetchPlayerData', 'normalizeEpisode', 'createDateOptions', 'dateKey', 'startOfDay', 'addDays', 'parseDate']) {
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

test('calendar and query boundaries use project timezone', async () => {
  const { context: c, requests } = fixture();
  await c.fetchPlayerData();
  assert.equal(c.state.dateOptions[0].key, '2026-09-27');
  assert.equal(c.state.activeDate, '2026-09-27');
  assert.equal(c.state.dateOptions[1].key, '2026-09-26');
  const params = new URL(requests[1], 'https://example.com').searchParams;
  assert.equal(params.get('since'), '2026-09-25');
  assert.equal(params.get('before'), '2026-09-28');
});
