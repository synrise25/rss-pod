const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');

const source = readFileSync(require('node:path').join(__dirname, '../app.js'), 'utf8');
function fixture() {
  const created = [];
  class Audio extends EventTarget {
    constructor() {
      super();
      this.paused = true;
      this.duration = 30;
      this.buffered = { length: 1, start: () => 0, end: () => 30 };
      this.loads = 0;
      created.push(this);
    }
    load() { this.loads++; }
    pause() { this.paused = true; }
    removeAttribute(name) { delete this[name]; }
    replaceWith(audio) { this.replacedBy = audio; }
  }
  const audio = new Audio();
  audio.paused = false;
  audio.id = 'audio';
  const queue = ['a', 'b', 'c'].map(id => ({ id, audioURL: `https://media.example/${id}.mp3` }));
  const context = vm.createContext({
    Audio, AbortController, nextAudio: null, audioEvents: null,
    isAdminPage: false, adminCSRF: '',
    state: { currentEpisodeID: 'a', speed: 1.5 },
    elements: { audio, nowPlayingTitle: {}, nowPlayingSource: {}, playToggle: {}, progress: {} },
    document: {}, visibleEpisodes: () => queue, sourceName: () => 'Source',
    clearMediaSessionPosition() {}, applyPlaybackRate() {}, updateMediaSession() {},
    renderEpisodeList() {}, scrollCurrentEpisodeIntoView() {}, safePlay() {},
    renderPlaybackState() {}, moveInQueue() {}, updateProgress() {},
    updateMediaSessionPosition() {}, persistResumeState() {},
  });
  for (const name of ['selectEpisode', 'bindAudioEvents', 'releaseAudio', 'clearNextAudio', 'updateNextAudio', 'isAudioFullyBuffered']) {
    const start = source.indexOf(`function ${name}(`);
    const rest = source.slice(start);
    const end = rest.indexOf('\n}\n') + 3;
    vm.runInContext(rest.slice(0, end), context);
  }
  return { context, audio, queue, created };
}

test('waits for a full continuous buffer and active playback', () => {
  const { context: c, audio, created } = fixture();
  audio.buffered.end = () => 20;
  c.updateNextAudio();
  assert.equal(created.length, 1);
  audio.buffered.end = () => 30;
  audio.buffered.start = () => 10;
  c.updateNextAudio();
  assert.equal(created.length, 1);
  audio.buffered.start = () => 0;
  audio.paused = true;
  c.updateNextAudio();
  assert.equal(created.length, 1);
  audio.paused = false;
  c.updateNextAudio();
  assert.equal(c.nextAudio.episodeID, 'b');
  assert.equal(c.nextAudio.audio.preload, 'auto');
  c.updateNextAudio();
  assert.equal(created.length, 2);
});

test('filter changes cancel outdated downloads and select the actual next episode', () => {
  const { context: c, queue } = fixture();
  c.updateNextAudio();
  const previous = c.nextAudio.audio;
  queue.splice(1, 1);
  c.updateNextAudio();
  assert.equal(previous.src, undefined);
  assert.equal(previous.loads, 2);
  assert.equal(c.nextAudio.episodeID, 'c');
  queue.splice(0, 1);
  c.updateNextAudio();
  assert.equal(c.nextAudio, null);
});

test('next playback reuses the exact buffered element without calling load again', () => {
  const { context: c, audio, queue } = fixture();
  c.updateNextAudio();
  const cached = c.nextAudio.audio;
  c.selectEpisode(queue[1], { autoplay: true });
  assert.equal(c.elements.audio, cached);
  assert.equal(cached.loads, 1);
  assert.equal(audio.src, undefined);
  assert.equal(c.nextAudio, null);
  assert.equal(cached.id, 'audio');
  assert.equal(cached.defaultPlaybackRate, 1.5);
});

test('jumping to another episode cancels preloading', () => {
  const { context: c, audio, queue } = fixture();
  c.updateNextAudio();
  const cached = c.nextAudio.audio;
  c.selectEpisode(queue[2], { autoplay: true });
  assert.equal(c.elements.audio, audio);
  assert.equal(audio.src, queue[2].audioURL);
  assert.equal(cached.src, undefined);
  assert.equal(c.nextAudio, null);
});

test('failed preload is not retried repeatedly and falls back to ordinary playback', () => {
  const { context: c, audio, queue, created } = fixture();
  c.updateNextAudio();
  c.nextAudio.audio.error = { code: 2 };
  c.updateNextAudio();
  assert.equal(created.length, 2);
  c.selectEpisode(queue[1], { autoplay: true });
  assert.equal(c.elements.audio, audio);
  assert.equal(audio.src, queue[1].audioURL);
});

test('end of queue and logged-out admin do not prefetch', () => {
  const { context: c } = fixture();
  c.state.currentEpisodeID = 'c';
  c.updateNextAudio();
  assert.equal(c.nextAudio, null);
  c.state.currentEpisodeID = 'a';
  c.isAdminPage = true;
  c.updateNextAudio();
  assert.equal(c.nextAudio, null);
});

test('buffer events trigger preloading and waiting releases unfinished downloads', () => {
  const { context: c, audio } = fixture();
  c.bindAudioEvents();
  audio.dispatchEvent(new Event('progress'));
  assert.equal(c.nextAudio.episodeID, 'b');
  c.nextAudio.audio.buffered.end = () => 10;
  audio.dispatchEvent(new Event('waiting'));
  assert.equal(c.nextAudio, null);
});

test('discarded audio events cannot advance the active queue', () => {
  const { context: c, audio, queue } = fixture();
  let advances = 0;
  c.moveInQueue = () => advances++;
  c.bindAudioEvents();
  c.updateNextAudio();
  c.selectEpisode(queue[1]);
  audio.dispatchEvent(new Event('ended'));
  assert.equal(advances, 0);
  c.elements.audio.dispatchEvent(new Event('ended'));
  assert.equal(advances, 1);
});

test('seeking or waiting keeps a fully cached next episode', () => {
  const { context: c, audio } = fixture();
  c.bindAudioEvents();
  c.updateNextAudio();
  const cached = c.nextAudio;
  audio.dispatchEvent(new Event('waiting'));
  assert.equal(c.nextAudio, cached);
});
