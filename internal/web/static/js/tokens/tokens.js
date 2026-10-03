/* semiplane token list — the keyboard model over ordinary buttons.
 *
 * What this adds, and only this: `ArrowUp`/`ArrowDown`/`Home`/`End` move focus
 * between rows, and activating a row marks it selected.
 *
 * What it deliberately does not do is take the rows out of the tab order. The
 * rows are real `<button type="button">` elements with no `tabindex` (UI §7.6), so
 * `Tab` already reaches every one of them in order and `Enter` already activates
 * one — which is why "operable by keyboard alone" survives this file being
 * blocked, and why this is not a roving-tabindex listbox. The arrows are an
 * addition on top of a list that already works, not the thing making it work.
 *
 * # The keys are data, and the data is the tested thing
 *
 * The two tables below are the entire key model. `tokens.js` contains no key
 * *names* beyond the two tables, so the names exist exactly once and Go can read
 * them: `step_keys_test.go` parses both tables out of this file and evaluates
 * them against `step.js`'s arithmetic over every row count and every starting
 * row. A key that moves the wrong way, or an edge key that lands on the wrong
 * end, fails there rather than in a browser.
 *
 * # Never `innerHTML`, and never a key built from a string
 *
 * Nothing in this file writes markup or reads an attribute into a selector. The
 * only values it acts on are `event.key`, which the browser produces, and
 * `data-placement`, which it reads off an element it already has. A list of
 * placements is campaign content; a client that assembled a selector from one
 * would be putting untrusted input into a query.
 *
 * # The seam for the canvas
 *
 * Selecting a row dispatches `semiplane:token-select` on the list, with the
 * placement id in `detail`. UI §7.6 says "selection mirrors to the canvas", and a
 * DOM event is the whole of what this file can honestly offer: whether anything
 * is listening, and what the table's transport turns it into, belongs to the
 * canvas and the realtime plane. Nothing here opens a socket.
 */
(function () {
  "use strict";

  /* Keys that move relatively, by a step that wraps. */
  var spTokenKeys = { ArrowUp: -1, ArrowDown: 1 };

  /* Keys that jump to an absolute row. A negative index means `count + index`,
   * so one number answers both ends and the wrap rule above stays the only
   * arithmetic in the file. */
  var spTokenEdges = { Home: 0, End: -1 };

  /* One constant for the event, so a listener and a dispatcher cannot name two
   * different things. */
  var SELECT = "semiplane:token-select";

  /* Rows are located by the attribute the template writes. The table itself is
   * found by the attribute `tokens.templ` puts on the `<ul>`. */
  function rowsOf(list) {
    return list.querySelectorAll("[data-token]");
  }

  /* destination answers which row a key selects, or -1 for a key this list does
   * not handle.
   *
   * `count` is the number of rows and is **positive**: `bind` returns before
   * anything calls this when the list is empty, so `step.js`'s modulo is never a
   * division by zero. That guard is here rather than in `step.js` because a guard
   * is a conditional and `step.js` may not contain one.
   */
  function destination(key, from, count) {
    var edge = spTokenEdges[key];

    if (edge !== undefined) {
      return edge < 0 ? count + edge : edge;
    }

    var move = spTokenKeys[key];

    if (move === undefined) {
      return -1;
    }

    return spFocusStep(from, move, count);
  }

  /* select marks one row selected and every other row not, then says so.
   *
   * `aria-pressed` is what the template already writes on every row, so selection
   * is one attribute the client maintains rather than a class, and a reader
   * hears the selection rather than seeing it.
   */
  function select(list, row) {
    rowsOf(list).forEach(function (other) {
      other.setAttribute("aria-pressed", other === row ? "true" : "false");
    });

    list.dispatchEvent(
      new CustomEvent(SELECT, {
        bubbles: true,
        detail: { placement: row.getAttribute("data-placement") },
      }),
    );
  }

  /* bind wires one list, and is a no-op on a list with no rows. */
  function bind(list) {
    var rows = rowsOf(list);

    if (rows.length === 0) {
      return;
    }

    list.addEventListener("keydown", function (event) {
      if (event.altKey || event.ctrlKey || event.metaKey) {
        return;
      }

      var from = -1;

      rows.forEach(function (row, at) {
        if (row === event.target) {
          from = at;
        }
      });

      if (from < 0) {
        return;
      }

      var next = destination(event.key, from, rows.length);

      if (next < 0) {
        /* Not our key. Leaving the default alone is what keeps `Tab` working and
         * what stops the arrow keys from stealing page scrolling on a list that
         * has no rows left to move to. */
        return;
      }

      /* Only after a destination is known: `preventDefault` on an unhandled key
       * would stop the page scrolling for a reader who pressed an arrow key
       * expecting it to. */
      event.preventDefault();
      rows[next].focus();
    });

    list.addEventListener("click", function (event) {
      var row = event.target.closest("[data-token]");

      if (row && list.contains(row)) {
        select(list, row);
      }
    });
  }

  function start() {
    document.querySelectorAll("[data-token-list]").forEach(bind);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }

  /* Exported for the one thing a second file would need, and for nothing else:
   * the key tables, so a reader can see them without running anything. */
  if (typeof window !== "undefined") {
    window.spTokenKeys = spTokenKeys;
    window.spTokenEdges = spTokenEdges;
  }
})();