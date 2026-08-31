const SPEED_KEY = "rss-pod.player-speed";
const RESUME_KEY = "rss-pod.resume-state";
const DEMO_AUDIO = "/demo.mp3";

const elements = {
  greeting: document.querySelector("#greeting"),
  dateTabs: document.querySelector("#date-tabs"),
  sourceFilters: document.querySelector("#source-filters"),
  episodeList: document.querySelector("#episode-list"),
  statusMessage: document.querySelector("#status-message"),
  rowTemplate: document.querySelector("#episode-row-template"),
  audio: document.querySelector("#audio"),
  playToggle: document.querySelector("#play-toggle"),
  playToggleIcon: document.querySelector("#play-toggle-icon"),
  previousButton: document.querySelector("#previous-button"),
  nextButton: document.querySelector("#next-button"),
  nowPlayingTitle: document.querySelector("#now-playing-title"),
  nowPlayingSource: document.querySelector("#now-playing-source"),
  progress: document.querySelector("#progress"),
  elapsedTime: document.querySelector("#elapsed-time"),
  remainingTime: document.querySelector("#remaining-time"),
  speedButtons: [...document.querySelectorAll("[data-speed]")],
};

const state = {
  sources: [],
  episodes: [],
  dateOptions: createDateOptions(),
  activeDate: dateKey(new Date()),
  activeSource: "all",
  currentEpisodeID: null,
  speed: isDemoMode() ? 1.2 : readStoredNumber(SPEED_KEY, 1.2),
  pendingResume: isDemoMode() ? null : readResumeState(),
};

updateGreeting();
window.setInterval(updateGreeting, 60_000);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) updateGreeting();
});

bindPlayerEvents();
renderSpeed();
loadPlayer();

async function loadPlayer() {
  setStatus("正在载入播客…");
  try {
    const payload = isDemoMode() ? demoPayload() : await fetchPlayerData();
    state.sources = payload.sources;
    state.episodes = payload.episodes
      .map(normalizeEpisode)
      .filter((episode) => episode.id && episode.audioURL)
      .sort((a, b) => b.sortTime - a.sortTime);

    renderAll();
    restoreLastEpisode();
  } catch (error) {
    console.error("load player", error);
    setStatus("暂时无法载入播客，请稍后重试");
    renderDateTabs();
    renderSourceFilters();
  }
}

async function fetchPlayerData() {
  const start = startOfDay(state.dateOptions[state.dateOptions.length - 1].date);
  const before = startOfDay(addDays(state.dateOptions[0].date, 1));
  const params = new URLSearchParams({
    since: start.toISOString(),
    before: before.toISOString(),
    limit: "500",
  });

  const [sourcesResponse, episodesResponse] = await Promise.all([
    fetch("/api/v1/player/sources", { headers: { Accept: "application/json" } }),
    fetch(`/api/v1/player/episodes?${params}`, { headers: { Accept: "application/json" } }),
  ]);

  if (!sourcesResponse.ok || !episodesResponse.ok) {
    throw new Error(`player API returned ${sourcesResponse.status}/${episodesResponse.status}`);
  }

  const [sourcesPayload, episodesPayload] = await Promise.all([
    sourcesResponse.json(),
    episodesResponse.json(),
  ]);
  return {
    sources: Array.isArray(sourcesPayload.sources) ? sourcesPayload.sources : [],
    episodes: Array.isArray(episodesPayload.episodes) ? episodesPayload.episodes : [],
  };
}

function renderAll() {
  renderDateTabs();
  renderSourceFilters();
  renderEpisodeList();
}

function renderDateTabs() {
  elements.dateTabs.replaceChildren();
  for (const option of state.dateOptions) {
    const button = document.createElement("button");
    button.className = "date-tab";
    button.type = "button";
    button.role = "tab";
    button.dataset.date = option.key;
    button.setAttribute("aria-selected", String(state.activeDate === option.key));

    const label = document.createElement("span");
    label.textContent = `${option.relativeLabel} ${option.monthDay}`;
    const count = document.createElement("span");
    count.className = "date-count";
    count.textContent = String(countEpisodesForDate(option.key));
    count.setAttribute("aria-label", `${count.textContent} 条`);
    button.append(label, count);

    button.addEventListener("click", () => {
      state.activeDate = option.key;
      renderAll();
    });
    elements.dateTabs.append(button);
  }
}

function renderSourceFilters() {
  elements.sourceFilters.replaceChildren();
  const sources = [{ id: "all", name: "全部" }, ...state.sources];
  for (const source of sources) {
    const button = document.createElement("button");
    button.className = "source-filter";
    button.type = "button";
    button.textContent = source.name;
    button.dataset.source = source.id;
    button.setAttribute("aria-pressed", String(state.activeSource === source.id));
    button.addEventListener("click", () => {
      state.activeSource = source.id;
      renderSourceFilters();
      renderEpisodeList();
    });
    elements.sourceFilters.append(button);
  }
}

function renderEpisodeList() {
  const previousScrollTop = elements.episodeList.scrollTop;
  elements.episodeList.replaceChildren();
  const episodes = visibleEpisodes();
  if (episodes.length === 0) {
    setStatus("这一天还没有符合条件的播客");
    updateQueueButtons();
    return;
  }

  setStatus("");
  const fragment = document.createDocumentFragment();
  for (const episode of episodes) {
    const row = elements.rowTemplate.content.firstElementChild.cloneNode(true);
    row.dataset.episodeId = episode.id;
    row.classList.toggle("is-current", episode.id === state.currentEpisodeID);
    row.classList.toggle("is-playing", episode.id === state.currentEpisodeID && !elements.audio.paused);

    const playButton = row.querySelector(".episode-play-button");
    const playIcon = row.querySelector(".episode-play-button img");
    const isPlaying = episode.id === state.currentEpisodeID && !elements.audio.paused;
    playButton.setAttribute("aria-label", `${isPlaying ? "暂停" : "播放"}：${episode.title}`);
    playIcon.src = isPlaying ? "/icons/pause.svg" : "/icons/play.svg";
    playButton.addEventListener("click", () => toggleEpisode(episode));

    row.tabIndex = 0;
    row.setAttribute("aria-label", `${isPlaying ? "暂停" : "播放"}：${episode.title}`);
    row.addEventListener("click", (event) => {
      if (event.target.closest("button, a, input")) return;
      toggleEpisode(episode);
    });
    row.addEventListener("keydown", (event) => {
      if (event.target !== row || (event.key !== "Enter" && event.key !== " ")) return;
      event.preventDefault();
      toggleEpisode(episode);
    });

    row.querySelector(".episode-source").textContent = sourceName(episode.sourceID);
    const title = row.querySelector(".episode-title");
    title.textContent = episode.title;
    title.title = episode.title;

    const time = row.querySelector(".episode-time");
    renderEpisodeDuration(time, episode.durationSeconds);
    fragment.append(row);
  }
  elements.episodeList.append(fragment);
  elements.episodeList.scrollTop = previousScrollTop;
  updateQueueButtons();
}

async function toggleEpisode(episode) {
  if (state.currentEpisodeID === episode.id) {
    if (elements.audio.paused) {
      await safePlay();
    } else {
      elements.audio.pause();
    }
    return;
  }
  selectEpisode(episode, { autoplay: true });
}

function selectEpisode(episode, { autoplay = false, resumeAt = 0 } = {}) {
  state.currentEpisodeID = episode.id;
  elements.audio.defaultPlaybackRate = state.speed;
  elements.audio.src = episode.audioURL;
  elements.audio.load();
  applyPlaybackRate();
  elements.nowPlayingTitle.textContent = episode.title;
  elements.nowPlayingSource.textContent = sourceName(episode.sourceID);
  elements.playToggle.disabled = false;
  elements.progress.disabled = false;
  updateMediaSession(episode);
  renderEpisodeList();
  scrollCurrentEpisodeIntoView();

  if (resumeAt > 0) {
    elements.audio.addEventListener(
      "loadedmetadata",
      () => {
        elements.audio.currentTime = Math.min(resumeAt, Math.max(0, elements.audio.duration - 1));
      },
      { once: true },
    );
  }
  if (autoplay) safePlay();
}

async function safePlay() {
  try {
    await elements.audio.play();
  } catch (error) {
    if (!isDemoMode()) console.error("play audio", error);
  }
}

function bindPlayerEvents() {
  elements.playToggle.addEventListener("click", () => {
    if (elements.audio.paused) safePlay();
    else elements.audio.pause();
  });
  elements.previousButton.addEventListener("click", () => moveInQueue(-1));
  elements.nextButton.addEventListener("click", () => moveInQueue(1));
  elements.audio.addEventListener("play", renderPlaybackState);
  elements.audio.addEventListener("pause", renderPlaybackState);
  elements.audio.addEventListener("ended", () => moveInQueue(1));
  elements.audio.addEventListener("loadedmetadata", () => {
    applyPlaybackRate();
    updateProgress();
  });
  elements.audio.addEventListener("durationchange", updateProgress);
  elements.audio.addEventListener("timeupdate", () => {
    updateProgress();
    persistResumeState();
  });
  elements.progress.addEventListener("input", () => {
    if (!Number.isFinite(elements.audio.duration)) return;
    elements.audio.currentTime = (Number(elements.progress.value) / 100) * elements.audio.duration;
  });
  for (const button of elements.speedButtons) {
    button.addEventListener("click", () => setSpeed(Number(button.dataset.speed)));
  }
}

function renderPlaybackState() {
  const playing = !elements.audio.paused;
  elements.playToggleIcon.src = playing ? "/icons/pause.svg" : "/icons/play.svg";
  elements.playToggle.setAttribute("aria-label", playing ? "暂停" : "播放");
  renderEpisodeList();
  scrollCurrentEpisodeIntoView();
}

function moveInQueue(offset) {
  const queue = visibleEpisodes();
  if (queue.length === 0) return;
  const currentIndex = queue.findIndex((episode) => episode.id === state.currentEpisodeID);
  const nextIndex = currentIndex < 0 ? 0 : currentIndex + offset;
  if (nextIndex < 0 || nextIndex >= queue.length) return;
  selectEpisode(queue[nextIndex], { autoplay: true });
}

function updateQueueButtons() {
  const queue = visibleEpisodes();
  const index = queue.findIndex((episode) => episode.id === state.currentEpisodeID);
  elements.previousButton.disabled = index <= 0;
  elements.nextButton.disabled = index < 0 || index >= queue.length - 1;
}

function setSpeed(speed) {
  if (![1, 1.2, 1.5].includes(speed)) return;
  state.speed = speed;
  applyPlaybackRate();
  if (!isDemoMode()) writeStorage(SPEED_KEY, String(speed));
  renderSpeed();
}

function applyPlaybackRate() {
  // Some in-car browsers reset playbackRate when audio.load() switches sources.
  // defaultPlaybackRate makes the next resource inherit the selection, while
  // playbackRate updates the currently loaded resource immediately.
  elements.audio.defaultPlaybackRate = state.speed;
  elements.audio.playbackRate = state.speed;
}

function renderSpeed() {
  for (const button of elements.speedButtons) {
    button.setAttribute("aria-pressed", String(Number(button.dataset.speed) === state.speed));
  }
}

function updateProgress() {
  const duration = elements.audio.duration;
  const current = elements.audio.currentTime || 0;
  if (!Number.isFinite(duration) || duration <= 0) {
    elements.progress.value = "0";
    elements.progress.style.setProperty("--progress-value", "0%");
    elements.elapsedTime.textContent = formatDuration(current);
    elements.remainingTime.textContent = "--:--";
    return;
  }
  const progress = Math.min(100, Math.max(0, (current / duration) * 100));
  elements.progress.value = String(progress);
  elements.progress.style.setProperty("--progress-value", `${progress}%`);
  elements.elapsedTime.textContent = formatDuration(current);
  elements.remainingTime.textContent = `-${formatDuration(Math.max(0, duration - current))}`;
}

function normalizeEpisode(episode) {
  const publishedAt = parseDate(episode.published_at);
  const durationSeconds = Number(episode.audio_duration_seconds);
  return {
    id: String(episode.id || ""),
    sourceID: String(episode.source_id || ""),
    title: String(episode.title || "未命名播客"),
    audioURL: String(episode.audio_url || ""),
    publishedAt,
    durationSeconds: Number.isFinite(durationSeconds) && durationSeconds > 0 ? durationSeconds : null,
    dayKey: publishedAt ? dateKey(publishedAt) : "",
    sortTime: publishedAt?.getTime() || 0,
  };
}

function renderEpisodeDuration(element, durationSeconds) {
  if (!Number.isFinite(durationSeconds) || durationSeconds <= 0) {
    element.textContent = "--:--";
    element.removeAttribute("datetime");
    element.setAttribute("aria-label", "暂无播放时长");
    return;
  }
  element.textContent = formatTotalDuration(durationSeconds);
  element.dateTime = `PT${Math.round(durationSeconds)}S`;
  element.setAttribute("aria-label", `播放时长 ${element.textContent}`);
}

function scrollCurrentEpisodeIntoView() {
  window.requestAnimationFrame(() => {
    const row = [...elements.episodeList.querySelectorAll(".episode-row")].find(
      (candidate) => candidate.dataset.episodeId === state.currentEpisodeID,
    );
    if (!row) return;
    const listRect = elements.episodeList.getBoundingClientRect();
    const rowRect = row.getBoundingClientRect();
    if (rowRect.top >= listRect.top && rowRect.bottom <= listRect.bottom) return;
    const top =
      elements.episodeList.scrollTop +
      rowRect.top -
      listRect.top -
      (elements.episodeList.clientHeight - rowRect.height) / 2;
    // Switching sources triggers both a list render and an audio `play` event.
    // Assigning scrollTop directly keeps the final position deterministic even
    // when those updates happen within the same frame.
    elements.episodeList.scrollTop = Math.max(0, top);
  });
}

function visibleEpisodes() {
  return state.episodes.filter(
    (episode) =>
      episode.dayKey === state.activeDate &&
      (state.activeSource === "all" || episode.sourceID === state.activeSource),
  );
}

function countEpisodesForDate(key) {
  return state.episodes.filter((episode) => episode.dayKey === key).length;
}

function sourceName(sourceID) {
  return state.sources.find((source) => source.id === sourceID)?.name || sourceID;
}

function updateGreeting() {
  const hour = new Date().getHours();
  let greeting = "晚上好";
  if (hour >= 5 && hour < 11) greeting = "早上好";
  else if (hour >= 11 && hour < 14) greeting = "中午好";
  else if (hour >= 14 && hour < 18) greeting = "下午好";
  elements.greeting.textContent = `${greeting}，今天听什么？`;
}

function createDateOptions() {
  const today = startOfDay(new Date());
  return [
    { relativeLabel: "今天", date: today },
    { relativeLabel: "昨天", date: addDays(today, -1) },
    { relativeLabel: "前天", date: addDays(today, -2) },
  ].map((option) => ({
    ...option,
    key: dateKey(option.date),
    monthDay: `${option.date.getMonth() + 1}月${option.date.getDate()}日`,
  }));
}

function dateKey(date) {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

function startOfDay(date) {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate());
}

function addDays(date, days) {
  const next = new Date(date);
  next.setDate(next.getDate() + days);
  return next;
}

function parseDate(value) {
  if (!value) return null;
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

function formatDuration(seconds) {
  if (!Number.isFinite(seconds) || seconds < 0) return "0:00";
  const whole = Math.floor(seconds);
  const minutes = Math.floor(whole / 60);
  return `${minutes}:${String(whole % 60).padStart(2, "0")}`;
}

function formatTotalDuration(seconds) {
  if (!Number.isFinite(seconds) || seconds < 0) return "--:--";
  const whole = Math.round(seconds);
  const minutes = Math.floor(whole / 60);
  return `${minutes}:${String(whole % 60).padStart(2, "0")}`;
}

function setStatus(message) {
  elements.statusMessage.textContent = message;
  elements.statusMessage.hidden = message === "";
}

function restoreLastEpisode() {
  if (!state.pendingResume?.episodeID) return;
  const episode = state.episodes.find((candidate) => candidate.id === state.pendingResume.episodeID);
  if (!episode) return;
  state.activeDate = episode.dayKey || state.activeDate;
  selectEpisode(episode, { resumeAt: state.pendingResume.currentTime || 0 });
  renderAll();
}

let lastPersistSecond = -1;
function persistResumeState() {
  if (isDemoMode() || !state.currentEpisodeID) return;
  const wholeSecond = Math.floor(elements.audio.currentTime || 0);
  if (wholeSecond === lastPersistSecond || wholeSecond % 5 !== 0) return;
  lastPersistSecond = wholeSecond;
  writeStorage(
    RESUME_KEY,
    JSON.stringify({ episodeID: state.currentEpisodeID, currentTime: wholeSecond }),
  );
}

function readResumeState() {
  try {
    const value = JSON.parse(localStorage.getItem(RESUME_KEY) || "null");
    return value && typeof value === "object" ? value : null;
  } catch {
    return null;
  }
}

function readStoredNumber(key, fallback) {
  try {
    const value = Number(localStorage.getItem(key));
    return Number.isFinite(value) && value > 0 ? value : fallback;
  } catch {
    return fallback;
  }
}

function writeStorage(key, value) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Playback remains functional in browsers that disable storage.
  }
}

function updateMediaSession(episode) {
  if (!("mediaSession" in navigator) || !("MediaMetadata" in window)) return;
  navigator.mediaSession.metadata = new MediaMetadata({
    title: episode.title,
    artist: sourceName(episode.sourceID),
    album: "通勤播客",
  });
  const handlers = {
    play: () => safePlay(),
    pause: () => elements.audio.pause(),
    previoustrack: () => moveInQueue(-1),
    nexttrack: () => moveInQueue(1),
  };
  for (const [action, handler] of Object.entries(handlers)) {
    try {
      navigator.mediaSession.setActionHandler(action, handler);
    } catch {
      // Some in-car browsers expose Media Session but only support a subset.
    }
  }
}

function isDemoMode() {
  return new URLSearchParams(window.location.search).get("demo") === "1";
}

function demoPayload() {
  const [today, yesterday, dayBefore] = state.dateOptions;
  const at = (option, hour, minute) => {
    const date = new Date(option.date);
    date.setHours(hour, minute, 0, 0);
    return date.toISOString();
  };
  return {
    sources: [
      { id: "zhihu-daily", name: "知乎日报" },
      { id: "v2ex-hot", name: "V2EX 热门" },
      { id: "zhihu-topic", name: "知乎话题" },
    ],
    episodes: [
      demoEpisode("demo-1", "zhihu-daily", "为什么我们总在睡前想起重要的事？关于记忆与焦虑的科学解释", at(today, 7, 30)),
      demoEpisode("demo-2", "v2ex-hot", "本周值得关注的开源项目 第 182 期", at(today, 6, 45)),
      demoEpisode("demo-3", "zhihu-topic", "如果给你一笔时间，你会用来做什么？来自 238 个真实回答的启发", at(today, 5, 40)),
      demoEpisode("demo-4", "zhihu-daily", "早起真的能改变人生吗？一项长达 5 年的追踪研究", at(today, 5, 10)),
      demoEpisode("demo-5", "v2ex-hot", "程序员如何优雅地进行技术选型？来自一线团队的实践经验", at(today, 4, 20)),
      demoEpisode("demo-6", "zhihu-topic", "AI 会取代哪些工作，又会创造哪些新机会？", null, at(today, 3, 55)),
      demoEpisode("demo-7", "zhihu-daily", "昨天最值得认真读完的五个回答", at(yesterday, 20, 15)),
      demoEpisode("demo-8", "v2ex-hot", "一个小团队如何维护大型开源项目", at(dayBefore, 18, 20)),
    ],
  };
}

function demoEpisode(id, sourceID, title, originalPublishedAt, publishedAt = originalPublishedAt) {
  return {
    id,
    source_id: sourceID,
    title,
    audio_url: DEMO_AUDIO,
    audio_duration_seconds: 30,
    published_at: publishedAt,
    original_published_at: originalPublishedAt,
  };
}
