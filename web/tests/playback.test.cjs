const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');

const source = readFileSync(require('node:path').join(__dirname, '../app.js'), 'utf8');

function fixture() {
  class Element {
    constructor() {
      this.classes = new Set();
      this.attributes = {};
      this.dataset = {};
      this.children = [];
      this.scrollTop = 0;
      this.classList = {
        toggle: (name, enabled) => enabled ? this.classes.add(name) : this.classes.delete(name),
        contains: name => this.classes.has(name),
      };
    }
    setAttribute(name, value) { this.attributes[name] = value; }
    addEventListener() {}
    append(child) { this.children.push(...(child.fragment ? child.children : [child])); }
    replaceChildren() { this.children = []; }
  }
  const episodeList = new Element();
  const audio = new EventTarget();
  audio.paused = true;
  const playToggle = new Element();
  const rowTemplate = {
    content: {
      firstElementChild: {
        cloneNode() {
          const row = new Element();
          const parts = Object.fromEntries(['.episode-play-button', '.episode-source', '.episode-title', '.episode-time']
            .map(selector => [selector, new Element()]));
          row.querySelector = selector => parts[selector] || null;
          return row;
        },
      },
    },
  };
  const episodes = ['a', 'b'].map(id => ({ id, title: `Episode ${id}`, sourceID: 'source' }));
  const context = vm.createContext({
    AbortController,
    elements: { audio, playToggle, episodeList, rowTemplate },
    state: { currentEpisodeID: 'a' },
    isAdminPage: false, audioEvents: null,
    copy: { play: 'Play', pause: 'Pause', playEpisode: title => `Play: ${title}`, pauseEpisode: title => `Pause: ${title}` },
    document: { createDocumentFragment: () => Object.assign(new Element(), { fragment: true }) },
    visibleEpisodes: () => episodes, sourceName: () => 'Source',
    setStatus() {}, renderEpisodeDuration() {}, updateQueueButtons() {},
    updateMediaSessionPlaybackState() {}, updateMediaSessionPosition() {},
    registerMediaSessionActions() {}, scrollCurrentEpisodeIntoView() {},
    updateNextAudio() {}, clearNextAudio() {},
  });
  for (const name of ['renderEpisodeList', 'renderPlaybackState', 'bindAudioEvents']) {
    const rest = source.slice(source.indexOf(`function ${name}(`));
    vm.runInContext(rest.slice(0, rest.indexOf('\n}\n') + 3), context);
  }
  context.bindAudioEvents();
  return { context, audio, playToggle, episodeList };
}

function assertControls(f, currentID, playing) {
  assert.equal(f.playToggle.classList.contains('is-playing'), playing);
  assert.equal(f.playToggle.attributes['aria-label'], playing ? 'Pause' : 'Play');
  for (const row of f.episodeList.children) {
    const active = playing && row.dataset.episodeId === currentID;
    const label = `${active ? 'Pause' : 'Play'}: Episode ${row.dataset.episodeId}`;
    assert.equal(row.classList.contains('is-playing'), active);
    assert.equal(row.attributes['aria-label'], label);
    assert.equal(row.querySelector('.episode-play-button').attributes['aria-label'], label);
  }
}

test('play and pause events keep both controls synchronized through repeated toggles', () => {
  const f = fixture();
  for (const playing of [false, true, false, true, false]) {
    f.audio.paused = !playing;
    f.audio.dispatchEvent(new Event(playing ? 'play' : 'pause'));
    assertControls(f, 'a', playing);
  }
});

test('switching episodes moves the pause symbol to the active row and survives list rerenders', () => {
  const f = fixture();
  f.audio.paused = false;
  f.audio.dispatchEvent(new Event('play'));
  f.context.state.currentEpisodeID = 'b';
  f.context.renderEpisodeList();
  assertControls(f, 'b', true);
  f.context.renderEpisodeList();
  assertControls(f, 'b', true);
  f.audio.paused = true;
  f.audio.dispatchEvent(new Event('pause'));
  assertControls(f, 'b', false);
});
