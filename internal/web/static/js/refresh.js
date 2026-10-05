const main = document.querySelector("main[data-refresh]");

if (main) {
  setInterval(async () => {
    if (document.hidden || !document.getSelection().isCollapsed) return;
    try {
      const res = await fetch(location.href, { headers: { "X-Partial": "1" } });
      if (!res.ok) return;
      const html = await res.text();
      const scroll = [...main.querySelectorAll("pre")].map((el) => el.scrollTop);
      main.innerHTML = html;
      main.querySelectorAll("pre").forEach((el, i) => {
        el.scrollTop = scroll[i] ?? 0;
      });
    } catch {
      // Keep the last rendered state while the controller is unreachable.
    }
  }, Number(main.dataset.refresh));
}
