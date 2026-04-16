# Generated Documentation

**Purpose**: Auto-generated documentation that should NOT be manually edited.

## What Goes Here

- **metrics-catalog.md**: Auto-generated from Prometheus metrics in code
- **package-map.md**: Auto-generated dependency graph
- **api-reference.md**: Auto-generated API documentation
- **metrics-dashboard.html**: Quality metrics dashboard (from agentic/scripts/)

## How to Generate

```bash
# Metrics dashboard (quality score)
./agentic/scripts/measure-all-metrics.sh --html

# Add other generation commands here as needed
# Example: go doc -all > agentic/generated/api-reference.md
```

## Gitignore

Consider adding to `.gitignore` if files are large or regenerated frequently:
```
agentic/generated/metrics-dashboard.html
agentic/generated/package-map.md
```

## When to Regenerate

- **metrics-dashboard.html**: After each documentation update
- **Other generated docs**: As part of release process or CI
