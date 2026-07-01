#!/bin/bash
# MCO QE Tooling Setup Script

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLAUDE_DIR="$(dirname "$SCRIPT_DIR")"
REPO_ROOT="$(dirname "$CLAUDE_DIR")"

VERIFY_MODE=false
if [[ "$1" == "--verify" ]]; then
    VERIFY_MODE=true
fi

echo "=== MCO QE Tooling Setup ==="
echo

# Check Python version
echo "Checking Python version..."
if ! command -v python3 &> /dev/null; then
    echo "❌ Python 3 not found. Please install Python 3.9 or higher."
    exit 1
fi

PYTHON_VERSION=$(python3 --version | awk '{print $2}')
PYTHON_MAJOR=$(echo "$PYTHON_VERSION" | cut -d. -f1)
PYTHON_MINOR=$(echo "$PYTHON_VERSION" | cut -d. -f2)

if [[ "$PYTHON_MAJOR" -lt 3 ]] || [[ "$PYTHON_MAJOR" -eq 3 && "$PYTHON_MINOR" -lt 9 ]]; then
    echo "❌ Python 3.9+ required (found $PYTHON_VERSION)"
    exit 1
fi

echo "✓ Python $PYTHON_VERSION found"
echo

# Check/install requests library
echo "Checking Python dependencies..."
for pkg in requests defusedxml; do
    if ! python3 -c "import $pkg" 2>/dev/null; then
        if [[ "$VERIFY_MODE" == true ]]; then
            echo "❌ $pkg library not installed"
            exit 1
        fi
        echo "Installing $pkg library..."
        python3 -m pip install --user "$pkg"
        echo "✓ $pkg installed"
    else
        echo "✓ $pkg already installed"
    fi
done
echo

# Check gh CLI
echo "Checking gh CLI..."
if ! command -v gh &> /dev/null; then
    echo "⚠️  gh CLI not found (optional for GitHub integration)"
    if [[ "$VERIFY_MODE" == true ]]; then
        echo "   Install: https://cli.github.com/"
    fi
else
    GH_VERSION=$(gh version 2>&1 | head -n1 | awk '{print $3}')
    echo "✓ gh CLI $GH_VERSION found"

    # Check gh auth status
    if gh auth status &> /dev/null; then
        GH_USER=$(gh api user --jq .login 2>/dev/null || echo "unknown")
        echo "✓ gh authenticated as $GH_USER"
    else
        echo "⚠️  gh not authenticated (optional)"
        if [[ "$VERIFY_MODE" == true ]]; then
            echo "   Run: gh auth login"
        fi
    fi
fi
echo

# Create /tmp/test-specs directory structure
echo "Creating /tmp/test-specs directory structure..."
mkdir -p /tmp/test-specs/{polarion,github,jira,steps}
echo "✓ Created /tmp/test-specs/"
echo "  - /tmp/test-specs/polarion/  (fetched Polarion TCs)"
echo "  - /tmp/test-specs/github/    (fetched GitHub issues)"
echo "  - /tmp/test-specs/jira/      (fetched Jira tickets)"
echo "  - /tmp/test-specs/steps/     (test step definitions)"
echo

# Check if .env exists at repo root
if [ ! -f "$REPO_ROOT/.env" ]; then
    if [[ "$VERIFY_MODE" == true ]]; then
        echo "❌ .env not found at repo root"
        echo "   Copy from: cp .env.example .env"
        exit 1
    fi
    echo "Creating .env from template..."
    if [ -f "$REPO_ROOT/.env.example" ]; then
        cp "$REPO_ROOT/.env.example" "$REPO_ROOT/.env"
        echo "✓ Created .env (please edit with your credentials)"
    else
        echo "❌ .env.example not found at repo root"
        exit 1
    fi
    echo
    echo "⚠️  NEXT STEPS:"
    echo "   1. Edit .env and add your POLARION_TOKEN"
    echo "   2. Generate token at: https://polarion.engineering.redhat.com/polarion/#/user/me"
    echo "   3. Run: ./scripts/setup.sh --verify"
else
    echo "✓ .env exists at repo root"

    # Verify POLARION_TOKEN is set
    if grep -q "^POLARION_TOKEN=your_personal_access_token_here" "$REPO_ROOT/.env" 2>/dev/null || \
       ! grep -q "^POLARION_TOKEN=..*" "$REPO_ROOT/.env" 2>/dev/null; then
        echo "⚠️  POLARION_TOKEN not configured in .env"
        if [[ "$VERIFY_MODE" == true ]]; then
            echo "❌ Setup incomplete - configure POLARION_TOKEN"
            exit 1
        fi
    else
        echo "✓ POLARION_TOKEN configured"

        # Ping Polarion in verify mode
        if [[ "$VERIFY_MODE" == true ]]; then
            echo
            echo "Testing Polarion connection..."

            POLARION_URL="$(grep -E '^POLARION_URL=' "$REPO_ROOT/.env" | cut -d= -f2-)"
            POLARION_TOKEN="$(grep -E '^POLARION_TOKEN=' "$REPO_ROOT/.env" | cut -d= -f2-)"
            POLARION_PROJECT="$(grep -E '^POLARION_PROJECT=' "$REPO_ROOT/.env" | cut -d= -f2-)"
            POLARION_URL=${POLARION_URL:-https://polarion.engineering.redhat.com}
            POLARION_PROJECT=${POLARION_PROJECT:-OSE}

            # Test connection with curl
            HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" \
                -H "Authorization: Bearer $POLARION_TOKEN" \
                -H "Accept: application/json" \
                "$POLARION_URL/polarion/rest/v1/projects/$POLARION_PROJECT" 2>/dev/null || echo "000")

            if [[ "$HTTP_CODE" == "200" ]]; then
                echo "✓ Polarion connection successful (HTTP 200)"

                # Get project name
                PROJECT_NAME=$(curl -s \
                    -H "Authorization: Bearer $POLARION_TOKEN" \
                    -H "Accept: application/json" \
                    "$POLARION_URL/polarion/rest/v1/projects/$POLARION_PROJECT" 2>/dev/null | \
                    python3 -c "import sys, json; print(json.load(sys.stdin)['data']['attributes']['name'])" 2>/dev/null || echo "")

                if [[ -n "$PROJECT_NAME" ]]; then
                    echo "  Project: $PROJECT_NAME ($POLARION_PROJECT)"
                fi
            elif [[ "$HTTP_CODE" == "401" ]]; then
                echo "❌ Polarion authentication failed (HTTP 401)"
                echo "   Check POLARION_TOKEN in .env"
                exit 1
            elif [[ "$HTTP_CODE" == "000" ]]; then
                echo "❌ Cannot reach Polarion server"
                echo "   Check network connection and POLARION_URL"
                exit 1
            else
                echo "❌ Polarion returned HTTP $HTTP_CODE"
                exit 1
            fi
        fi
    fi
fi
echo

# Update .gitignore
echo "Updating .gitignore..."
if ! grep -q "^\.env$" "$REPO_ROOT/.gitignore" 2>/dev/null; then
    echo "
# MCO QE Tooling
.env
.claude/.env" >> "$REPO_ROOT/.gitignore"
    echo "✓ Added .env to .gitignore"
else
    echo "✓ .gitignore already configured"
fi
echo

# Make scripts executable
echo "Making scripts executable..."
chmod +x "$SCRIPT_DIR"/*.sh
chmod +x "$SCRIPT_DIR"/*.py 2>/dev/null || true
echo "✓ Scripts are executable"
echo

if [[ "$VERIFY_MODE" == true ]]; then
    echo "=== Verification Complete ==="
    echo "✓ All checks passed"
else
    echo "=== Setup Complete ==="
    echo
    echo "Next steps:"
    echo "1. Edit .env with your Polarion token"
    echo "2. Run verification: ./scripts/setup.sh --verify"
    echo "3. See .claude/README.md for usage examples"
fi
echo
