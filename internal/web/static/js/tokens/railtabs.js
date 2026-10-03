/* semiplane — the /play utilities rail: D16's default tab, and the tabs
 * keyboard model.
 *
 * # What D16 decides, and why it happens here
 *
 * "The default rail tab is `Initiative` on laptop and `Tokens` on TV and phone,
 * remembered per campaign in `localStorage`, applied client-side from `data-ui`
 * so the document never varies by device."
 *
 * The last clause is the whole reason this file exists. A server that chose the
 * tab from a tier would produce a document that varies by tier, which is §13.5
 * and ADR 0035's prohibition — and the tier itself is resolved in the browser
 * (§3.7), so the server cannot see it in any case.
 *
 * So the document carries the **rule** — `data-tab-defaults`, a map from tab id
 * to the tiers at which that tab is the default — and this file carries no tier
 * names at all. It reads `data-ui`, looks the tier up in the document's own
 * table, and selects the tab the table names. A tier the table does not name
 * selects nothing, which leaves the server's selection alone.
 *
 * # Storage is untrusted input, and validated as membership
 *
 * `localStorage` is per-origin shared, per-campaign *keyed*, and writable by
 * anything that has run on the origin. Three rules follow and all three are in
 * `readRemembered` / `remember`:
 *
 *   - **Membership, not a lookup.** The stored string is compared against the
 *     `data-tab` values of the tabs this document actually rendered. A stored
 *     value that matches nothing is discarded, so no input from storage reaches
 *     anything but a comparison.
 *   - **Never markup.** Nothing here writes `innerHTML`, `insertAdjacentHTML`,
 *     `outerHTML`, `document.write` or a `new Function`. A selector is never
 *     assembled from a stored value; the elements are found first and the value
 *     is compared afterwards.
 *   - **Silent on every failure.** Storage throws in private windows and when a
 *     site's data is full, `setItem` throws on a quota, and both are ordinary.
 *     A reader with storage disabled gets the tier default and nothing else, so
 *     every access is in a `try`.
 *
 * # The tabs keyboard model, which the document already implies
 *
 * `ui.Tabs` renders roving `tabindex` — `0` on the selected tab, `-1` on the
 * others — so a tablist is **one** tab stop and the unselected tabs are *not* in
 * the tab order. That is correct only if something moves focus between them, so
 * this file does: `ArrowLeft`/`ArrowRight` move and activate, `Home`/`End` go to
 * the ends, and `ArrowUp`/`ArrowDown` do nothing here (§7.5 gives arrows to
 * within the widget, and a vertical move in a horizontal tablist is a surprise).
 *
 * Automatic activation — moving to a tab selects it — is the WAI-ARIA tabs
 * pattern's default, and it is the right one for four panels with no input in
 * them. `Manual` activation would need a second key press to do the same thing.
 */
(function () {
  "use strict";

  /* The storage key's prefix. Per campaign: the key is this plus the slug, and
   * the slug is the document's own `data-campaign` rather than anything read out
   * of a URL, so a reader with two tables open does not share a preference. */
  var KEY = "sp_rail_tab:";

  function railOf(document) {
    return document.querySelector("[data-rail-tabs]");
  }

  /* rule reads the document's own tier table, and returns nothing when it is
   * unreadable. A missing or malformed table is a page with no defaults, which
   * is the state the document already renders correctly in. */
  function rule(rail) {
    var raw = rail.getAttribute("data-tab-defaults");

    if (!raw) {
      return null;
    }

    try {
      var parsed = JSON.parse(raw);

      return parsed && typeof parsed === "object" ? parsed : null;
    } catch (error) {
      return null;
    }
  }

  /* tier reads the resolver's answer. §3.7 writes it on the document element
   * before the first paint, and it is absent when the script has not run — which
   * is the fourth case D16 does not name, and the answer is to change nothing. */
  function tier() {
    return document.documentElement.dataset.ui || "";
  }

  /* tabsIn returns the rendered tab buttons, in document order. Arrow traversal
   * is this order, and `ui.Tabs` renders it from the route's order. */
  function tabsIn(rail) {
    return Array.prototype.slice.call(rail.querySelectorAll("[data-tab]"));
  }

  /* tabId reads a tab's identity the way `ui.Tabs` wrote it. */
  function tabId(tab) {
    return tab.getAttribute("data-tab") || "";
  }

  /* defaultFor answers which tab the rule makes default at this tier, or "".
   *
   * `Object.keys` rather than a search over the values, so a tier named by two
   * tabs resolves to the first in the document's own order — and the Go test
   * asserts no tier is named twice, which is what makes that determinate. */
  function defaultFor(table, at) {
    if (!table || !at) {
      return "";
    }

    var ids = Object.keys(table);

    for (var i = 0; i < ids.length; i++) {
      var tiers = table[ids[i]];

      if (Object.prototype.toString.call(tiers) !== "[object Array]") {
        continue;
      }

      for (var j = 0; j < tiers.length; j++) {
        if (tiers[j] === at) {
          return ids[i];
        }
      }
    }

    return "";
  }

  /* panelFor returns the panel belonging to a tab id, by walking the document's
   * own panels and comparing their `data-tabpanel` values.
   *
   * Not a selector built from the value. The rule this file states about
   * `localStorage` is "a selector is never assembled from a stored value", and
   * holding it in one place while breaking it two functions later is exactly how
   * the rule stops being true. Walking four elements is not a cost worth a
   * second rule to remember. */
  function panelFor(rail, value) {
    var panels = rail.querySelectorAll("[data-tabpanel]");

    for (var i = 0; i < panels.length; i++) {
      if ((panels[i].getAttribute("data-tabpanel") || "") === value) {
        return panels[i];
      }
    }

    return null;
  }

  /* selectTab moves the widget: one `aria-selected="true"`, one `tabindex="0"`,
   * and the panels' `hidden` following.
   *
   * `hidden` rather than `aria-hidden` because `ui.Tabs` chose it for the reason
   * `components/ui/tabs.templ` documents: a panel full of controls must leave
   * the accessibility tree *and* the tab order together, and §7.10 prohibits
   * `aria-hidden` on a focus stop. */
  function selectTab(rail, value) {
    tabsIn(rail).forEach(function (tab) {
      var mine = tabId(tab) === value;

      tab.setAttribute("aria-selected", mine ? "true" : "false");
      tab.setAttribute("tabindex", mine ? "0" : "-1");

      var panel = panelFor(rail, tabId(tab));

      if (panel) {
        panel.hidden = !mine;
      }
    });
  }

  /* readRemembered returns the stored tab id for this campaign, or "".
   *
   * **Membership is checked against the rendered tabs**, which is the whole
   * validation: a value that is not one of this document's `data-tab` values
   * cannot name anything, so it cannot become a selector, an id or a label. The
   * length bound is belt-and-braces against a megabyte of somebody else's data
   * sitting in this origin's storage. */
  function readRemembered(rail) {
    var campaign = rail.getAttribute("data-campaign") || "";

    if (campaign === "") {
      return "";
    }

    var stored = null;

    try {
      stored = window.localStorage.getItem(KEY + campaign);
    } catch (error) {
      return "";
    }

    if (typeof stored !== "string" || stored === "" || stored.length > 128) {
      return "";
    }

    var tabs = tabsIn(rail);

    for (var i = 0; i < tabs.length; i++) {
      if (tabId(tabs[i]) === stored) {
        return stored;
      }
    }

    return "";
  }

  function remember(rail, value) {
    var campaign = rail.getAttribute("data-campaign") || "";

    if (campaign === "" || value === "") {
      return;
    }

    try {
      window.localStorage.setItem(KEY + campaign, value);
    } catch (error) {
      /* A quota or a private window. The preference is a convenience, so this
       * is not worth reporting and not worth the reader's attention. */
    }
  }

  /* stepTab moves `from` by `delta` rows in the tablist, wrapping, and selects
   * what it lands on. Wrapping is §7.5's rule for arrows within a widget, and it
   * is the same arithmetic the token list uses, for the same reason: one rule
   * for both, so there is one to check. */
  function stepTab(rail, from, delta) {
    var tabs = tabsIn(rail);

    if (tabs.length === 0) {
      return;
    }

    var next = ((from + delta) % tabs.length + tabs.length) % tabs.length;

    selectTab(rail, tabId(tabs[next]));
    tabs[next].focus();
  }

  function bind(rail) {
    var remembered = readRemembered(rail);

    /* Remembered wins over the tier default, which wins over nothing. A reader
     * who has chosen a tab keeps it on every device they have used it on, which
     * is the half of D16 the tier rule exists to make a *first* visit pleasant
     * rather than a permanent setting. */
    var wanted = remembered || defaultFor(rule(rail), tier());

    if (wanted !== "") {
      selectTab(rail, wanted);
    }

    rail.addEventListener("click", function (event) {
      var tab = event.target.closest("[data-tab]");

      if (!tab || !rail.contains(tab)) {
        return;
      }

      selectTab(rail, tabId(tab));
      remember(rail, tabId(tab));
    });

    rail.addEventListener("keydown", function (event) {
      if (event.altKey || event.ctrlKey || event.metaKey) {
        return;
      }

      var tabs = tabsIn(rail);
      var from = -1;

      for (var i = 0; i < tabs.length; i++) {
        if (tabs[i] === event.target) {
          from = i;
        }
      }

      if (from < 0) {
        return;
      }

      var delta = 0;

      switch (event.key) {
        case "ArrowRight":
          delta = 1;
          break;
        case "ArrowLeft":
          delta = -1;
          break;
        case "Home":
          delta = -from;
          break;
        case "End":
          delta = tabs.length - 1 - from;
          break;
        default:
          return;
      }

      event.preventDefault();
      stepTab(rail, from, delta);
    });
  }

  function start() {
    var rail = railOf(document);

    if (rail) {
      bind(rail);
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();