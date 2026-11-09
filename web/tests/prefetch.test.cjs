const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');

const source = readFileSync(require('node:path').join(__dirname, '../app.js'), 'utf8');
function fixture() {
  const created = [];
  const requests = [];
  const objectURLs = [];
  const revoked = [];
  class Audio extends EventTarget {
    constructor() {
      super();
      this.paused = true;
      this.duration = 30;
      this.currentTime = 0;
      this.buffered = { length: 1, start: () => 0, end: () => 30 };
      this.loads = 0;
      created.push(this);
    }
    load() { this.loads++; this.paused = true; this.currentTime = 0; }
  }
  const audio = new Audio();
  audio.paused = false;
  audio.id = 'audio';
  const queue = ['a', 'b', 'c'].map(id => ({ id, audioURL: `https://media.example/${id}.mp3` }));
  const context = vm.createContext({
    Audio, AbortController, nextAudio: null, activeAudioObjectURL: null, audioEvents: null,
    fetch: (url, options) => new Promise((resolve, reject) => requests.push({ url, signal: options.signal, resolve, reject })),
    URL: {
      createObjectURL(blob) {
        const url = `blob:audio-${objectURLs.length}`;
        objectURLs.push({ blob, url });
        return url;
      },
      revokeObjectURL(url) { revoked.push(url); },
    },
    isAdminPage: false, adminCSRF: '',
    state: { currentEpisodeID: 'a', speed: 1.5 },
    elements: { audio, nowPlayingTitle: {}, nowPlayingSource: {}, playToggle: {}, progress: { style: { setProperty() {} } }, elapsedTime: {}, remainingTime: {} },
    document: {}, visibleEpisodes: () => queue, sourceName: () => 'Source',
    clearMediaSessionPosition() {}, applyPlaybackRate() {}, updateMediaSession() {},
    renderEpisodeList() {}, scrollCurrentEpisodeIntoView() {}, safePlay() {},
    renderPlaybackState() {}, moveInQueue() {}, updateProgress() {},
    updateMediaSessionPosition() {}, persistResumeState() {},
  });
  for (const name of ['selectEpisode', 'bindAudioEvents', 'clearNextAudio', 'updateNextAudio', 'downloadNextAudio', 'isAudioFullyBuffered', 'updateProgress', 'formatDuration']) {
    const match = source.match(new RegExp(`(?:async )?function ${name}\\(`));
    const rest = source.slice(match.index);
    vm.runInContext(rest.slice(0, rest.indexOf('\n}\n') + 3), context);
  }
  async function complete() {
    const preload = context.nextAudio;
    requests.at(-1).resolve({ ok: true, blob: async () => new Blob(['MP3 data'], { type: 'audio/mpeg' }) });
    await preload.promise;
    return preload;
  }
  return { context, audio, queue, created, requests, objectURLs, revoked, complete };
}

test('waits for a full continuous buffer and active playback', () => {
  const { context: c, audio, requests } = fixture();
  audio.buffered.end = () => 20;
  c.updateNextAudio();
  assert.equal(requests.length, 0);
  audio.buffered.end = () => 30;
  audio.buffered.start = () => 10;
  c.updateNextAudio();
  assert.equal(requests.length, 0);
  audio.buffered.start = () => 0;
  audio.paused = true;
  c.updateNextAudio();
  assert.equal(requests.length, 0);
  audio.paused = false;
  c.updateNextAudio();
  assert.equal(c.nextAudio.episodeID, 'b');
  c.updateNextAudio();
  assert.equal(requests.length, 1);
});

test('preloading downloads bytes without creating another media session', async () => {
  const f = fixture();
  f.context.updateNextAudio();
  const preload = await f.complete();
  assert.equal(f.requests[0].url, f.queue[1].audioURL);
  assert.equal(f.requests[0].signal, preload.controller.signal);
  assert.equal(f.created.length, 1);
  assert.equal(f.audio.loads, 0);
  assert.equal(f.audio.paused, false);
  assert.equal(preload.objectURL, 'blob:audio-0');
});

test('next playback uses the downloaded file on the same audio element', async () => {
  const { context: c, audio, queue, created, revoked, complete } = fixture();
  c.updateNextAudio();
  const cached = await complete();
  const cachedURL = cached.objectURL;
  c.selectEpisode(queue[1], { autoplay: true });
  assert.equal(c.elements.audio, audio);
  assert.equal(created.length, 1);
  assert.equal(audio.src, cachedURL);
  assert.equal(audio.loads, 1);
  assert.equal(audio.defaultPlaybackRate, 1.5);
  assert.equal(c.activeAudioObjectURL, cachedURL);
  assert.equal(c.nextAudio, null);
  assert.deepEqual(revoked, []);
  assert.equal(c.elements.progress.value, '0');
  assert.equal(c.elements.elapsedTime.textContent, '0:00');
  assert.equal(c.elements.remainingTime.textContent, '-0:30');
});

test('selection during an unfinished download immediately uses the original URL', () => {
  const { context: c, audio, queue, requests } = fixture();
  let plays = 0;
  c.safePlay = () => plays++;
  c.updateNextAudio();
  c.selectEpisode(queue[1], { autoplay: true });
  assert.equal(c.elements.audio, audio);
  assert.equal(audio.src, queue[1].audioURL);
  assert.equal(requests[0].signal.aborted, true);
  assert.equal(c.nextAudio, null);
  assert.equal(plays, 1);
});

test('filter changes discard old downloads and select the actual next episode', async () => {
  const { context: c, queue, requests, revoked, complete } = fixture();
  c.updateNextAudio();
  await complete();
  queue.splice(1, 1);
  c.updateNextAudio();
  assert.equal(requests[0].signal.aborted, true);
  assert.deepEqual(revoked, ['blob:audio-0']);
  assert.equal(c.nextAudio.episodeID, 'c');
  queue.splice(0, 1);
  c.updateNextAudio();
  assert.equal(c.nextAudio, null);
  assert.equal(requests[1].signal.aborted, true);
});

test('jumping to another episode cancels preloading', () => {
  const { context: c, audio, queue, requests } = fixture();
  c.updateNextAudio();
  c.selectEpisode(queue[2], { autoplay: true });
  assert.equal(c.elements.audio, audio);
  assert.equal(audio.src, queue[2].audioURL);
  assert.equal(requests[0].signal.aborted, true);
  assert.equal(c.nextAudio, null);
});

test('failed, blocked and empty downloads fall back without repeatedly retrying', async () => {
  for (const failure of ['http', 'cors', 'empty']) {
    const { context: c, audio, queue, requests, objectURLs } = fixture();
    c.updateNextAudio();
    const preload = c.nextAudio;
    if (failure === 'cors') requests[0].reject(new TypeError('CORS blocked'));
    else requests[0].resolve({ ok: failure !== 'http', blob: async () => new Blob([]) });
    await preload.promise;
    c.updateNextAudio();
    assert.equal(requests.length, 1);
    assert.equal(objectURLs.length, 0);
    c.selectEpisode(queue[1], { autoplay: true });
    assert.equal(audio.src, queue[1].audioURL);
  }
});

test('end of queue and logged-out admin do not prefetch', () => {
  const { context: c, requests } = fixture();
  c.state.currentEpisodeID = 'c';
  c.updateNextAudio();
  assert.equal(c.nextAudio, null);
  c.state.currentEpisodeID = 'a';
  c.isAdminPage = true;
  c.updateNextAudio();
  assert.equal(c.nextAudio, null);
  assert.equal(requests.length, 0);
});

test('buffer events trigger downloading and waiting cancels unfinished downloads', () => {
  const { context: c, audio, requests } = fixture();
  c.bindAudioEvents();
  audio.dispatchEvent(new Event('progress'));
  assert.equal(c.nextAudio.episodeID, 'b');
  audio.dispatchEvent(new Event('waiting'));
  assert.equal(c.nextAudio, null);
  assert.equal(requests[0].signal.aborted, true);
});

test('cancelled responses cannot create object URLs or overwrite a newer download', async () => {
  const { context: c, audio, queue, requests, objectURLs } = fixture();
  c.updateNextAudio();
  const cancelled = c.nextAudio;
  c.selectEpisode(queue[1]);
  audio.paused = false;
  c.updateNextAudio();
  const current = c.nextAudio;
  requests[0].resolve({ ok: true, blob: async () => new Blob(['old download']) });
  await cancelled.promise;
  assert.equal(objectURLs.length, 0);
  assert.equal(c.nextAudio, current);
  assert.equal(current.episodeID, 'c');
  assert.equal(current.controller.signal.aborted, false);
});

test('repeated selections keep only the current queue and resume handlers', () => {
  const { context: c, audio, queue } = fixture();
  let advances = 0;
  c.moveInQueue = () => advances++;
  c.selectEpisode(queue[1], { resumeAt: 10 });
  c.selectEpisode(queue[2]);
  audio.dispatchEvent(new Event('loadedmetadata'));
  assert.equal(audio.currentTime, 0);
  audio.dispatchEvent(new Event('ended'));
  assert.equal(advances, 1);
});

test('waiting keeps a completed next episode', async () => {
  const { context: c, audio, complete } = fixture();
  c.bindAudioEvents();
  c.updateNextAudio();
  const cached = await complete();
  audio.dispatchEvent(new Event('waiting'));
  assert.equal(c.nextAudio, cached);
  assert.equal(cached.controller.signal.aborted, false);
});

test('completed downloads are released after playback switches away', async () => {
  const { context: c, audio, queue, revoked, complete } = fixture();
  c.updateNextAudio();
  await complete();
  c.selectEpisode(queue[1]);
  audio.paused = false;
  c.updateNextAudio();
  await complete();
  c.clearNextAudio();
  assert.deepEqual(revoked, ['blob:audio-1']);
  assert.equal(audio.src, 'blob:audio-0');
  c.selectEpisode(queue[0]);
  assert.deepEqual(revoked, ['blob:audio-1', 'blob:audio-0']);
  assert.equal(c.activeAudioObjectURL, null);
  assert.equal(audio.src, queue[0].audioURL);
});

test('blocked Blob playback falls back once and preserves autoplay and resume', async () => {
  const { context: c, audio, queue, revoked, complete } = fixture();
  let plays = 0;
  c.safePlay = () => plays++;
  c.updateNextAudio();
  await complete();
  c.selectEpisode(queue[1], { autoplay: true, resumeAt: 12 });
  audio.dispatchEvent(new Event('error'));
  assert.equal(audio.src, queue[1].audioURL);
  assert.equal(audio.loads, 2);
  assert.equal(plays, 2);
  assert.equal(c.activeAudioObjectURL, null);
  assert.deepEqual(revoked, ['blob:audio-0']);
  audio.dispatchEvent(new Event('loadedmetadata'));
  assert.equal(audio.currentTime, 12);
  audio.dispatchEvent(new Event('error'));
  assert.equal(audio.loads, 2);
  assert.equal(plays, 2);
});

test('Blob fallback respects a pause requested after track selection', async () => {
  const { context: c, audio, queue, complete } = fixture();
  let plays = 0;
  c.safePlay = () => plays++;
  c.updateNextAudio();
  await complete();
  c.selectEpisode(queue[1], { autoplay: true });
  audio.dispatchEvent(new Event('pause'));
  audio.dispatchEvent(new Event('error'));
  assert.equal(audio.src, queue[1].audioURL);
  assert.equal(plays, 1);
  assert.equal(audio.paused, true);
});

test('Blob fallback respects a later play request and the current position', async () => {
  const { context: c, audio, queue, complete } = fixture();
  let plays = 0;
  c.safePlay = () => plays++;
  c.updateNextAudio();
  await complete();
  c.selectEpisode(queue[1], { resumeAt: 10 });
  audio.dispatchEvent(new Event('loadedmetadata'));
  audio.currentTime = 17;
  audio.dispatchEvent(new Event('play'));
  audio.dispatchEvent(new Event('error'));
  assert.equal(audio.src, queue[1].audioURL);
  assert.equal(plays, 1);
  audio.dispatchEvent(new Event('loadedmetadata'));
  assert.equal(audio.currentTime, 17);
});

test('stale Blob errors cannot restore an old selection', async () => {
  const { context: c, audio, queue, complete } = fixture();
  c.updateNextAudio();
  await complete();
  c.selectEpisode(queue[1]);
  c.selectEpisode(queue[2]);
  audio.dispatchEvent(new Event('error'));
  assert.equal(audio.src, queue[2].audioURL);
  assert.equal(audio.loads, 2);
});

test('Blob fallback preserves a seek to zero after the saved position was restored', async () => {
  const { context: c, audio, queue, complete } = fixture();
  c.updateNextAudio();
  await complete();
  c.selectEpisode(queue[1], { resumeAt: 10 });
  audio.dispatchEvent(new Event('loadedmetadata'));
  assert.equal(audio.currentTime, 10);
  audio.currentTime = 0;
  audio.dispatchEvent(new Event('error'));
  assert.equal(audio.src, queue[1].audioURL);
  audio.dispatchEvent(new Event('loadedmetadata'));
  assert.equal(audio.currentTime, 0);
});
