#!/usr/bin/env python3
"""
Fetch GitHub PR and parse QE Pre-merge testing comment.

Usage:
    python3 fetch_github_pr.py --repo openshift/machine-config-operator --pr 5691 --comment 4054832254
    python3 fetch_github_pr.py --repo openshift/machine-config-operator --pr 5691  # fetch all comments

Output:
    /tmp/test-specs/drafts/pr-<number>.json
"""

import json
import os
import re
import sys
from typing import Dict, Any, List, Optional
import requests


def load_env():
    """Load .env file from repo root"""
    script_dir = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.dirname(os.path.dirname(script_dir))
    env_file = os.path.join(repo_root, '.env')

    env_vars = {}
    if os.path.exists(env_file):
        with open(env_file) as f:
            for line in f:
                line = line.strip()
                if line and not line.startswith('#') and '=' in line:
                    key, value = line.split('=', 1)
                    env_vars[key] = value

    return env_vars


def fetch_pr_details(repo: str, pr_number: int, github_token: Optional[str] = None) -> Dict:
    """Fetch PR details from GitHub API"""
    url = f"https://api.github.com/repos/{repo}/pulls/{pr_number}"
    headers = {"Accept": "application/vnd.github.v3+json"}

    if github_token and github_token != 'your_github_token_here':
        headers["Authorization"] = f"token {github_token}"

    try:
        response = requests.get(url, headers=headers, timeout=30)
        response.raise_for_status()
        return response.json()
    except requests.exceptions.HTTPError as e:
        if e.response.status_code == 401 and github_token:
            # Token invalid, try without auth for public repo
            print(f"Warning: GitHub token invalid, trying without authentication...", file=sys.stderr)
            headers.pop("Authorization", None)
            response = requests.get(url, headers=headers, timeout=30)
            response.raise_for_status()
            return response.json()
        raise


def fetch_pr_comments(repo: str, pr_number: int, github_token: Optional[str] = None) -> List[Dict]:
    """Fetch all PR comments from GitHub API (handles pagination)"""
    url = f"https://api.github.com/repos/{repo}/issues/{pr_number}/comments"
    headers = {"Accept": "application/vnd.github.v3+json"}

    if github_token and github_token != 'your_github_token_here':
        headers["Authorization"] = f"token {github_token}"

    all_comments = []
    page = 1

    while True:
        try:
            response = requests.get(url, headers=headers, params={"per_page": 100, "page": page}, timeout=30)
            response.raise_for_status()
        except requests.exceptions.HTTPError as e:
            if e.response.status_code == 401 and github_token and "Authorization" in headers:
                print(f"Warning: GitHub token invalid, trying without authentication...", file=sys.stderr)
                headers.pop("Authorization", None)
                response = requests.get(url, headers=headers, params={"per_page": 100, "page": page}, timeout=30)
                response.raise_for_status()
            else:
                raise

        batch = response.json()
        if not batch:
            break
        all_comments.extend(batch)
        if len(batch) < 100:
            break
        page += 1

    return all_comments


def extract_jira_id(text: str) -> Optional[str]:
    """Extract Jira ID from PR title or body"""
    # Match OCPBUGS-12345 or MCO-1234
    match = re.search(r'(OCPBUGS-\d+|MCO-\d+)', text, re.IGNORECASE)
    if match:
        return match.group(1).upper()
    return None


def parse_environment_setup(content: str) -> Dict[str, str]:
    """Parse Environment Setup section"""
    env_data = {}

    # Find Environment Setup section — stop at next section header (blank line + Title: or ## or Steps:/Scenario)
    env_match = re.search(r'(?:##\s*)?Environment Setup:\s*\n(.*?)(?=\n\n(?:##|\w)|\nSteps:|\nScenario\s|\Z)', content, re.DOTALL | re.IGNORECASE)
    if not env_match:
        env_match = re.search(r'##\s*Environment Setup\s*\n(.*?)(?=\n##|\Z)', content, re.DOTALL | re.IGNORECASE)

    if not env_match:
        return env_data

    env_section = env_match.group(1)

    # Parse key-value pairs
    patterns = {
        'version': r'(?:OCP\s+)?[Vv]ersion[:\s]+([0-9.-]+)',  # Allow dashes for extended versions
        'platform': r'[Pp]latform[:\s]+(\w+)',
        'cluster_type': r'[Cc]luster\s+[Tt]ype[:\s]+(.+)',
        'nodes': r'[Nn]odes?[:\s]+(\d+)',
    }

    for key, pattern in patterns.items():
        match = re.search(pattern, env_section)
        if match:
            value = match.group(1).strip()
            # For version, extract base version (4.22) from extended format
            if key == 'version':
                base_version = re.match(r'(\d+\.\d+)', value)
                if base_version:
                    env_data[key] = base_version.group(1)
                else:
                    env_data[key] = value
            else:
                env_data[key] = value

    return env_data


def _extract_steps_section(content: str) -> Optional[str]:
    """Extract the Steps section, respecting fenced code blocks."""
    # Find where the Steps section starts
    header_match = re.search(
        r'(?:##\s*)?(?:Test(?:ing)?\s+)?Steps:\s*\n',
        content, re.IGNORECASE
    )
    if not header_match:
        header_match = re.search(
            r'##\s*(?:Test(?:ing)?\s+Steps|Steps)\s*\n',
            content, re.IGNORECASE
        )
    if not header_match:
        return None

    body = content[header_match.end():]
    lines = []
    in_fence = False
    for line in body.splitlines():
        if line.strip().startswith('```'):
            in_fence = not in_fence
        if not in_fence:
            # Stop at next section header outside a fence
            if re.match(r'##\s+', line) or re.match(r'[A-Z][a-z]+:\s*$', line):
                break
        lines.append(line)
    return '\n'.join(lines)


def _split_bullets(section: str) -> List[str]:
    """Split section into bullet items, ignoring bullet-like lines inside fences."""
    bullets = []
    current = []
    in_fence = False
    for line in section.splitlines():
        if line.strip().startswith('```'):
            in_fence = not in_fence

        if not in_fence and re.match(r'^[-*]\s+', line):
            if current:
                bullets.append('\n'.join(current))
            current = [re.sub(r'^[-*]\s+', '', line, count=1)]
        else:
            current.append(line)

    if current:
        bullets.append('\n'.join(current))
    return [b.strip() for b in bullets if b.strip()]


def parse_test_steps(content: str) -> List[Dict[str, str]]:
    """
    Parse test steps from QE comment format:
    - Bullet points → step column
    - ``` code blocks → expected_result column
    """
    steps = []

    steps_section = _extract_steps_section(content)
    if not steps_section:
        return steps

    bullets = _split_bullets(steps_section)

    for bullet_content in bullets:
        # Extract step text (before code block)
        # Extract code block as expected result
        step_text = bullet_content.strip()
        expected_result = ''

        # Find code blocks (handle both ``` and inline code)
        code_blocks = re.findall(r'```(?:\w+)?\s*\n(.*?)```', bullet_content, re.DOTALL)
        if code_blocks:
            # Concatenate all code blocks for this step
            expected_result = '\n\n'.join(block.strip() for block in code_blocks)

            # Remove code blocks from step text
            step_text = re.sub(r'```(?:\w+)?\s*\n.*?```', '', bullet_content, flags=re.DOTALL).strip()
        else:
            # No code block found - split at newline to see if there's prose after the bullet
            lines = bullet_content.strip().split('\n', 1)
            step_text = lines[0].strip()
            if len(lines) > 1:
                # Use remaining lines as expected result
                expected_result = lines[1].strip()

        # Clean up step text (remove extra whitespace)
        step_text = re.sub(r'\s+', ' ', step_text).strip()

        if step_text:
            steps.append({
                'step': step_text,
                'expected_result': expected_result
            })

    return steps


def find_qe_comment(comments: List[Dict]) -> Optional[Dict]:
    """Find most recent QE pre-merge testing comment"""
    for comment in reversed(comments):
        body = comment.get('body', '')
        if re.search(r'pre-?merge\s+test(?:ing)?', body, re.IGNORECASE):
            return comment

        # Also check for "Test Steps" or "Environment Setup" headers
        if re.search(r'##\s*(?:Test(?:ing)?\s+Steps|Environment Setup)', body, re.IGNORECASE):
            return comment

    return None


def parse_qe_comment(comment_body: str, pr_title: str, pr_body: str) -> Dict:
    """Parse QE comment into test case draft"""
    # Extract Jira ID from PR title or body
    jira_id = extract_jira_id(pr_title) or extract_jira_id(pr_body or '')

    # Create title in [MCO][JIRA-ID] format
    title = pr_title
    if jira_id:
        if re.match(r'^\[MCO\]\[[A-Z0-9-]+\]', title):
            pass
        elif re.match(r'^\[MCO\]', title):
            title = f"[MCO][{jira_id}] {re.sub(r'^\[MCO\]\s*', '', title, count=1)}"
        else:
            title = f"[MCO][{jira_id}] {title}"

    # Parse environment
    env_data = parse_environment_setup(comment_body)

    # Parse test steps
    test_steps = parse_test_steps(comment_body)

    # Extract description from comment (before ## sections)
    description_match = re.search(r'^(.*?)(?=\n##|\Z)', comment_body, re.DOTALL)
    description = description_match.group(1).strip() if description_match else ''

    # Build draft
    draft = {
        'title': title,
        'description': description or pr_title,
        'component': 'Machine Config Operator',
        'sub_team': 'MCO',
        'products': 'OCP',
        'test_type': 'Functional',
        'version': env_data.get('version', ''),
        'trello_jira': jira_id or '',
        'test_steps': test_steps,
        'metadata': {
            'source_pr': pr_title,
            'environment': env_data
        }
    }

    return draft


def main():
    import argparse

    parser = argparse.ArgumentParser(description='Fetch GitHub PR and parse QE testing comment')
    parser.add_argument('--repo', required=True, help='Repository (e.g., openshift/machine-config-operator)')
    parser.add_argument('--pr', type=int, required=True, help='PR number')
    parser.add_argument('--comment', type=int, help='Comment ID (optional - will find QE comment if not provided)')
    parser.add_argument('--output', help='Output file (default: /tmp/test-specs/drafts/pr-<number>.json)')

    args = parser.parse_args()

    # Load environment
    env = load_env()
    github_token = env.get('GITHUB_TOKEN')

    if not github_token:
        print("Warning: GITHUB_TOKEN not found in .env (rate limits may apply)", file=sys.stderr)

    try:
        # Fetch PR details
        print(f"Fetching PR #{args.pr} from {args.repo}...", file=sys.stderr)
        pr_data = fetch_pr_details(args.repo, args.pr, github_token)
        pr_title = pr_data['title']
        pr_body = pr_data.get('body', '')

        print(f"PR Title: {pr_title}", file=sys.stderr)

        # Fetch comments
        print("Fetching comments...", file=sys.stderr)
        comments = fetch_pr_comments(args.repo, args.pr, github_token)

        # Find target comment
        target_comment = None
        if args.comment:
            # Find specific comment by ID
            for comment in comments:
                if comment['id'] == args.comment:
                    target_comment = comment
                    break

            if not target_comment:
                print(f"Error: Comment {args.comment} not found", file=sys.stderr)
                sys.exit(1)
        else:
            # Find QE comment
            target_comment = find_qe_comment(comments)
            if not target_comment:
                print("Error: No QE pre-merge testing comment found", file=sys.stderr)
                print("Use --comment <id> to specify a specific comment", file=sys.stderr)
                sys.exit(1)

        comment_body = target_comment['body']
        comment_id = target_comment['id']

        print(f"Found comment {comment_id}", file=sys.stderr)

        # Parse comment
        draft = parse_qe_comment(comment_body, pr_title, pr_body)

        if not draft.get('test_steps'):
            print("Error: No test steps parsed from QE comment", file=sys.stderr)
            print("The comment may use a format the parser doesn't recognize.", file=sys.stderr)
            sys.exit(1)

        # Output
        if args.output:
            output_file = args.output
        else:
            output_dir = '/tmp/test-specs/drafts'
            os.makedirs(output_dir, exist_ok=True)
            output_file = os.path.join(output_dir, f"pr-{args.pr}.json")

        # Write draft
        with open(output_file, 'w') as f:
            json.dump(draft, f, indent=2)

        print(f"\n✓ Draft saved to: {output_file}", file=sys.stderr)
        print(f"  Title: {draft['title']}", file=sys.stderr)
        print(f"  Jira: {draft.get('trello_jira', 'NOT FOUND')}", file=sys.stderr)
        print(f"  Version: {draft.get('version', 'NOT FOUND')}", file=sys.stderr)
        print(f"  Test Steps: {len(draft['test_steps'])}", file=sys.stderr)

        # Also output JSON to stdout
        print(json.dumps(draft, indent=2))

    except requests.HTTPError as e:
        print(f"GitHub API error: {e}", file=sys.stderr)
        print(f"Response: {e.response.text if hasattr(e, 'response') else 'N/A'}", file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        import traceback
        traceback.print_exc()
        sys.exit(1)


if __name__ == '__main__':
    main()
