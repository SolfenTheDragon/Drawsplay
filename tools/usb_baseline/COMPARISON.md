# Whole-run USB resource comparisons

`compare.py` consumes collector directories without contacting the host,
tablet or network. It verifies raw sample counts, status/cadence validity,
resource summaries, device/settings compatibility and fixed artifact hashes
within each group. Candidate artifacts may differ from baseline artifacts;
their change is reported. It never combines process generations' partial
intervals into a host CPU total.

Pair each baseline directory with its corresponding candidate directory in
the supplied order. Use distinct independently collected runs, fixed
controlled content/brightness/thermal conditions and randomized A/B order.
Neither timestamps nor this tool can prove experimental independence.
Do not duplicate directory evidence to inflate run counts.

```sh
python3 tools/usb_baseline/compare.py \
  --baseline /local/run-a1 /local/run-a2 \
  --candidate /local/run-b1 /local/run-b2
```

There are six increasing-is-adverse resource metrics: host process-tree CPU,
tablet CPU, tablet PSS/RSS, sampled decode delay and sampled queue depth.
Each contributes **one mean per run**, not one observation per frame or
two-second interval. Host process CPU means may only be added when all
recorded generations cover equal interval counts. Unknown memory/CPU/decode
fields, nonfinite data, invalid windows, unequal settings/device and changing
artifact sets are rejected. Schema/coverage differences need fresh runs,
not fabricated zero defaults.

With fewer than 10 matched pairs, output is `INSUFFICIENT_RUNS` with
descriptive deltas and no p-values. At 10 or more pairs, each metric uses an
exact one-sided paired sign test (zero deltas excluded), then Holm correction
across all six metrics at familywise alpha 0.05. The sign test is conservative
and tests shift direction, not a confidence bound on effect magnitude; it
does not exploit correlated individual samples as artificial replication.
An adverse adjusted result gives `REGRESSION`. Otherwise the result is
`INCONCLUSIVE`, **never PASS**: failure to find significance is not evidence
of equivalence.

Exit 1 means incompatible/incomplete evidence or input error. Exit 2 means
results produced, with overall acceptance still `BLOCKED`. JSON goes to
stdout; redirect to a new local file when saving. The comparator doesn't
write or overwrite the source runs.

This comparison does not establish approved sensitivity/equivalence bounds,
actual input RTT, complete frame/report coverage, power, startup, memory
leak behavior or cross-platform behavior. Those cannot be silently inferred
from CPU/RSS/decode proxies. Camera/physical-interaction checks remain
waived; no code in this tool demands them.
