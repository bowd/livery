(() => {
  // Theme switcher
  const saved = localStorage.getItem("livery-site-theme");
  if (saved === "dark") document.documentElement.classList.add("dark");

  const themeBtn = document.getElementById("theme-toggle");
  const renderThemeIcon = () => {
    if (!themeBtn) return;
    const dark = document.documentElement.classList.contains("dark");
    themeBtn.innerHTML = dark
      ? `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/></svg>`
      : `<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>`;
  };
  if (themeBtn) {
    renderThemeIcon();
    themeBtn.addEventListener("click", () => {
      const next = !document.documentElement.classList.contains("dark");
      document.documentElement.classList.toggle("dark", next);
      localStorage.setItem("livery-site-theme", next ? "dark" : "light");
      renderThemeIcon();
    });
  }

  // Install command switcher
  const CMDS = {
    shell: "curl -fsSL https://liv.bowd.io/install.sh | sh",
    go: "CGO_ENABLED=0 go install github.com/bowd/livery/cmd/liv@latest",
    nix: "nix profile add github:bowd/livery",
    source: "git clone https://github.com/bowd/livery.git && cd livery && make install",
  };

  const cmdText = document.getElementById("install-cmd-text");
  const copyBtn = document.getElementById("copy-install-btn");
  let activeCmd = CMDS.shell;

  document.querySelectorAll("[data-itab]").forEach((btn) => {
    btn.addEventListener("click", () => {
      document.querySelectorAll("[data-itab]").forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      activeCmd = CMDS[btn.dataset.itab] || CMDS.shell;
      if (cmdText) cmdText.textContent = activeCmd;
    });
  });

  if (copyBtn) {
    copyBtn.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(activeCmd);
        copyBtn.textContent = "Copied ✓";
        setTimeout(() => { copyBtn.textContent = "Copy"; }, 1400);
      } catch (_) {}
    });
  }

  // Hero media stage switcher
  const stageImg = document.getElementById("hero-stage-img");
  document.querySelectorAll("[data-stage-src]").forEach((btn) => {
    btn.addEventListener("click", () => {
      document.querySelectorAll("[data-stage-src]").forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      if (stageImg) {
        stageImg.src = btn.dataset.stageSrc;
        stageImg.alt = btn.dataset.stageAlt || "Livery demo";
      }
    });
  });

  // Feature tabs
  document.querySelectorAll("[data-ftab]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const target = btn.dataset.ftab;
      document.querySelectorAll("[data-ftab]").forEach((b) => b.classList.toggle("active", b === btn));
      document.querySelectorAll("[data-fpanel]").forEach((p) => p.classList.toggle("active", p.dataset.fpanel === target));
    });
  });

  // Attach Copy buttons to code blocks
  document.querySelectorAll("pre.code-block, .docs-article pre").forEach((pre) => {
    if (pre.parentElement && pre.parentElement.classList.contains("code-wrap")) return;
    const wrap = document.createElement("div");
    wrap.className = "code-wrap";
    pre.parentNode.insertBefore(wrap, pre);
    wrap.appendChild(pre);
    const btn = document.createElement("button");
    btn.className = "code-copy";
    btn.textContent = "Copy";
    btn.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(pre.innerText.trim());
        btn.textContent = "Copied ✓";
        setTimeout(() => { btn.textContent = "Copy"; }, 1300);
      } catch (_) {}
    });
    wrap.appendChild(btn);
  });
})();
