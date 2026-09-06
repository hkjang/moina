// The Flow marks its cards `content-visibility: auto` so a long feed does not
// pay style and layout for what nobody is looking at. Chromium honours that in
// a fullPage screenshot too: everything below the viewport is reserved by
// contain-intrinsic-size but never painted, so the capture comes out blank
// past the fold.
//
// A screenshot is a documentation artefact, not a measurement of what a reader
// perceives, so the capture tools turn the optimisation off for themselves and
// leave it on for real browsers.
export const paintEveryCardCSS =
  '.feed-list > .moin-card { content-visibility: visible !important; contain-intrinsic-size: auto !important; }';

export async function paintEveryCard(context) {
  await context.addInitScript((css) => {
    const apply = () => {
      const style = document.createElement('style');
      style.dataset.e2eRenderMode = 'paint-every-card';
      style.textContent = css;
      document.head.append(style);
    };
    if (document.head) apply();
    else document.addEventListener('DOMContentLoaded', apply, { once: true });
  }, paintEveryCardCSS);
}
