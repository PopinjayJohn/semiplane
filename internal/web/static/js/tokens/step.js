/* semiplane token list — the focus movement rule, as arithmetic.
 *
 * This file exists to be *checked*, not for its size. The rule the arrow keys
 * follow is the whole of it:
 *
 *     the next row is `from` moved by `delta`, wrapped into `0..count-1`
 *
 * `internal/web/static/js/tokens/step_arith_test.go` parses this file and
 * evaluates the shipped bytes over every combination of `from`, `delta` and
 * `count`. No transcription, no second implementation to drift, and no Node on
 * the CI runner.
 *
 * That is only true because of what the file is *not*. There is no `if`, no
 * loop, no string, no `document`, no call and no assignment: one function, one
 * `return`, one expression over three numbers. Anything else is a **parse error**
 * and the build goes red, which is the design property — "unsupported" means
 * "fails", never "passes unexamined". There is no evaluator-invented name to
 * shadow a host-language one either, because the evaluator binds only these three
 * parameters and knows no functions at all.
 *
 * `count` is positive at every call site: `tokens.js` returns before reaching
 * this when the list has no rows, so the modulo is never a division by zero and
 * the rule has no `count <= 0` branch to express. See `tokens.js`'s own comment.
 */
var spFocusStep = function spFocusStep(from, delta, count) {
  return ((from + delta) % count + count) % count;
};