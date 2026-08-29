const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

const menuButton = $('[data-menu-button]');
const mobileMenu = $('[data-mobile-menu]');
if (menuButton && mobileMenu) {
  const closeMenu = () => {
    mobileMenu.hidden = true;
    menuButton.setAttribute('aria-expanded', 'false');
  };
  menuButton.addEventListener('click', () => {
    const open = mobileMenu.hidden;
    mobileMenu.hidden = !open;
    menuButton.setAttribute('aria-expanded', String(open));
  });
  mobileMenu.addEventListener('click', (event) => { if (event.target.closest('a')) closeMenu(); });
  document.addEventListener('keydown', (event) => { if (event.key === 'Escape') closeMenu(); });
}

$$('[data-current-year]').forEach((node) => { node.textContent = String(new Date().getFullYear()); });

const gallery = $('[data-screenshot-gallery]');
if (gallery) {
  fetch('assets/screenshots/manifest.json', { cache: 'no-store' })
    .then((response) => {
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      return response.json();
    })
    .then((manifest) => {
      if (!Array.isArray(manifest.screenshots) || manifest.screenshots.length === 0) {
        gallery.innerHTML = '<p class="gallery-empty">실제 서비스 화면은 릴리스 검증 후 게시됩니다.</p>';
        return;
      }
      const mode = gallery.dataset.galleryFilter || 'featured';
      const preferred = mode === 'admin'
        ? manifest.screenshots.filter((shot) => shot.slug.includes('-admin-'))
        : mode === 'user'
          ? manifest.screenshots.filter((shot) => !shot.slug.includes('-admin-'))
          : manifest.screenshots.filter((shot) => shot.slug.startsWith('desktop-')).slice(0, 12);
      gallery.innerHTML = preferred.map((shot) => `
        <figure class="screen-card">
          <button type="button" data-lightbox="${shot.path}" aria-label="${shot.title} 크게 보기">
            <img src="${shot.path}" alt="MOINA ${shot.title} 실제 화면" loading="lazy" decoding="async">
          </button>
          <figcaption><strong>${shot.title}</strong><code>${shot.route}</code></figcaption>
        </figure>`).join('');
    })
    .catch(() => { gallery.innerHTML = '<p class="gallery-empty">화면 manifest를 불러오지 못했습니다.</p>'; });
}

const lightbox = $('[data-lightbox-dialog]');
if (lightbox) {
  document.addEventListener('click', (event) => {
    const trigger = event.target.closest('[data-lightbox]');
    if (!trigger) return;
    const image = $('img', lightbox);
    image.src = trigger.dataset.lightbox;
    image.alt = trigger.getAttribute('aria-label') || 'MOINA 화면';
    lightbox.showModal();
  });
  $('[data-lightbox-close]', lightbox)?.addEventListener('click', () => lightbox.close());
  lightbox.addEventListener('click', (event) => { if (event.target === lightbox) lightbox.close(); });
}

const guideSections = $$('[data-guide-section]');
if (guideSections.length && 'IntersectionObserver' in window) {
  const links = new Map($$('a[href^="#"]').map((link) => [link.hash.slice(1), link]));
  const observer = new IntersectionObserver((entries) => {
    const current = entries.filter((entry) => entry.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0];
    if (!current) return;
    links.forEach((link) => link.removeAttribute('aria-current'));
    links.get(current.target.id)?.setAttribute('aria-current', 'location');
  }, { rootMargin: '-15% 0px -70%' });
  guideSections.forEach((section) => observer.observe(section));
}
