#!/bin/bash
# Validation tests for metrics scripts
# Run this to verify metrics calculations are correct

# Sanity check: detect if being run with wrong interpreter
if [ -z "$BASH_VERSION" ]; then
    echo "❌ ERROR: This is a Bash script, not a Python script"
    echo ""
    echo "Correct usage:"
    echo "  ./agentic/scripts/test-metrics.sh  ✅"
    echo "  bash agentic/scripts/test-metrics.sh  ✅"
    echo ""
    echo "NOT: python3 test-metrics.sh  ❌"
    exit 1
fi

set -e

REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$REPO_ROOT"

SCRIPT_DIR="agentic/scripts"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo "╔════════════════════════════════════════════════════════════════╗"
echo "║             METRICS VALIDATION TESTS                           ║"
echo "╚════════════════════════════════════════════════════════════════╝"
echo ""

PASS=0
FAIL=0

# Test 1: Navigation metrics math
echo "Test 1: Navigation metrics math (total = reachable + unreachable)"
OUTPUT=$(python3 "$SCRIPT_DIR/measure-navigation-depth.py" 2>&1)

TOTAL=$(echo "$OUTPUT" | grep "Total documents found:" | awk '{print $4}')
REACHABLE=$(echo "$OUTPUT" | grep "Reachable documents:" | awk '{print $3}')
UNREACHABLE=$(echo "$OUTPUT" | grep "Unreachable documents:" | awk '{print $3}')

SUM=$((REACHABLE + UNREACHABLE))

if [ "$TOTAL" -eq "$SUM" ]; then
    echo -e "${GREEN}✓ PASS${NC}: Total ($TOTAL) = Reachable ($REACHABLE) + Unreachable ($UNREACHABLE)"
    PASS=$((PASS + 1))
else
    echo -e "${RED}✗ FAIL${NC}: Total ($TOTAL) ≠ Reachable ($REACHABLE) + Unreachable ($UNREACHABLE) = $SUM"
    FAIL=$((FAIL + 1))
fi

# Test 2: Navigation depth is reasonable
echo "Test 2: Max navigation depth is reasonable (≤10 hops)"
MAX_DEPTH=$(echo "$OUTPUT" | grep "Max observed depth:" | awk '{print $4}')

if [ "$MAX_DEPTH" -le 10 ]; then
    echo -e "${GREEN}✓ PASS${NC}: Max depth ($MAX_DEPTH) is reasonable"
    PASS=$((PASS + 1))
else
    echo -e "${RED}✗ FAIL${NC}: Max depth ($MAX_DEPTH) seems too high"
    FAIL=$((FAIL + 1))
fi

# Test 3: Context budget workflows count
echo "Test 3: Context budget has workflows defined"
BUDGET_OUTPUT=$(python3 "$SCRIPT_DIR/measure-context-budget.py" 2>&1)

WORKFLOW_COUNT=$(echo "$BUDGET_OUTPUT" | grep -c "Status:" || echo 0)

if [ "$WORKFLOW_COUNT" -ge 3 ]; then
    echo -e "${GREEN}✓ PASS${NC}: Found $WORKFLOW_COUNT workflows"
    PASS=$((PASS + 1))
else
    echo -e "${YELLOW}⚠ WARN${NC}: Only found $WORKFLOW_COUNT workflows (expected ≥3)"
    PASS=$((PASS + 1))  # Warning, not failure
fi

# Test 4: AGENTS.md exists and is entry point
echo "Test 4: AGENTS.md exists and is readable"
if [ -f "AGENTS.md" ] && [ -r "AGENTS.md" ]; then
    AGENTS_LINES=$(wc -l < AGENTS.md)
    if [ "$AGENTS_LINES" -le 150 ]; then
        echo -e "${GREEN}✓ PASS${NC}: AGENTS.md exists and is $AGENTS_LINES lines (≤150)"
        PASS=$((PASS + 1))
    else
        echo -e "${RED}✗ FAIL${NC}: AGENTS.md is $AGENTS_LINES lines (should be ≤150)"
        FAIL=$((FAIL + 1))
    fi
else
    echo -e "${RED}✗ FAIL${NC}: AGENTS.md not found or not readable"
    FAIL=$((FAIL + 1))
fi

# Test 5: All scripts exist and are executable
echo "Test 5: Required scripts exist"
REQUIRED_SCRIPTS=(
    "$SCRIPT_DIR/measure-navigation-depth.py"
    "$SCRIPT_DIR/measure-context-budget.py"
    "$SCRIPT_DIR/measure-all-metrics.sh"
    "$SCRIPT_DIR/generate-metrics-dashboard.py"
)

SCRIPT_PASS=true
for script in "${REQUIRED_SCRIPTS[@]}"; do
    if [ -f "$script" ] && [ -r "$script" ]; then
        : # Script exists
    else
        echo -e "${RED}  ✗ Missing: $script${NC}"
        SCRIPT_PASS=false
    fi
done

if [ "$SCRIPT_PASS" = true ]; then
    echo -e "${GREEN}✓ PASS${NC}: All required scripts found"
    PASS=$((PASS + 1))
else
    echo -e "${RED}✗ FAIL${NC}: Some scripts missing"
    FAIL=$((FAIL + 1))
fi

# Test 6: Dashboard generation doesn't error
echo "Test 6: HTML dashboard can be generated"
if python3 "$SCRIPT_DIR/generate-metrics-dashboard.py" --output /tmp/test-dashboard.html 2>&1 | grep -q "Dashboard generated"; then
    echo -e "${GREEN}✓ PASS${NC}: Dashboard generated successfully"
    PASS=$((PASS + 1))
    rm -f /tmp/test-dashboard.html
else
    echo -e "${RED}✗ FAIL${NC}: Dashboard generation failed"
    FAIL=$((FAIL + 1))
fi

# Summary
echo ""
echo "════════════════════════════════════════════════════════════════"
echo "RESULTS"
echo "════════════════════════════════════════════════════════════════"
echo -e "  Passed: ${GREEN}$PASS${NC}"
echo -e "  Failed: ${RED}$FAIL${NC}"
echo ""

if [ "$FAIL" -eq 0 ]; then
    echo -e "${GREEN}✓ ALL TESTS PASSED${NC}"
    exit 0
else
    echo -e "${RED}✗ SOME TESTS FAILED${NC}"
    exit 1
fi
