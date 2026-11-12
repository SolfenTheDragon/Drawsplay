const { test } = require("node:test");
const assert = require("node:assert/strict");
const { solvePenCalibration } = require("./webui/calibration.js");

const targets = [[0.2, 0.2], [0.8, 0.2], [0.8, 0.8], [0.2, 0.8]];
const identity = { scaleX: 1, scaleY: 1, offsetX: 0, offsetY: 0 };
const samplesFor = transform => targets.map(([x, y]) => ({
  targetX: x, targetY: y, observedX: x * transform.scaleX + transform.offsetX,
  observedY: y * transform.scaleY + transform.offsetY,
}));

test("target fitting composes with the saved calibration", () => {
  const saved = { scaleX: 1.1, scaleY: 0.9, offsetX: 0.02, offsetY: -0.01 };
  const fitted = solvePenCalibration(samplesFor(saved), saved);
  for (const key of Object.keys(identity)) assert.ok(Math.abs(fitted[key] - identity[key]) < 1e-10);
});

test("target fitting corrects scale and offset on both axes", () => {
  const fitted = solvePenCalibration(samplesFor({ scaleX: 0.9, scaleY: 1.2, offsetX: 0.04, offsetY: -0.05 }), identity);
  assert.ok(Math.abs(fitted.scaleX - 1 / 0.9) < 1e-10);
  assert.ok(Math.abs(fitted.offsetY - 0.05 / 1.2) < 1e-10);
});

test("target fitting rejects incomplete, clustered, inconsistent and unsafe taps", () => {
  assert.throws(() => solvePenCalibration([], identity), /four targets/);
  assert.throws(() => solvePenCalibration(samplesFor({ scaleX: 0, scaleY: 0, offsetX: 0.5, offsetY: 0.5 }), identity), /too close/);
  const inconsistent = samplesFor(identity);
  inconsistent[0].observedX = 0.6;
  assert.throws(() => solvePenCalibration(inconsistent, identity), /did not match/);
  assert.throws(() => solvePenCalibration(samplesFor({ ...identity, scaleX: 0.5 }), identity), /safe range/);
  const invalid = samplesFor(identity);
  invalid[0].observedX = NaN;
  assert.throws(() => solvePenCalibration(invalid, identity), /Invalid/);
});
