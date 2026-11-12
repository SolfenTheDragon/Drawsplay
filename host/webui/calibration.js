"use strict";

// Browser coordinates already include the saved host transform. Compose the
// measured correction with it rather than treating observations as raw input.
function solvePenCalibration(samples, current) {
  if (samples.length < 4) throw new Error("Tap all four targets before continuing.");
  const result = {};
  for (const axis of ["X", "Y"]) {
    const observed = samples.map(point => point[`observed${axis}`]);
    const targets = samples.map(point => point[`target${axis}`]);
    if (![...observed, ...targets].every(Number.isFinite)) {
      throw new Error("Invalid target position. Please try calibration again.");
    }
    const mean = values => values.reduce((sum, value) => sum + value, 0) / values.length;
    const observedMean = mean(observed);
    const targetMean = mean(targets);
    const variance = observed.reduce((sum, value) => sum + (value - observedMean) ** 2, 0);
    if (variance < 0.02) throw new Error("Targets were too close together. Tap each displayed target with your pen.");
    const scale = observed.reduce((sum, value, index) =>
      sum + (value - observedMean) * (targets[index] - targetMean), 0) / variance;
    const offset = targetMean - scale * observedMean;
    if (observed.some((value, index) => Math.abs(scale * value + offset - targets[index]) > 0.06)) {
      throw new Error("The taps did not match the targets. Keep the browser on the tablet display and try again.");
    }
    result[`scale${axis}`] = scale * current[`scale${axis}`];
    result[`offset${axis}`] = scale * current[`offset${axis}`] + offset;
    if (!Number.isFinite(result[`scale${axis}`]) || !Number.isFinite(result[`offset${axis}`]) ||
        result[`scale${axis}`] < 0.5 || result[`scale${axis}`] > 1.5 ||
        Math.abs(result[`offset${axis}`]) > 0.25) {
      throw new Error("This adjustment exceeds the safe range. Check display scaling and repeat on the tablet's screen.");
    }
  }
  return result;
}

if (typeof module !== "undefined") module.exports = { solvePenCalibration };
