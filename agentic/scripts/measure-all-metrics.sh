#!/bin/bash
# Comprehensive agentic documentation metrics dashboard
#
# Measures:
# 1. Navigation depth (link graph analysis)
# 2. Context budget (typical workflows)
# 3. Structure compliance (validation)
# 4. Quality score calculation
#
# Usage:
#   ./scripts/measure-all-metrics.sh                    # Display metrics only
#   ./scripts/measure-all-metrics.sh --generate-reports # Save to files

# Sanity check: detect if being run with wrong interpreter
if [ -z "$BASH_VERSION" ]; then
    echo "❌ ERROR: This is a Bash script, not a Python script"
    echo ""
    echo "You tried to run:"
    echo "  python3 measure-all-metrics.sh  ❌ WRONG"
    echo ""
    echo "Correct usage:"
    echo "  ./agentic/scripts/measure-all-metrics.sh  ✅ CORRECT"
    echo "  bash agentic/scripts/measure-all-metrics.sh  ✅ CORRECT"
    echo ""
    echo "File types:"
    echo "  .sh files = Bash scripts (use ./ or bash)"
    echo "  .py files = Python scripts (use python3)"
    exit 1
fi

set -e

# Find repo root
REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$REPO_ROOT"

# Script directory (relative to repo root)
SCRIPT_DIR="agentic/scripts"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

GENERATE_REPORTS=false
GENERATE_HTML=false

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --generate-reports)
      GENERATE_REPORTS=true
      shift
      ;;
    --html)
      GENERATE_HTML=true
      shift
      ;;
    --update-quality-score)
      # Backward compatibility - deprecated
      echo -e "${YELLOW}⚠️  --update-quality-score is deprecated, use --generate-reports${NC}"
      GENERATE_REPORTS=true
      shift
      ;;
    -h|--help)
      echo "Usage: $0 [OPTIONS]"
      echo ""
      echo "Measures agentic documentation quality metrics."
      echo ""
      echo "Options:"
      echo "  --generate-reports    Generate METRICS_REPORT.md and update QUALITY_SCORE.md"
      echo "  --html               Generate HTML dashboard (agentic/metrics-dashboard.html)"
      echo "  -h, --help           Show this help message"
      exit 0
      ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: $0 [--generate-reports] [--html]"
      exit 1
      ;;
  esac
done

echo -e "${BLUE}╔════════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║        AGENTIC DOCUMENTATION METRICS DASHBOARD                     ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Check if Python 3 is available
if ! command -v python3 &> /dev/null; then
    echo -e "${RED}❌ Python 3 is required but not found${NC}"
    exit 1
fi

# Metric 1: Navigation Depth
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}1. NAVIGATION DEPTH ANALYSIS${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

if [ -f "$SCRIPT_DIR/measure-navigation-depth.py" ]; then
    NAV_OUTPUT=$(python3 $SCRIPT_DIR/measure-navigation-depth.py --max-depth 3 2>&1)
    echo "$NAV_OUTPUT"

    # Parse output for PASSED/FAILED
    if echo "$NAV_OUTPUT" | grep -q "✅ PASSED"; then
        NAVIGATION_STATUS="✅ PASSED"
        NAVIGATION_SCORE=100
    elif echo "$NAV_OUTPUT" | grep -q "❌ FAILED"; then
        NAVIGATION_STATUS="❌ FAILED"
        NAVIGATION_SCORE=50
    else
        NAVIGATION_STATUS="⚠️  UNKNOWN"
        NAVIGATION_SCORE=0
    fi
else
    echo -e "${YELLOW}⚠️  Navigation depth script not found${NC}"
    NAVIGATION_STATUS="⚠️  SKIPPED"
    NAVIGATION_SCORE=0
fi

# Metric 2: Context Budget
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}2. CONTEXT BUDGET ANALYSIS${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

if [ -f "$SCRIPT_DIR/measure-context-budget.py" ]; then
    BUDGET_OUTPUT=$(python3 $SCRIPT_DIR/measure-context-budget.py --max-budget 700 2>&1)
    echo "$BUDGET_OUTPUT"
    echo ""

    # Parse output for PASSED/FAILED
    if echo "$BUDGET_OUTPUT" | grep -q "✅ PASSED"; then
        BUDGET_STATUS="✅ PASSED"
        BUDGET_SCORE=100
    elif echo "$BUDGET_OUTPUT" | grep -q "❌ FAILED"; then
        BUDGET_STATUS="❌ FAILED"
        BUDGET_SCORE=75
    else
        BUDGET_STATUS="⚠️  UNKNOWN"
        BUDGET_SCORE=0
    fi
else
    echo -e "${YELLOW}⚠️  Context budget script not found${NC}"
    BUDGET_STATUS="⚠️  SKIPPED"
    BUDGET_SCORE=0
fi

# Metric 3: Structure Validation
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}3. STRUCTURE VALIDATION${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

STRUCTURE_SCORE=0
STRUCTURE_CHECKS=0
STRUCTURE_PASSED=0

# Check AGENTS.md length
if [ -f "AGENTS.md" ]; then
    STRUCTURE_CHECKS=$((STRUCTURE_CHECKS + 1))
    AGENTS_LINES=$(wc -l < AGENTS.md)
    if [ $AGENTS_LINES -le 150 ]; then
        echo -e "${GREEN}✅ AGENTS.md length OK ($AGENTS_LINES/150 lines)${NC}"
        STRUCTURE_PASSED=$((STRUCTURE_PASSED + 1))
    else
        echo -e "${RED}❌ AGENTS.md too long ($AGENTS_LINES/150 lines)${NC}"
    fi
fi

# Check required directories
REQUIRED_DIRS="agentic/design-docs agentic/domain agentic/exec-plans agentic/decisions"
for dir in $REQUIRED_DIRS; do
    STRUCTURE_CHECKS=$((STRUCTURE_CHECKS + 1))
    if [ -d "$dir" ]; then
        STRUCTURE_PASSED=$((STRUCTURE_PASSED + 1))
    else
        echo -e "${RED}❌ Missing directory: $dir${NC}"
    fi
done

# Calculate structure score
if [ $STRUCTURE_CHECKS -gt 0 ]; then
    STRUCTURE_SCORE=$(( STRUCTURE_PASSED * 100 / STRUCTURE_CHECKS ))
    if [ $STRUCTURE_SCORE -eq 100 ]; then
        STRUCTURE_STATUS="✅ PASSED"
    elif [ $STRUCTURE_SCORE -ge 80 ]; then
        STRUCTURE_STATUS="⚠️  PARTIAL"
    else
        STRUCTURE_STATUS="❌ FAILED"
    fi
else
    STRUCTURE_STATUS="⚠️  SKIPPED"
fi

# Metric 4: Documentation Coverage
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}4. DOCUMENTATION COVERAGE${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

# Count ADRs
ADR_COUNT=$(find agentic/decisions -name "adr-*.md" -not -name "*template*" 2>/dev/null | wc -l)
echo -e "  ADRs documented: $ADR_COUNT"

# Count concept docs
CONCEPT_COUNT=$(find agentic/domain/concepts -name "*.md" 2>/dev/null | wc -l)
echo -e "  Domain concepts: $CONCEPT_COUNT"

# Count exec plans
ACTIVE_PLANS=$(find agentic/exec-plans/active -name "*.md" -not -name "template*" 2>/dev/null | wc -l)
COMPLETED_PLANS=$(find agentic/exec-plans/completed -name "*.md" 2>/dev/null | wc -l)
echo -e "  Execution plans: $ACTIVE_PLANS active, $COMPLETED_PLANS completed"

# Calculate coverage score
COVERAGE_SCORE=0
if [ $ADR_COUNT -ge 3 ]; then COVERAGE_SCORE=$((COVERAGE_SCORE + 40)); fi
if [ $CONCEPT_COUNT -ge 2 ]; then COVERAGE_SCORE=$((COVERAGE_SCORE + 30)); fi
if [ $((ACTIVE_PLANS + COMPLETED_PLANS)) -ge 1 ]; then COVERAGE_SCORE=$((COVERAGE_SCORE + 30)); fi

if [ $COVERAGE_SCORE -ge 80 ]; then
    COVERAGE_STATUS="✅ GOOD"
elif [ $COVERAGE_SCORE -ge 50 ]; then
    COVERAGE_STATUS="⚠️  FAIR"
else
    COVERAGE_STATUS="❌ POOR"
fi

echo -e "  Coverage score: $COVERAGE_SCORE/100 $COVERAGE_STATUS"

# Overall Summary
echo ""
echo -e "${BLUE}╔════════════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║                        OVERALL SUMMARY                             ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════════════╝${NC}"
echo ""

printf "  %-30s %10s %10s\n" "Metric" "Score" "Status"
echo "  ────────────────────────────────────────────────────────────────────"
printf "  %-30s %10s %10s\n" "Navigation Depth" "$NAVIGATION_SCORE/100" "$NAVIGATION_STATUS"
printf "  %-30s %10s %10s\n" "Context Budget" "$BUDGET_SCORE/100" "$BUDGET_STATUS"
printf "  %-30s %10s %10s\n" "Structure Compliance" "$STRUCTURE_SCORE/100" "$STRUCTURE_STATUS"
printf "  %-30s %10s %10s\n" "Documentation Coverage" "$COVERAGE_SCORE/100" "$COVERAGE_STATUS"
echo "  ────────────────────────────────────────────────────────────────────"

# Calculate overall score
TOTAL_SCORE=$(( (NAVIGATION_SCORE + BUDGET_SCORE + STRUCTURE_SCORE + COVERAGE_SCORE) / 4 ))
printf "  %-30s %10s\n" "OVERALL QUALITY SCORE" "$TOTAL_SCORE/100"

echo ""

if [ $TOTAL_SCORE -ge 80 ]; then
    echo -e "${GREEN}✅ EXCELLENT - Documentation is in great shape${NC}"
    EXIT_CODE=0
elif [ $TOTAL_SCORE -ge 60 ]; then
    echo -e "${YELLOW}⚠️  GOOD - Some improvements recommended${NC}"
    EXIT_CODE=0
elif [ $TOTAL_SCORE -ge 40 ]; then
    echo -e "${YELLOW}⚠️  FAIR - Significant improvements needed${NC}"
    EXIT_CODE=1
else
    echo -e "${RED}❌ POOR - Documentation needs major work${NC}"
    EXIT_CODE=1
fi

echo ""

# Generate report files if requested
if [ "$GENERATE_REPORTS" = true ]; then
    echo -e "${BLUE}Updating agentic/METRICS_REPORT.md...${NC}"

    cat > agentic/METRICS_REPORT.md <<EOF
# Documentation Quality Score

> **Last Updated**: $(date +"%Y-%m-%d %H:%M:%S")
> **Overall Score**: $TOTAL_SCORE/100

## Summary

| Metric | Score | Status |
|--------|-------|--------|
| Navigation Depth | $NAVIGATION_SCORE/100 | $NAVIGATION_STATUS |
| Context Budget | $BUDGET_SCORE/100 | $BUDGET_STATUS |
| Structure Compliance | $STRUCTURE_SCORE/100 | $STRUCTURE_STATUS |
| Documentation Coverage | $COVERAGE_SCORE/100 | $COVERAGE_STATUS |
| **OVERALL** | **$TOTAL_SCORE/100** | |

## Metrics Explained

### Navigation Depth ($NAVIGATION_SCORE/100)

Measures how many "hops" (link clicks) are required to reach any documentation from AGENTS.md.

- **Target**: All docs reachable in ≤3 hops
- **Why**: Keeps context loading efficient, prevents "lost in navigation"

### Context Budget ($BUDGET_SCORE/100)

Measures total documentation lines loaded for typical agent workflows.

- **Target**: ≤700 lines for feature implementation
- **Why**: Prevents context window overflow, improves agent performance

### Structure Compliance ($STRUCTURE_SCORE/100)

Validates required directory structure and files exist.

- **Target**: 100% compliance
- **Why**: Ensures consistent structure across repositories

### Documentation Coverage ($COVERAGE_SCORE/100)

Measures completeness of documentation.

- **Metrics**:
  - ADRs: $ADR_COUNT (target: ≥3)
  - Concepts: $CONCEPT_COUNT (target: ≥2)
  - Exec Plans: $((ACTIVE_PLANS + COMPLETED_PLANS)) (target: ≥1)

## How to Improve

Run individual measurements:

\`\`\`bash
# Check navigation depth
python3 $SCRIPT_DIR/measure-navigation-depth.py --verbose

# Check context budget
python3 $SCRIPT_DIR/measure-context-budget.py

# Run all metrics
./scripts/measure-all-metrics.sh
\`\`\`

## Benchmarking Protocol

Before making major changes, run benchmarking:

1. Select 25-50 historical PRs/issues
2. Test with current docs structure
3. Measure:
   - Task completion rate
   - Token usage
   - Navigation steps
4. Only adopt changes that improve success >10% without increasing cost >15%

---

*This report is automatically generated by \`scripts/measure-all-metrics.sh --generate-reports\`*

---

## Relationship to QUALITY_SCORE.md

This automated metrics report complements the manual quality assessment in [QUALITY_SCORE.md](./QUALITY_SCORE.md):

- **QUALITY_SCORE.md** (manual): Tracks Navigation, Completeness, Freshness, Consistency, Correctness, Utility, Automation (7 categories)
- **METRICS_REPORT.md** (automated): Tracks Navigation Depth, Context Budget, Structure Compliance, Coverage (4 automated metrics)

Both are valuable - use QUALITY_SCORE.md for comprehensive assessment and improvement planning.
EOF

    echo -e "${GREEN}✅ Updated agentic/METRICS_REPORT.md${NC}"

    # Also append automated metrics summary to QUALITY_SCORE.md
    echo -e "${BLUE}Appending automated metrics to QUALITY_SCORE.md...${NC}"

    # Check if automated section already exists
    if ! grep -q "## Automated Metrics" agentic/QUALITY_SCORE.md 2>/dev/null; then
        cat >> agentic/QUALITY_SCORE.md <<EOF

---

## Automated Metrics

> **Last Run**: $(date +"%Y-%m-%d %H:%M:%S")
> **Source**: Generated by \`scripts/measure-all-metrics.sh\`

| Metric | Score | Status |
|--------|-------|--------|
| Navigation Depth | $NAVIGATION_SCORE/100 | $NAVIGATION_STATUS |
| Context Budget | $BUDGET_SCORE/100 | $BUDGET_STATUS |
| Structure Compliance | $STRUCTURE_SCORE/100 | $STRUCTURE_STATUS |
| Documentation Coverage | $COVERAGE_SCORE/100 | $COVERAGE_STATUS |

**Overall Automated Score**: $TOTAL_SCORE/100

See [METRICS_REPORT.md](./METRICS_REPORT.md) for detailed automated metrics.

EOF
        echo -e "${GREEN}✅ Appended automated metrics section to QUALITY_SCORE.md${NC}"
    else
        # Update existing section
        # Create temp file with updated metrics
        awk -v nav="$NAVIGATION_SCORE" -v navs="$NAVIGATION_STATUS" \
            -v bud="$BUDGET_SCORE" -v buds="$BUDGET_STATUS" \
            -v str="$STRUCTURE_SCORE" -v strs="$STRUCTURE_STATUS" \
            -v cov="$COVERAGE_SCORE" -v covs="$COVERAGE_STATUS" \
            -v tot="$TOTAL_SCORE" -v date="$(date +"%Y-%m-%d %H:%M:%S")" '
            /^> \*\*Last Run\*\*:/ { print "> **Last Run**: " date; next }
            /^\| Navigation Depth / { print "| Navigation Depth | " nav "/100 | " navs " |"; next }
            /^\| Context Budget / { print "| Context Budget | " bud "/100 | " buds " |"; next }
            /^\| Structure Compliance / { print "| Structure Compliance | " str "/100 | " strs " |"; next }
            /^\| Documentation Coverage / { print "| Documentation Coverage | " cov "/100 | " covs " |"; next }
            /^\*\*Overall Automated Score\*\*:/ { print "**Overall Automated Score**: " tot "/100"; print ""; next }
            { print }
        ' agentic/QUALITY_SCORE.md > agentic/QUALITY_SCORE.md.tmp
        mv agentic/QUALITY_SCORE.md.tmp agentic/QUALITY_SCORE.md
        echo -e "${GREEN}✅ Updated automated metrics section in QUALITY_SCORE.md${NC}"
    fi
fi

# Generate HTML dashboard if requested
if [ "$GENERATE_HTML" = true ]; then
    echo ""
    echo -e "${BLUE}Generating HTML dashboard...${NC}"
    if [ -f "$SCRIPT_DIR/generate-metrics-dashboard.py" ]; then
        python3 $SCRIPT_DIR/generate-metrics-dashboard.py
        echo -e "${GREEN}✅ HTML dashboard available at: agentic/metrics-dashboard.html${NC}"
        echo -e "${BLUE}   Open with: firefox agentic/metrics-dashboard.html${NC}"
    else
        echo -e "${YELLOW}⚠️  HTML dashboard generator not found${NC}"
    fi
fi

exit $EXIT_CODE
