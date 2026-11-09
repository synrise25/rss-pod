const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');

const source = readFileSync(require('node:path').join(__dirname, '../app.js'), 'utf8');

function fixture() {
  class Audio extends EventTarget {
    constructor() {
      super();
      this.paused = true;
      this.ended = false;
      this.muted = false;
      this.duration = 120;
      this.currentTime = 12;
      this.playbackRate = 1.5;
      this.buffered = { length: 0 };
      this.plays = 0;
    }
    play() {
      this.plays++;
      if (this.playError) return Promise.reject(this.playError);
      if (this.paused) {
        this.paused = false;
        this.dispatchEvent(new Event('play'));
      }
      return this.pendingPlay || Promise.resolve();
    }
    pause() {
      if (this.paused) return;
      this.paused = true;
      this.dispatchEvent(new Event('pause'));
    }
    load() {}
    removeAttribute(name) { delete this[name]; }
    replaceWith() {}
  }
  const audio = new Audio();
  const publications = [];
  const actions = {};
  const session = {
    playbackState: 'none',
    setPositionState(position) {
      publications.push({ playbackState: this.playbackState, ...position });
    },
    setActionHandler(action, handler) { actions[action] = handler; },
  };
  const queue = ['a', 'b'].map(id => ({ id, audioURL: `https://media.example/${id}.mp3` }));
  const document = new EventTarget();
  document.hidden = false;
  const context = vm.createContext({
    Audio, AbortController, document, navigator: { mediaSession: session },
    elements: { audio, playToggle: { classList: { toggle() {} }, setAttribute() {} } },
    state: { currentEpisodeID: 'a', speed: 1.5 },
    copy: { play: 'Play', pause: 'Pause' },
    isAdminPage: false, adminCSRF: '', audioEvents: null, nextAudio: null,
    visibleEpisodes: () => queue, isDemoMode: () => true,
    renderEpisodeList() {}, scrollCurrentEpisodeIntoView() {}, updateGreeting() {},
    updateProgress() {}, persistResumeState() {}, applyPlaybackRate() {}, moveInQueue() {},
  });
  for (const name of [
    'safePlay', 'bindAudioEvents', 'renderPlaybackState', 'syncMediaSessionState',
    'getMediaSession', 'updateMediaSessionPlaybackState', 'updateMediaSessionPosition',
    'registerMediaSessionActions', 'updateNextAudio', 'isAudioFullyBuffered',
    'clearNextAudio', 'releaseAudio',
  ]) {
    const match = source.match(new RegExp(`(?:async )?function ${name}\\(`));
    const rest = source.slice(match.index);
    vm.runInContext(rest.slice(0, rest.indexOf('\n}\n') + 3), context);
  }
  const visibilityListener = source.slice(source.indexOf('document.addEventListener("visibilitychange"'));
  vm.runInContext(visibilityListener.slice(0, visibilityListener.indexOf('\n});') + 4), context);
  context.bindAudioEvents();
  context.registerMediaSessionActions();
  return { context, audio, session, publications, actions, document };
}

function assertSession(f, playbackState, position = f.audio.currentTime) {
  assert.equal(f.session.playbackState, playbackState);
  assert.deepEqual(f.publications.at(-1), {
    playbackState, duration: f.audio.duration, playbackRate: f.audio.playbackRate, position,
  });
}

test('actual playback repairs a state reset after the initial play event', async () => {
  const f = fixture();
  await f.context.safePlay();
  f.session.playbackState = 'paused';
  f.audio.dispatchEvent(new Event('playing'));
  assertSession(f, 'playing');
});

test('progress repairs stale system state before publishing the advancing position', () => {
  const f = fixture();
  f.audio.paused = false;
  f.session.playbackState = 'paused';
  f.audio.currentTime = 35;
  f.audio.dispatchEvent(new Event('timeupdate'));
  assertSession(f, 'playing', 35);
  f.audio.pause();
  assertSession(f, 'paused', 35);
});

test('system play on already playing audio repairs state without restarting it', async () => {
  const f = fixture();
  f.audio.paused = false;
  f.session.playbackState = 'paused';
  await f.actions.play();
  assertSession(f, 'playing', 12);
  assert.equal(f.audio.plays, 1);
  f.actions.pause();
  assert.equal(f.audio.paused, true);
  assertSession(f, 'paused', 12);
  f.session.playbackState = 'playing';
  f.actions.pause();
  assertSession(f, 'paused', 12);
});

test('failed play and a pause during a pending play never report playing', async () => {
  const f = fixture();
  f.audio.playError = new Error('Playback denied');
  f.session.playbackState = 'playing';
  await f.context.safePlay();
  assertSession(f, 'paused');
  f.audio.playError = null;
  let resolvePlay;
  f.audio.pendingPlay = new Promise(resolve => { resolvePlay = resolve; });
  const playing = f.context.safePlay();
  f.audio.pause();
  resolvePlay();
  await playing;
  assertSession(f, 'paused');
});

test('returning to the page repairs both playing and paused system state', () => {
  const f = fixture();
  for (const paused of [false, true]) {
    f.audio.paused = paused;
    f.session.playbackState = paused ? 'playing' : 'paused';
    f.document.dispatchEvent(new Event('visibilitychange'));
    assertSession(f, paused ? 'paused' : 'playing');
  }
});

test('metadata, seeking and speed changes publish the active timeline', () => {
  const f = fixture();
  f.audio.paused = false;
  for (const type of ['loadedmetadata', 'durationchange', 'seeked', 'ratechange']) {
    f.session.playbackState = 'paused';
    f.audio.currentTime += 5;
    f.audio.dispatchEvent(new Event(type));
    assertSession(f, 'playing');
  }
});

test('ending the queue leaves system controls paused at the end of the track', () => {
  const f = fixture();
  f.audio.currentTime = f.audio.duration;
  f.audio.ended = true;
  f.session.playbackState = 'playing';
  f.audio.dispatchEvent(new Event('ended'));
  assertSession(f, 'paused', 120);
});

test('silent preloading does not publish its pause, metadata or progress', () => {
  const f = fixture();
  f.audio.paused = false;
  f.audio.buffered = { length: 1, start: () => 0, end: () => 120 };
  f.audio.dispatchEvent(new Event('progress'));
  const preload = f.context.nextAudio.audio;
  assert.equal(preload.muted, true);
  f.context.syncMediaSessionState();
  const count = f.publications.length;
  for (const type of ['pause', 'loadedmetadata', 'timeupdate']) {
    preload.dispatchEvent(new Event(type));
  }
  assert.equal(f.publications.length, count);
  assertSession(f, 'playing');
});

test('a discarded element finishing play does not update the new media session', async () => {
  const f = fixture();
  let resolvePlay;
  f.audio.pendingPlay = new Promise(resolve => { resolvePlay = resolve; });
  const playing = f.context.safePlay();
  f.context.elements.audio = { paused: true };
  f.session.playbackState = 'paused';
  const count = f.publications.length;
  resolvePlay();
  await playing;
  assert.equal(f.publications.length, count);
  assert.equal(f.session.playbackState, 'paused');
});

test('missing or partial media session APIs do not prevent playback', async () => {
  for (const mediaSession of [undefined, {}, {
    playbackState: 'none',
    setPositionState() { throw new Error('Unsupported'); },
    setActionHandler() { throw new Error('Unsupported'); },
  }]) {
    const f = fixture();
    f.context.navigator.mediaSession = mediaSession;
    await f.context.safePlay();
    f.audio.dispatchEvent(new Event('playing'));
    assert.equal(f.audio.paused, false);
    f.audio.pause();
  }
});
