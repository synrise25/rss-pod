const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');

const source = readFileSync(require('node:path').join(__dirname, '../theme.js'), 'utf8');
const storageKey = 'rss-pod.appearance';

function fixture({ stored = null, dark = false, denied = false, legacy = false, path = '/zh-cn' } = {}) {
  const storage = new Map(stored === null ? [] : [[storageKey, stored]]);
  let document;
  class Element {
    constructor() { this.listeners = {}; this.dataset = {}; this.attributes = {}; this.style = {}; }
    addEventListener(type, handler) { (this.listeners[type] ||= []).push(handler); }
    dispatch(type, event = {}) {
      event.preventDefault ||= () => { event.prevented = true; };
      for (const handler of this.listeners[type] || []) handler(event);
      return event;
    }
    setAttribute(name, value) { this.attributes[name] = value; }
    removeAttribute(name) { delete this.attributes[name]; }
    focus() { document.activeElement = this; }
  }
  const inputs = ['style', 'mode'].flatMap(kind =>
    (kind === 'style' ? ['silver', 'crystal', 'glacier'] : ['day', 'night', 'auto']).map(value =>
      Object.assign(new Element(), { name: `theme-${kind}`, value, checked: false })));
  const button = new Element();
  const close = new Element();
  const dialog = Object.assign(new Element(), { hidden: true, querySelectorAll: () => inputs });
  const panel = Object.assign(new Element(), {
    querySelector: () => inputs.find(input => input.checked),
    querySelectorAll: () => [close, ...inputs.filter(input => input.checked)],
  });
  const main = new Element();
  const themeColor = new Element();
  const colorScheme = new Element();
  const translated = Object.assign(new Element(), { dataset: { themeCopy: 'auto' } });
  document = Object.assign(new Element(), {
    documentElement: new Element(), hidden: false,
    querySelector: selector => ({
      '#theme-button': button, '#theme-dialog': dialog, '.theme-panel': panel,
      '.app-shell': main, '#theme-close': close,
      'meta[name="theme-color"]': themeColor, 'meta[name="color-scheme"]': colorScheme,
    })[selector],
    querySelectorAll: () => [translated],
  });
  const media = { matches: dark };
  media[legacy ? 'addListener' : 'addEventListener'] = (...args) => { media.onChange = args.at(-1); };
  const window = Object.assign(new Element(), {
    location: { pathname: path }, matchMedia: () => media,
  });
  vm.runInNewContext(source, {
    document, window,
    localStorage: {
      getItem(key) { if (denied) throw new Error('Storage unavailable'); return storage.get(key) ?? null; },
      setItem(key, value) { if (denied) throw new Error('Storage unavailable'); storage.set(key, value); },
    },
  });
  const bootstrap = { ...document.documentElement.dataset };
  document.dispatch('DOMContentLoaded');
  return {
    root: document.documentElement, bootstrap, storage, document, window, media,
    button, close, dialog, main, inputs, translated, themeColor, colorScheme,
    change(kind, value) { dialog.dispatch('change', { target: inputs.find(input => input.name === `theme-${kind}` && input.value === value) }); },
    setSystemDark(value) { media.matches = value; media.onChange(); },
  };
}

test('first visit is Silver day even on a dark system, before styles load', () => {
  const f = fixture({ dark: true });
  assert.deepEqual(f.bootstrap, { style: 'silver', appearance: 'day' });
  assert.equal(f.root.style.colorScheme, 'light');
  assert.equal(f.colorScheme.attributes.content, 'light');
  assert.equal(f.storage.size, 0);
  f.setSystemDark(false);
  f.setSystemDark(true);
  assert.equal(f.root.dataset.appearance, 'day');
});

test('style and mode are independent, persist and restore across language routes', () => {
  const f = fixture();
  f.change('style', 'crystal');
  f.change('mode', 'night');
  assert.equal(f.root.dataset.style, 'crystal');
  assert.equal(f.root.dataset.appearance, 'night');
  const saved = f.storage.get(storageKey);
  assert.deepEqual(JSON.parse(saved), { style: 'crystal', mode: 'night' });
  const reopened = fixture({ stored: saved, path: '/en' });
  assert.deepEqual(reopened.bootstrap, { style: 'crystal', appearance: 'night' });
  assert.equal(reopened.themeColor.attributes.content, '#1c1917');
  assert.equal(reopened.translated.textContent, 'System');
});

test('System follows OS changes immediately and retains the chosen style', () => {
  const f = fixture({ dark: true });
  f.change('style', 'glacier');
  f.change('mode', 'auto');
  assert.equal(f.root.dataset.appearance, 'night');
  f.setSystemDark(false);
  assert.equal(f.root.dataset.appearance, 'day');
  assert.equal(f.root.dataset.style, 'glacier');
  f.setSystemDark(true);
  assert.equal(f.root.dataset.appearance, 'night');
  assert.equal(JSON.parse(f.storage.get(storageKey)).mode, 'auto');
  assert.equal(f.inputs.find(input => input.value === 'auto').checked, true);
});

test('manual night and day override system preference, including legacy browsers', () => {
  const f = fixture({ legacy: true });
  f.change('mode', 'night');
  f.setSystemDark(false);
  assert.equal(f.root.dataset.appearance, 'night');
  f.change('mode', 'day');
  f.setSystemDark(true);
  assert.equal(f.root.dataset.appearance, 'day');
  f.change('mode', 'auto');
  f.setSystemDark(false);
  assert.equal(f.root.dataset.appearance, 'day');
});

test('invalid or corrupted settings safely fall back to Silver day', () => {
  for (const stored of ['{broken', 'null', '42', '"night"', '{"style":"unknown","mode":"system"}']) {
    const f = fixture({ stored, dark: true });
    assert.deepEqual(f.bootstrap, { style: 'silver', appearance: 'day' });
  }
});

test('storage-denied browsers still apply changes and follow the system', () => {
  const f = fixture({ denied: true });
  f.change('style', 'crystal');
  f.change('mode', 'auto');
  f.setSystemDark(true);
  assert.equal(f.root.dataset.style, 'crystal');
  assert.equal(f.root.dataset.appearance, 'night');
  assert.equal(f.storage.size, 0);
});

test('other tabs synchronize settings, including clearing saved preferences', () => {
  const f = fixture();
  f.storage.set(storageKey, JSON.stringify({ style: 'glacier', mode: 'night' }));
  f.window.dispatch('storage', { key: storageKey });
  assert.equal(f.root.dataset.style, 'glacier');
  assert.equal(f.root.dataset.appearance, 'night');
  f.storage.clear();
  f.window.dispatch('storage', { key: null });
  assert.deepEqual({ ...f.root.dataset }, { style: 'silver', appearance: 'day' });
});

test('returning to a suspended page refreshes System appearance', () => {
  const f = fixture({ stored: JSON.stringify({ style: 'silver', mode: 'auto' }) });
  f.media.matches = true;
  f.document.dispatch('visibilitychange');
  assert.equal(f.root.dataset.appearance, 'night');
});

test('settings support Escape, trap keyboard focus and restore focus to the trigger', () => {
  const f = fixture();
  f.button.dispatch('click');
  assert.equal(f.dialog.hidden, false);
  assert.equal(f.main.inert, true);
  assert.equal(f.document.activeElement.value, 'silver');
  f.inputs.find(input => input.value === 'day').focus();
  assert.equal(f.dialog.dispatch('keydown', { key: 'Tab' }).prevented, true);
  assert.equal(f.document.activeElement, f.close);
  f.dialog.dispatch('keydown', { key: 'Tab', shiftKey: true });
  assert.equal(f.document.activeElement.value, 'day');
  f.dialog.dispatch('keydown', { key: 'Escape' });
  assert.equal(f.dialog.hidden, true);
  assert.equal(f.main.inert, false);
  assert.equal(f.button.attributes['aria-expanded'], 'false');
  assert.equal(f.document.activeElement, f.button);
});
