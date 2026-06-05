// Subtitler docs — lightweight progressive enhancement.
(() => {
  "use strict";

  // --- Mobile nav toggle ---------------------------------------------------
  const toggle = document.querySelector(".nav-toggle");
  const nav = document.querySelector(".nav");
  if (toggle && nav) {
    toggle.addEventListener("click", () => {
      const open = nav.classList.toggle("open");
      toggle.setAttribute("aria-expanded", String(open));
    });
    nav.addEventListener("click", (e) => {
      if (e.target.closest("a")) nav.classList.remove("open");
    });
  }

  // --- Copy buttons on code blocks ----------------------------------------
  document.querySelectorAll(".code-copy").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const block = btn.closest(".code");
      const code = block && block.querySelector("pre");
      if (!code) return;
      try {
        await navigator.clipboard.writeText(code.innerText.trim());
        const label = btn.querySelector(".copy-label");
        const prev = label ? label.textContent : "";
        btn.classList.add("copied");
        if (label) label.textContent = "Copied";
        setTimeout(() => {
          btn.classList.remove("copied");
          if (label) label.textContent = prev;
        }, 1600);
      } catch (_) {
        /* clipboard unavailable — no-op */
      }
    });
  });

  // --- Reveal on scroll ----------------------------------------------------
  const reveals = document.querySelectorAll("[data-reveal]");
  if (reveals.length && "IntersectionObserver" in window) {
    const io = new IntersectionObserver(
      (entries) => {
        entries.forEach((entry) => {
          if (entry.isIntersecting) {
            entry.target.classList.add("in");
            io.unobserve(entry.target);
          }
        });
      },
      { rootMargin: "0px 0px -10% 0px", threshold: 0.05 }
    );
    reveals.forEach((el) => io.observe(el));
  } else {
    reveals.forEach((el) => el.classList.add("in"));
  }

  // --- Table-of-contents scrollspy ----------------------------------------
  const tocLinks = Array.from(document.querySelectorAll(".toc a[href^='#']"));
  if (tocLinks.length && "IntersectionObserver" in window) {
    const map = new Map();
    tocLinks.forEach((a) => {
      const id = a.getAttribute("href").slice(1);
      const target = document.getElementById(id);
      if (target) map.set(target, a);
    });
    const spy = new IntersectionObserver(
      (entries) => {
        entries.forEach((entry) => {
          if (entry.isIntersecting) {
            tocLinks.forEach((a) => a.classList.remove("active"));
            const link = map.get(entry.target);
            if (link) link.classList.add("active");
          }
        });
      },
      { rootMargin: "-20% 0px -70% 0px", threshold: 0 }
    );
    map.forEach((_, target) => spy.observe(target));
  }
})();
