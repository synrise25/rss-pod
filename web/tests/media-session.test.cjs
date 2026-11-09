const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');

const source = readFileSync(require('node:path').join(__dirname, '../app.js'), 'utf8');
const safari = 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_7 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.0.1 Mobile/15E148 Safari/604.1';
const chrome = 'Mozilla/5.0 AppleWebKit/537.36 Chrome/144.0.0.0 Safari/537.36';

function fixture(userAgent = chrome) {
  const audio = new EventTarget();
  Object.assign(audio, { paused: true, ended: false, duration: 120, currentTime: 12, playbackRate: 1.5 });
  audio.play = async () => { audio.paused = false; audio.dispatchEvent(new Event('play')); };
  audio.pause = () => { audio.paused = true; audio.dispatchEvent(new Event('pause')); };
  const actions = {};
  const positions = [];
  const registrations = [];
  const writes = [];
  let playbackState = 'none';
  const session = {
    get playbackState() { return playbackState; },
    set playbackState(value) { writes.push(value); playbackState = value; },
    setPositionState(position) { positions.push(position); },
    setActionHandler(action, handler) { registrations.push(action); actions[action] = handler; },
  };
  const context = vm.createContext({
    AbortController, window: {}, navigator: { mediaSession: session, userAgent },
    elements: { audio, playToggle: { classList: { toggle() {} }, setAttribute() {} } },
    state: { currentEpisodeID: 'a' }, audioEvents: null,
    copy: { play: 'Play', pause: 'Pause', mediaAlbum: 'Podcasts' }, MEDIA_ARTWORK: [], DEFAULT_SEEK_OFFSET: 10,
    sourceName: () => 'Source', isDemoMode: () => true,
    renderEpisodeList() {}, scrollCurrentEpisodeIntoView() {}, applyPlaybackRate() {},
    updateProgress() {}, persistResumeState() {}, updateNextAudio() {}, clearNextAudio() {},
    moveInQueue() {},
  });
  for (const name of [
    'safePlay', 'bindAudioEvents', 'renderPlaybackState', 'getMediaSession',
    'updateMediaSession', 'updateMediaSessionPlaybackState', 'updateMediaSessionPosition',
    'registerMediaSessionActions', 'useNativeMediaSessionControls', 'seekBy', 'seekTo',
  ]) {
    const match = source.match(new RegExp(`(?:async )?function ${name}\\(`));
    const rest = source.slice(match.index);
    vm.runInContext(rest.slice(0, rest.indexOf('\n}\n') + 3), context);
  }
  context.bindAudioEvents();
  context.updateMediaSession({ title: 'Episode', sourceID: 'source', durationSeconds: 120 });
  return { context, audio, session, actions, positions, registrations, writes };
}

test('WebKit retains native transport controls and receives no declared state writes', async () => {
  for (const agent of [safari, safari.replace('Version/27.0.1', 'CriOS/144.0.0.0'), safari.replace('Version/27.0.1', 'FxiOS/145.0')]) {
    const f = fixture(agent);
    assert.equal(f.actions.play, null);
    assert.equal(f.actions.pause, null);
    assert.equal(f.session.metadata.title, 'Episode');
    await f.context.safePlay();
    f.audio.dispatchEvent(new Event('playing'));
    f.audio.dispatchEvent(new Event('timeupdate'));
    f.audio.pause();
    assert.deepEqual(f.writes, []);
    assert.equal(f.audio.paused, true);
  }
});

test('WebKit still receives duration, progress, seeks and playback speed', async () => {
  const f = fixture(safari);
  await f.context.safePlay();
  for (const type of ['loadedmetadata', 'durationchange', 'timeupdate', 'seeked', 'ratechange']) {
    f.audio.currentTime += 5;
    f.audio.dispatchEvent(new Event(type));
    assert.deepEqual({ ...f.positions.at(-1) }, { duration: 120, position: f.audio.currentTime, playbackRate: 1.5 });
  }
  assert.deepEqual(f.writes, []);
});

test('WebKit keeps queue navigation and seek actions while native transport is used', () => {
  const f = fixture(safari);
  const moves = [];
  f.context.moveInQueue = offset => moves.push(offset);
  f.actions.nexttrack();
  f.actions.previoustrack();
  assert.deepEqual(moves, [1, -1]);
  f.actions.seekto({ seekTime: 50 });
  assert.equal(f.audio.currentTime, 50);
  f.actions.seekbackward({ seekOffset: 10 });
  assert.equal(f.audio.currentTime, 40);
  f.actions.seekforward({});
  assert.equal(f.audio.currentTime, 50);
});

test('Chromium and partial embedded clients retain scripted play and pause actions', async () => {
  for (const agent of [chrome, 'Mozilla/5.0 (Linux; Android 12) AppleWebKit/534.30 Version/4.0 Safari/534.30', '']) {
    const f = fixture(agent);
    assert.equal(typeof f.actions.play, 'function');
    assert.equal(typeof f.actions.pause, 'function');
    await f.actions.play();
    assert.equal(f.audio.paused, false);
    assert.equal(f.session.playbackState, 'playing');
    f.actions.pause();
    assert.equal(f.audio.paused, true);
    assert.equal(f.session.playbackState, 'paused');
  }
});

test('play and pause events leave registered system actions in place', async () => {
  const f = fixture();
  const count = f.registrations.length;
  await f.context.safePlay();
  f.audio.pause();
  await f.context.safePlay();
  assert.equal(f.registrations.length, count);
});

test('ending in the background keeps native state ownership and advances the queue once', () => {
  const f = fixture(safari);
  let advances = 0;
  f.context.moveInQueue = () => advances++;
  f.audio.ended = true;
  f.audio.currentTime = f.audio.duration;
  f.audio.dispatchEvent(new Event('pause'));
  f.audio.dispatchEvent(new Event('ended'));
  assert.equal(advances, 1);
  assert.deepEqual(f.writes, []);
});

test('missing and partial media-session capabilities do not interrupt playback', async () => {
  for (const mediaSession of [undefined, {}, {
    playbackState: 'none',
    setPositionState() { throw new Error('Unsupported'); },
    setActionHandler() { throw new Error('Unsupported'); },
  }]) {
    const f = fixture();
    f.context.navigator.mediaSession = mediaSession;
    f.context.updateMediaSession({ title: 'Episode', sourceID: 'source', durationSeconds: 120 });
    await f.context.safePlay();
    assert.equal(f.audio.paused, false);
    f.audio.pause();
  }
});
