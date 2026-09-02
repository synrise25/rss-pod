const SPEED_KEY = "rss-pod.player-speed";
const RESUME_KEY = "rss-pod.resume-state";
const DEMO_AUDIO = "/demo.mp3";
const MEDIA_ARTWORK = [
  { src: "/icons/favicon.png", sizes: "64x64", type: "image/png" },
  { src: "/icons/apple-touch-icon.png", sizes: "180x180", type: "image/png" },
];
const DEFAULT_SEEK_OFFSET = 10;

const localeKey = /^\/zh-cn(?:\/|$)/i.test(window.location.pathname) ? "zh-CN" : "en";
const copy = {
  en: {
    lang: "en",
    documentTitle: "Commute Podcasts",
    languageLabel: "Language",
    dateTabsLabel: "Choose a date",
    sourceSectionLabel: "Filter by source",
    sourceFilterLabel: "Feeds",
    episodeRegionLabel: "Podcast episodes",
    playerLabel: "Player",
    play: "Play",
    pause: "Pause",
    previous: "Previous episode",
    next: "Next episode",
    nowPlaying: "PLAYING",
    chooseEpisode: "Choose an episode",
    progressLabel: "Playback progress",
    speedLabel: "Speed",
    playbackSpeed: "Playback speed",
    loading: "Loading podcasts…",
    loadError: "Podcasts are unavailable right now. Please try again later.",
    empty: "No matching podcasts for this day",
    allSources: "All",
    untitled: "Untitled episode",
    durationUnavailable: "Duration unavailable",
    mediaAlbum: "Commute Podcasts",
    relativeDates: ["Today", "Yesterday", "2 days ago"],
    dateLocale: "en-US",
    greetings: ["Good morning", "Good afternoon", "Good afternoon", "Good evening"],
    greeting: (value) => `${value}. What's worth a listen?`,
    episodeCount: (count) => `${count} ${count === 1 ? "episode" : "episodes"}`,
    playEpisode: (title) => `Play: ${title}`,
    pauseEpisode: (title) => `Pause: ${title}`,
    durationLabel: (duration) => `Duration ${duration}`,
  },
  "zh-CN": {
    lang: "zh-CN",
    documentTitle: "通勤播客",
    languageLabel: "语言",
    dateTabsLabel: "选择日期",
    sourceSectionLabel: "按来源筛选",
    sourceFilterLabel: "内容来源",
    episodeRegionLabel: "播客列表",
    playerLabel: "播放器",
    play: "播放",
    pause: "暂停",
    previous: "上一条",
    next: "下一条",
    nowPlaying: "正在播放",
    chooseEpisode: "选择一条播客开始播放",
    progressLabel: "播放进度",
    speedLabel: "播放倍速",
    playbackSpeed: "播放速度",
    loading: "正在载入播客…",
    loadError: "暂时无法载入播客，请稍后重试",
    empty: "这一天还没有符合条件的播客",
    allSources: "全部",
    untitled: "未命名播客",
    durationUnavailable: "暂无播放时长",
    mediaAlbum: "通勤播客",
    relativeDates: ["今天", "昨天", "前天"],
    dateLocale: "zh-CN",
    greetings: ["早上好", "中午好", "下午好", "晚上好"],
    greeting: (value) => `${value}，今天听什么？`,
    episodeCount: (count) => `${count} 条`,
    playEpisode: (title) => `播放：${title}`,
    pauseEpisode: (title) => `暂停：${title}`,
    durationLabel: (duration) => `播放时长 ${duration}`,
  },
}[localeKey];

const demoContent = {
  en: {
    sources: ["Daily Brief", "Tech Radar", "Deep Reads"],
    titles: [
      "Why important thoughts surface at bedtime",
      "Open-source projects worth watching — Issue 182",
      "What would you do with more time?",
      "Can waking up early change your life?",
      "How engineers choose the right technology",
      "Which jobs will AI replace—and create?",
      "Five ideas worth revisiting",
      "How a small team maintains a large project",
    ],
  },
  "zh-CN": {
    sources: ["知乎日报", "V2EX 热门", "知乎话题"],
    titles: [
      "为什么我们总在睡前想起重要的事？关于记忆与焦虑的科学解释",
      "本周值得关注的开源项目 第 182 期",
      "如果给你一笔时间，你会用来做什么？来自 238 个真实回答的启发",
      "早起真的能改变人生吗？一项长达 5 年的追踪研究",
      "程序员如何优雅地进行技术选型？来自一线团队的实践经验",
      "AI 会取代哪些工作，又会创造哪些新机会？",
      "昨天最值得认真读完的五个回答",
      "一个小团队如何维护大型开源项目",
    ],
  },
}[localeKey];

const elements = {
  greeting: document.querySelector("#greeting"),
  languageSwitcher: document.querySelector("#language-switcher"),
  languageLinks: [...document.querySelectorAll("[data-locale]")],
  dateTabs: document.querySelector("#date-tabs"),
  sourceFilterSection: document.querySelector("#source-filter-section"),
  sourceFilterLabel: document.querySelector("#source-filter-label"),
  sourceFilters: document.querySelector("#source-filters"),
  episodeRegion: document.querySelector("#episode-region"),
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
  playerDock: document.querySelector("#player-dock"),
  nowPlayingLabel: document.querySelector("#now-playing-label"),
  speedLabel: document.querySelector("#speed-label"),
  speedLegend: document.querySelector("#speed-legend"),
  speedButtons: [...document.querySelectorAll("[data-speed]")],
};

applyLocale();

const dateOptions = createDateOptions();
const state = {
  sources: [],
  episodes: [],
  dateOptions,
  activeDate: dateOptions[0].key,
  activeSource: "all",
  currentEpisodeID: null,
  speed: isDemoMode() ? 1.2 : readStoredNumber(SPEED_KEY, 1.2),
  pendingResume: isDemoMode() ? null : readResumeState(),
  restoringResume: false,
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
  setStatus(copy.loading);
  try {
    const payload = isDemoMode() ? demoPayload() : await fetchPlayerData();
    state.sources = payload.sources;
    state.episodes = payload.episodes
      .map(normalizeEpisode)
      .filter((episode) => episode.id && episode.audioURL)
      .sort((a, b) => b.sortTime - a.sortTime);

    selectInitialEpisode();
  } catch (error) {
    console.error("load player", error);
    setStatus(copy.loadError);
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
    count.setAttribute("aria-label", copy.episodeCount(Number(count.textContent)));
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
  const sources = [{ id: "all", name: copy.allSources }, ...state.sources];
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
    setStatus(copy.empty);
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
    playButton.setAttribute(
      "aria-label",
      isPlaying ? copy.pauseEpisode(episode.title) : copy.playEpisode(episode.title),
    );
    playIcon.src = isPlaying ? "/icons/pause.svg" : "/icons/play.svg";
    playButton.addEventListener("click", () => toggleEpisode(episode));

    row.tabIndex = 0;
    row.setAttribute(
      "aria-label",
      isPlaying ? copy.pauseEpisode(episode.title) : copy.playEpisode(episode.title),
    );
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
  state.restoringResume = resumeAt > 0;
  clearMediaSessionPosition();
  elements.audio.defaultPlaybackRate = state.speed;
  if (resumeAt > 0) {
    const resumeEpisodeID = episode.id;
    elements.audio.addEventListener(
      "loadedmetadata",
      () => {
        if (state.currentEpisodeID !== resumeEpisodeID) return;
        elements.audio.currentTime = Math.min(resumeAt, Math.max(0, elements.audio.duration - 1));
        state.restoringResume = false;
      },
      { once: true },
    );
  }
  elements.audio.src = episode.audioURL;
  elements.audio.load();
  applyPlaybackRate();
  document.title = episode.title;
  elements.nowPlayingTitle.textContent = episode.title;
  elements.nowPlayingSource.textContent = sourceName(episode.sourceID);
  elements.playToggle.disabled = false;
  elements.progress.disabled = false;
  updateMediaSession(episode);
  renderEpisodeList();
  scrollCurrentEpisodeIntoView();

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
    updateMediaSessionPosition();
  });
  elements.audio.addEventListener("durationchange", () => {
    updateProgress();
    updateMediaSessionPosition();
  });
  elements.audio.addEventListener("timeupdate", () => {
    updateProgress();
    updateMediaSessionPosition();
    persistResumeState();
  });
  elements.audio.addEventListener("seeked", updateMediaSessionPosition);
  elements.audio.addEventListener("ratechange", updateMediaSessionPosition);
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
  elements.playToggle.setAttribute("aria-label", playing ? copy.pause : copy.play);
  updateMediaSessionPlaybackState();
  updateMediaSessionPosition();
  registerMediaSessionActions();
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
    title: String(episode.title || copy.untitled),
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
    element.setAttribute("aria-label", copy.durationUnavailable);
    return;
  }
  element.textContent = formatTotalDuration(durationSeconds);
  element.dateTime = `PT${Math.round(durationSeconds)}S`;
  element.setAttribute("aria-label", copy.durationLabel(element.textContent));
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

function applyLocale() {
  document.documentElement.lang = copy.lang;
  document.title = copy.documentTitle;
  elements.languageSwitcher.setAttribute("aria-label", copy.languageLabel);
  elements.dateTabs.setAttribute("aria-label", copy.dateTabsLabel);
  elements.sourceFilterSection.setAttribute("aria-label", copy.sourceSectionLabel);
  elements.sourceFilterLabel.textContent = copy.sourceFilterLabel;
  elements.episodeRegion.setAttribute("aria-label", copy.episodeRegionLabel);
  elements.playerDock.setAttribute("aria-label", copy.playerLabel);
  elements.playToggle.setAttribute("aria-label", copy.play);
  elements.previousButton.setAttribute("aria-label", copy.previous);
  elements.nextButton.setAttribute("aria-label", copy.next);
  elements.nowPlayingLabel.textContent = copy.nowPlaying;
  elements.nowPlayingTitle.textContent = copy.chooseEpisode;
  elements.progress.setAttribute("aria-label", copy.progressLabel);
  elements.speedLabel.textContent = copy.speedLabel;
  elements.speedLegend.textContent = copy.playbackSpeed;
  elements.statusMessage.textContent = copy.loading;

  for (const link of elements.languageLinks) {
    const selected = link.dataset.locale === localeKey;
    if (selected) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
    const targetPath = link.dataset.locale === "zh-CN" ? "/zh-cn" : "/en";
    link.href = `${targetPath}${window.location.search}${window.location.hash}`;
  }
}

function updateGreeting() {
  const hour = new Date().getHours();
  let greeting = copy.greetings[3];
  if (hour >= 5 && hour < 11) greeting = copy.greetings[0];
  else if (hour >= 11 && hour < 14) greeting = copy.greetings[1];
  else if (hour >= 14 && hour < 18) greeting = copy.greetings[2];
  elements.greeting.textContent = copy.greeting(greeting);
}

function createDateOptions() {
  const today = startOfDay(new Date());
  return [
    { relativeLabel: copy.relativeDates[0], date: today },
    { relativeLabel: copy.relativeDates[1], date: addDays(today, -1) },
    { relativeLabel: copy.relativeDates[2], date: addDays(today, -2) },
  ].map((option) => ({
    ...option,
    key: dateKey(option.date),
    monthDay: new Intl.DateTimeFormat(copy.dateLocale, {
      month: "short",
      day: "numeric",
    }).format(option.date),
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

function selectInitialEpisode() {
  const availableDateKeys = new Set(state.dateOptions.map((option) => option.key));
  const latestEpisode = state.episodes.find((candidate) => availableDateKeys.has(candidate.dayKey));
  if (!latestEpisode) {
    renderAll();
    return;
  }

  state.activeDate = latestEpisode.dayKey;
  const resumeEpisode = state.pendingResume?.episodeID
    ? state.episodes.find((candidate) => candidate.id === state.pendingResume.episodeID)
    : null;
  const episode = resumeEpisode?.dayKey === latestEpisode.dayKey ? resumeEpisode : latestEpisode;
  renderDateTabs();
  renderSourceFilters();
  selectEpisode(episode, {
    resumeAt: episode === resumeEpisode ? state.pendingResume.currentTime || 0 : 0,
  });
}

let lastPersistSecond = -1;
function persistResumeState() {
  if (isDemoMode() || !state.currentEpisodeID || state.restoringResume) return;
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
  const mediaSession = getMediaSession();
  if (!mediaSession) return;

  // Some embedded and in-car browsers implement the Media Session actions
  // without exposing MediaMetadata. Keep every capability independently
  // detectable so those browsers still receive transport and seek controls.
  const metadata = {
    title: episode.title,
    artist: sourceName(episode.sourceID),
    album: copy.mediaAlbum,
    artwork: MEDIA_ARTWORK,
  };
  try {
    mediaSession.metadata =
      typeof window.MediaMetadata === "function" ? new window.MediaMetadata(metadata) : metadata;
  } catch {
    // document.title remains a useful metadata fallback for partial clients.
  }

  registerMediaSessionActions();
  updateMediaSessionPlaybackState();
  updateMediaSessionPosition(episode.durationSeconds);
}

function registerMediaSessionActions() {
  const mediaSession = getMediaSession();
  if (!mediaSession || typeof mediaSession.setActionHandler !== "function") return;
  const handlers = {
    play: () => safePlay(),
    pause: () => elements.audio.pause(),
    previoustrack: () => moveInQueue(-1),
    nexttrack: () => moveInQueue(1),
    seekbackward: (details) => seekBy(-(details.seekOffset || DEFAULT_SEEK_OFFSET)),
    seekforward: (details) => seekBy(details.seekOffset || DEFAULT_SEEK_OFFSET),
    seekto: (details) => seekTo(details.seekTime, details.fastSeek),
  };
  for (const [action, handler] of Object.entries(handlers)) {
    try {
      mediaSession.setActionHandler(action, handler);
    } catch {
      // Some in-car browsers expose Media Session but only support a subset.
    }
  }
}

function updateMediaSessionPlaybackState() {
  const mediaSession = getMediaSession();
  if (!mediaSession || !("playbackState" in mediaSession)) return;
  try {
    mediaSession.playbackState = elements.audio.paused ? "paused" : "playing";
  } catch {
    // Playback remains controlled by the media element on partial clients.
  }
}

function updateMediaSessionPosition(fallbackDuration = null) {
  const mediaSession = getMediaSession();
  if (!mediaSession || typeof mediaSession.setPositionState !== "function") return;

  const audioDuration = elements.audio.duration;
  const duration =
    Number.isFinite(audioDuration) && audioDuration > 0 ? audioDuration : fallbackDuration;
  if (!Number.isFinite(duration) || duration <= 0) return;

  const currentTime = Number.isFinite(elements.audio.currentTime) ? elements.audio.currentTime : 0;
  const playbackRate =
    Number.isFinite(elements.audio.playbackRate) && elements.audio.playbackRate > 0
      ? elements.audio.playbackRate
      : 1;
  try {
    mediaSession.setPositionState({
      duration,
      playbackRate,
      position: Math.min(duration, Math.max(0, currentTime)),
    });
  } catch {
    // Invalid or stale media state should not interrupt playback.
  }
}

function clearMediaSessionPosition() {
  const mediaSession = getMediaSession();
  if (!mediaSession || typeof mediaSession.setPositionState !== "function") return;
  try {
    mediaSession.setPositionState();
  } catch {
    // Older clients may not support clearing position state.
  }
}

function seekBy(offset) {
  if (!Number.isFinite(offset)) return;
  seekTo(elements.audio.currentTime + offset);
}

function seekTo(time, fastSeek = false) {
  const duration = elements.audio.duration;
  if (!Number.isFinite(time) || !Number.isFinite(duration) || duration <= 0) return;
  const target = Math.min(duration, Math.max(0, time));
  try {
    if (fastSeek && typeof elements.audio.fastSeek === "function") elements.audio.fastSeek(target);
    else elements.audio.currentTime = target;
  } catch {
    // Ignore seek requests that the current media resource cannot satisfy.
  }
}

function getMediaSession() {
  return "mediaSession" in navigator ? navigator.mediaSession : null;
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
      { id: "zhihu-daily", name: demoContent.sources[0] },
      { id: "v2ex-hot", name: demoContent.sources[1] },
      { id: "zhihu-topic", name: demoContent.sources[2] },
    ],
    episodes: [
      demoEpisode("demo-1", "zhihu-daily", demoContent.titles[0], at(today, 7, 30)),
      demoEpisode("demo-2", "v2ex-hot", demoContent.titles[1], at(today, 6, 45)),
      demoEpisode("demo-3", "zhihu-topic", demoContent.titles[2], at(today, 5, 40)),
      demoEpisode("demo-4", "zhihu-daily", demoContent.titles[3], at(today, 5, 10)),
      demoEpisode("demo-5", "v2ex-hot", demoContent.titles[4], at(today, 4, 20)),
      demoEpisode("demo-6", "zhihu-topic", demoContent.titles[5], null, at(today, 3, 55)),
      demoEpisode("demo-7", "zhihu-daily", demoContent.titles[6], at(yesterday, 20, 15)),
      demoEpisode("demo-8", "v2ex-hot", demoContent.titles[7], at(dayBefore, 18, 20)),
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
