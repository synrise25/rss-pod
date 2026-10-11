// Apply saved appearance before loading styles, including when opening a new tab.
(() => {
  const storageKey = "rss-pod.appearance";
  const styles = ["silver", "crystal", "glacier"];
  const modes = ["day", "night", "auto"];
  const root = document.documentElement;
  const pageColors = {
    silver: { day: "#f0f1ed", night: "#141817" },
    crystal: { day: "#dfddd7", night: "#1c1917" },
    glacier: { day: "#f4f9ff", night: "#101b2a" },
  };
  let preferences = readPreferences();
  let inputs = [];
  const systemAppearance = window.matchMedia?.("(prefers-color-scheme: dark)") || { matches: false };

  function normalize(value) {
    return {
      style: styles.includes(value?.style) ? value.style : "silver",
      mode: modes.includes(value?.mode) ? value.mode : "day",
    };
  }

  function readPreferences() {
    try {
      return normalize(JSON.parse(localStorage.getItem(storageKey)));
    } catch {
      return normalize(null);
    }
  }

  function applyAppearance() {
    const appearance = preferences.mode === "auto"
      ? (systemAppearance.matches ? "night" : "day")
      : preferences.mode;
    root.dataset.style = preferences.style;
    root.dataset.appearance = appearance;
    root.style.colorScheme = appearance === "night" ? "dark" : "light";
    document.querySelector('meta[name="theme-color"]')?.setAttribute("content", pageColors[preferences.style][appearance]);
    document.querySelector('meta[name="color-scheme"]')?.setAttribute("content", appearance === "night" ? "dark" : "light");
    for (const input of inputs) {
      input.checked = input.value === preferences[input.name === "theme-style" ? "style" : "mode"];
    }
  }

  applyAppearance();

  window.addEventListener("storage", (event) => {
    if (event.key !== storageKey && event.key !== null) return;
    preferences = readPreferences();
    applyAppearance();
  });
  if (systemAppearance.addEventListener) {
    systemAppearance.addEventListener("change", applyAppearance);
  } else if (systemAppearance.addListener) {
    systemAppearance.addListener(applyAppearance);
  }
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) applyAppearance();
  });

  document.addEventListener("DOMContentLoaded", () => {
    const copy = /^(?:\/admin)?\/zh-cn(?:\/|$)/i.test(window.location.pathname) || window.location.pathname === "/admin" ? {
      button: "主题", title: "主题与外观", close: "完成", style: "外观风格",
      silver: "银雾信号", crystal: "烟晶玻璃", glacier: "冰川 · 原版",
      mode: "日间与夜间", day: "日间", night: "夜间", auto: "跟随系统",
    } : {
      button: "Theme", title: "Appearance", close: "Done", style: "Style",
      silver: "Silver Signal", crystal: "Smoked Crystal", glacier: "Glacier · Original",
      mode: "Day & night", day: "Day", night: "Night", auto: "System",
    };
    for (const element of document.querySelectorAll("[data-theme-copy]")) {
      element.textContent = copy[element.dataset.themeCopy];
    }

    const button = document.querySelector("#theme-button");
    const dialog = document.querySelector("#theme-dialog");
    const panel = document.querySelector(".theme-panel");
    const main = document.querySelector(".app-shell");
    inputs = [...dialog.querySelectorAll("input")];
    applyAppearance();

    function close() {
      dialog.hidden = true;
      main.inert = false;
      main.removeAttribute("aria-hidden");
      button.setAttribute("aria-expanded", "false");
      button.focus();
    }

    button.addEventListener("click", () => {
      dialog.hidden = false;
      button.setAttribute("aria-expanded", "true");
      panel.querySelector("input:checked").focus();
      main.inert = true;
      main.setAttribute("aria-hidden", "true");
    });
    document.querySelector("#theme-close").addEventListener("click", close);
    dialog.addEventListener("click", (event) => {
      if (event.target === dialog) close();
    });
    dialog.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        event.preventDefault();
        close();
      } else if (event.key === "Tab") {
        const focusable = [...panel.querySelectorAll("button, input:checked")];
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      }
    });
    dialog.addEventListener("change", (event) => {
      const input = event.target;
      if (input.name !== "theme-style" && input.name !== "theme-mode") return;
      preferences = normalize({ ...preferences, [input.name === "theme-style" ? "style" : "mode"]: input.value });
      try {
        localStorage.setItem(storageKey, JSON.stringify(preferences));
      } catch {
        // Appearance still works when browser storage is unavailable.
      }
      applyAppearance();
    });
  });
})();
