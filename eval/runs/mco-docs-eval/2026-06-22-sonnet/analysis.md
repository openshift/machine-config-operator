---
agent: Claude Code
model: claude-opus-4-6
date: 2026-06-22T17:54:00Z
---

## Recommendation

**Fix the eval config and test data before drawing conclusions about documentation quality.** Three config/data issues prevented most judges from running, masking the actual evaluation:

1. **Raise budget_check threshold** to $15.00 (current $2.00 is per-run, not per-case; the run cost $11.14)
2. **Move expected fields from input.yaml to annotations.yaml** — `expected_mentions`, `expected_files`, `category`, `rule`, `expected_guidance`, `expected_patterns` must be in annotations.yaml for judges to access them via `outputs["annotations"]`
3. **Fix `response_accuracy` and `anti_pattern_rejection` `if` conditions** — annotations are being parsed as strings rather than dicts in some cases, causing `'str object' has no attribute 'get'`

After these fixes, re-run: `/eval-run --model claude-sonnet-4-6 --run-id 2026-06-22-sonnet-v2`

## Summary

| Metric | Value |
|--------|-------|
| Model | claude-sonnet-4-6 |
| Cases | 16 (15 OK, 1 timeout) |
| Duration | 46:48 wall clock (2807s case time) |
| Total cost | $11.14 |
| Cost/case | $0.74 avg |
| Cost/turn | $0.030 |
| Cache hit rate | 94.1% |
| Turns | 371 total |

**Judge results:**
| Judge | Pass Rate | Notes |
|-------|-----------|-------|
| budget_check | 0% | Threshold too low ($2.00 for $11.14 run) |
| consulted_docs | 100% | Passed but vacuously (no expected_files in annotations) |
| has_response | 100% | All cases produced responses (135-12,569 chars) |
| mentions_expected_keywords | N/A | Skipped — expected_mentions not in annotations.yaml |
| response_accuracy | N/A | Error — annotations parsed as string, not dict |
| anti_pattern_rejection | N/A | Error — same annotations parsing issue |
| authoring_quality | N/A | Error — same annotations parsing issue |

## Failure Patterns

### 1. Annotations data structure mismatch (all LLM judges)
The taxonomy generator put `category`, `expected_mentions`, `expected_files`, etc. in `input.yaml`. The judges expect these in `annotations.yaml`. The `annotations.yaml` files only contain top-level keys like `category: navigation`, `difficulty: easy` — but these are being parsed as strings when the `if` condition uses `.get()`.

**Impact:** 5 of 7 judges produced no meaningful scores.

### 2. Budget threshold miscalibrated
The `cost_budget` builtin checks total run cost against the threshold. At $11.14 total, the $2.00 threshold fails every case.

### 3. Case-013 execution timeout
Case-013 (anti-pattern: unsupported Ignition sections for users/filesystems) timed out at 300s with 91 turns but no cost recorded. The agent may have entered a loop exploring code without converging.

## Root Causes

1. **Config issue (not model issue)**: The annotations.yaml schema in the generated test cases doesn't match what the judges expect. The taxonomy generator put metadata fields in both input.yaml and annotations.yaml but used different field names.

2. **Budget scope**: The `cost_budget` builtin operates on run-level cost, but the threshold was set thinking per-case.

3. **Timeout**: 300s is adequate for most cases (avg 176s) but case-013 was complex enough to exceed it. Consider raising to 600s for thorny anti-pattern cases.

## Cost Attribution

- Total: $11.14 for 16 cases
- Cost/case: $0.74 avg ($0.31-$1.24 range)
- Cost/turn: $0.030
- Cost/million output tokens: $0.41
- Cache hit rate: 94.1% (excellent — Sonnet benefits from repeated codebase reads)
- Most expensive case: case-014 ($1.24, 248s, 40 turns — complex anti-pattern about merge ordering)
- Cheapest case: case-011 ($0.31, 122s, 12 turns — simple anti-pattern about pool selector)

The cost profile is reasonable for documentation evaluation — most cost is in cache reads (~$0.01/Mtok) with modest output generation.
